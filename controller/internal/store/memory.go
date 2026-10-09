package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/ramnindra/fleetmanagementplatform/controller/internal/model"
)

// Memory is an in-process Store used by unit and API tests.
type Memory struct {
	mu       sync.Mutex
	devices  map[string]*model.Device
	commands map[string]*model.Command
	messages []model.DeviceMessage
	nextMsg  int64
}

func NewMemory() *Memory {
	return &Memory{devices: map[string]*model.Device{}, commands: map[string]*model.Command{}}
}

func (m *Memory) Ping(context.Context) error { return nil }

func (m *Memory) CreateDevice(_ context.Context, d *model.Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.devices[d.DeviceID]; ok {
		return ErrConflict
	}
	now := time.Now().UTC()
	d.CreatedAt, d.UpdatedAt = now, now
	cp := *d
	m.devices[d.DeviceID] = &cp
	return nil
}

func (m *Memory) GetDevice(_ context.Context, id string) (*model.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *d
	return &cp, nil
}

func (m *Memory) SaveDeviceAuth(_ context.Context, d *model.Device) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.devices[d.DeviceID]
	if !ok {
		return ErrNotFound
	}
	cur.Status, cur.EnrollmentTokenHash, cur.EnrollmentTokenExpiresAt, cur.CredentialHash =
		d.Status, d.EnrollmentTokenHash, d.EnrollmentTokenExpiresAt, d.CredentialHash
	return nil
}

func (m *Memory) ListDevices(_ context.Context, limit, offset int) ([]model.Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]model.Device, 0, len(m.devices))
	for _, d := range m.devices {
		all = append(all, *d)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	if offset > len(all) {
		offset = len(all)
	}
	all = all[offset:]
	if limit < len(all) {
		all = all[:limit]
	}
	return all, nil
}

func (m *Memory) TouchLastSeen(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.devices[id]; ok {
		d.LastSeenAt = &at
	}
	return nil
}

func (m *Memory) CountDevices(_ context.Context, cutoff time.Time) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	online := 0
	for _, d := range m.devices {
		if d.LastSeenAt != nil && !d.LastSeenAt.Before(cutoff) {
			online++
		}
	}
	return online, len(m.devices), nil
}

func (m *Memory) CreateCommand(_ context.Context, c *model.Command) (*model.Command, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.IdempotencyKey != nil {
		for _, e := range m.commands {
			if e.DeviceID == c.DeviceID && e.IdempotencyKey != nil && *e.IdempotencyKey == *c.IdempotencyKey {
				cp := *e
				return &cp, false, nil
			}
		}
	}
	c.CreatedAt = time.Now().UTC()
	cp := *c
	m.commands[c.CommandID] = &cp
	return c, true, nil
}

func (m *Memory) GetCommand(_ context.Context, id string) (*model.Command, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.commands[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *c
	return &cp, nil
}

func (m *Memory) ListCommands(_ context.Context, deviceID string, limit int) ([]model.Command, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []model.Command{}
	for _, c := range m.commands {
		if c.DeviceID == deviceID {
			out = append(out, *c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) ListRecentCommands(_ context.Context, limit int) ([]model.Command, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Command, 0, len(m.commands))
	for _, c := range m.commands {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if limit < len(out) {
		out = out[:limit]
	}
	return out, nil
}

func (m *Memory) MarkDelivered(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.commands[id]; ok && !model.IsTerminal(c.Status) {
		c.Status, c.DeliveredAt = model.CmdDelivered, &at
	}
	return nil
}

func (m *Memory) ApplyAck(_ context.Context, id, status string, result map[string]any, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.commands[id]
	if !ok || model.IsTerminal(c.Status) {
		return false, nil
	}
	c.Status, c.Result, c.CompletedAt = status, result, &at
	return true, nil
}

func (m *Memory) AddMessage(_ context.Context, msg *model.DeviceMessage, retention time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextMsg++
	msg.ID = m.nextMsg
	msg.ReceivedAt = time.Now().UTC()
	m.messages = append(m.messages, *msg)
	kept := m.messages[:0]
	for _, x := range m.messages {
		if x.DeviceID == msg.DeviceID && x.ReceivedAt.Before(msg.ReceivedAt.Add(-retention)) {
			continue
		}
		kept = append(kept, x)
	}
	m.messages = kept
	return nil
}

func (m *Memory) ListMessages(_ context.Context, deviceID string, limit int, afterID int64) ([]model.DeviceMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []model.DeviceMessage{}
	for i := len(m.messages) - 1; i >= 0; i-- {
		x := m.messages[i]
		if x.DeviceID == deviceID && x.ID > afterID {
			out = append(out, x)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}
