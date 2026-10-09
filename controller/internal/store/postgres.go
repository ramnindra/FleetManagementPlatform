package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ramnindra/fleetmanagementplatform/controller/internal/model"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Postgres struct{ pool *pgxpool.Pool }

func NewPostgres(ctx context.Context, url string, maxConns int) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

// Migrate applies embedded SQL migrations in filename order. Each file is
// idempotent (IF NOT EXISTS), so databases first created by the earlier
// Alembic-managed Python controller upgrade cleanly. A Postgres advisory lock
// serializes concurrent replicas.
func (p *Postgres) Migrate(ctx context.Context) error {
	conn, err := p.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(727274)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(727274)`) //nolint:errcheck

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		sqlText, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, string(sqlText)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
		slog.Info("migration_applied", "name", name)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func mustJSON(m map[string]any) []byte {
	if m == nil {
		m = map[string]any{}
	}
	b, _ := json.Marshal(m)
	return b
}

func jsonOrNil(m map[string]any) any {
	if m == nil {
		return nil
	}
	return mustJSON(m)
}

func decode(b []byte) map[string]any {
	if len(b) == 0 {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

const deviceCols = `device_id, device_type, status, enrollment_token_hash, enrollment_token_expires_at,
	credential_hash, last_seen_at, attributes, created_at, updated_at`

func scanDevice(row pgx.Row) (*model.Device, error) {
	var d model.Device
	var attrs []byte
	err := row.Scan(&d.DeviceID, &d.DeviceType, &d.Status, &d.EnrollmentTokenHash, &d.EnrollmentTokenExpiresAt,
		&d.CredentialHash, &d.LastSeenAt, &attrs, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Attributes = decode(attrs)
	if d.Attributes == nil {
		d.Attributes = map[string]any{}
	}
	return &d, nil
}

func (p *Postgres) CreateDevice(ctx context.Context, d *model.Device) error {
	now := time.Now().UTC()
	d.CreatedAt, d.UpdatedAt = now, now
	_, err := p.pool.Exec(ctx, `INSERT INTO devices
		(device_id, device_type, status, enrollment_token_hash, enrollment_token_expires_at, attributes, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$7)`,
		d.DeviceID, d.DeviceType, d.Status, d.EnrollmentTokenHash, d.EnrollmentTokenExpiresAt, mustJSON(d.Attributes), now)
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (p *Postgres) GetDevice(ctx context.Context, id string) (*model.Device, error) {
	return scanDevice(p.pool.QueryRow(ctx, `SELECT `+deviceCols+` FROM devices WHERE device_id=$1`, id))
}

func (p *Postgres) SaveDeviceAuth(ctx context.Context, d *model.Device) error {
	d.UpdatedAt = time.Now().UTC()
	tag, err := p.pool.Exec(ctx, `UPDATE devices SET status=$2, enrollment_token_hash=$3,
		enrollment_token_expires_at=$4, credential_hash=$5, updated_at=$6 WHERE device_id=$1`,
		d.DeviceID, d.Status, d.EnrollmentTokenHash, d.EnrollmentTokenExpiresAt, d.CredentialHash, d.UpdatedAt)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (p *Postgres) ListDevices(ctx context.Context, limit, offset int) ([]model.Device, error) {
	rows, err := p.pool.Query(ctx, `SELECT `+deviceCols+` FROM devices ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (p *Postgres) TouchLastSeen(ctx context.Context, id string, at time.Time) error {
	_, err := p.pool.Exec(ctx, `UPDATE devices SET last_seen_at=$2, updated_at=$2 WHERE device_id=$1`, id, at)
	return err
}

func (p *Postgres) CountDevices(ctx context.Context, cutoff time.Time) (int, int, error) {
	var online, total int
	err := p.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE last_seen_at >= $1), count(*) FROM devices`, cutoff).Scan(&online, &total)
	return online, total, err
}

const commandCols = `command_id, device_id, action, params, status, result, idempotency_key, created_at, delivered_at, completed_at`

func scanCommand(row pgx.Row) (*model.Command, error) {
	var c model.Command
	var params, result []byte
	err := row.Scan(&c.CommandID, &c.DeviceID, &c.Action, &params, &c.Status, &result, &c.IdempotencyKey,
		&c.CreatedAt, &c.DeliveredAt, &c.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.Params = decode(params)
	if c.Params == nil {
		c.Params = map[string]any{}
	}
	c.Result = decode(result)
	return &c, nil
}

func (p *Postgres) CreateCommand(ctx context.Context, c *model.Command) (*model.Command, bool, error) {
	c.CreatedAt = time.Now().UTC()
	tag, err := p.pool.Exec(ctx, `INSERT INTO commands
		(command_id, device_id, action, params, status, idempotency_key, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (device_id, idempotency_key) DO NOTHING`,
		c.CommandID, c.DeviceID, c.Action, mustJSON(c.Params), c.Status, c.IdempotencyKey, c.CreatedAt)
	if err != nil {
		return nil, false, err
	}
	if tag.RowsAffected() == 1 {
		return c, true, nil
	}
	existing, err := scanCommand(p.pool.QueryRow(ctx,
		`SELECT `+commandCols+` FROM commands WHERE device_id=$1 AND idempotency_key=$2`, c.DeviceID, c.IdempotencyKey))
	return existing, false, err
}

func (p *Postgres) GetCommand(ctx context.Context, id string) (*model.Command, error) {
	return scanCommand(p.pool.QueryRow(ctx, `SELECT `+commandCols+` FROM commands WHERE command_id=$1`, id))
}

func (p *Postgres) ListCommands(ctx context.Context, deviceID string, limit int) ([]model.Command, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT `+commandCols+` FROM commands WHERE device_id=$1 ORDER BY created_at DESC LIMIT $2`, deviceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Command{}
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (p *Postgres) MarkDelivered(ctx context.Context, id string, at time.Time) error {
	_, err := p.pool.Exec(ctx, `UPDATE commands SET status='delivered', delivered_at=$2
		WHERE command_id=$1 AND status NOT IN ('success','failed','expired')`, id, at)
	return err
}

func (p *Postgres) ApplyAck(ctx context.Context, id, status string, result map[string]any, at time.Time) (bool, error) {
	tag, err := p.pool.Exec(ctx, `UPDATE commands SET status=$2, result=$3, completed_at=$4
		WHERE command_id=$1 AND status NOT IN ('success','failed','expired')`, id, status, jsonOrNil(result), at)
	return tag.RowsAffected() == 1, err
}

func (p *Postgres) AddMessage(ctx context.Context, m *model.DeviceMessage, retention time.Duration) error {
	m.ReceivedAt = time.Now().UTC()
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO device_messages (device_id, kind, payload, received_at) VALUES ($1,$2,$3,$4)`,
		m.DeviceID, m.Kind, mustJSON(m.Payload), m.ReceivedAt)
	batch.Queue(`DELETE FROM device_messages WHERE device_id=$1 AND received_at < $2`,
		m.DeviceID, m.ReceivedAt.Add(-retention))
	return p.pool.SendBatch(ctx, batch).Close()
}

func (p *Postgres) ListMessages(ctx context.Context, deviceID string, limit int, afterID int64) ([]model.DeviceMessage, error) {
	rows, err := p.pool.Query(ctx, `SELECT id, device_id, kind, payload, received_at FROM device_messages
		WHERE device_id=$1 AND id > $2 ORDER BY id DESC LIMIT $3`, deviceID, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.DeviceMessage{}
	for rows.Next() {
		var m model.DeviceMessage
		var payload []byte
		if err := rows.Scan(&m.ID, &m.DeviceID, &m.Kind, &payload, &m.ReceivedAt); err != nil {
			return nil, err
		}
		m.Payload = decode(payload)
		if m.Payload == nil {
			m.Payload = map[string]any{}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
