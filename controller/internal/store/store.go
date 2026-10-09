// Package store defines the persistence interface and its Postgres implementation.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/ramnindra/fleetmanagementplatform/controller/internal/model"
)

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("already exists")
)

type Store interface {
	Ping(ctx context.Context) error

	CreateDevice(ctx context.Context, d *model.Device) error // ErrConflict if device_id exists
	GetDevice(ctx context.Context, id string) (*model.Device, error)
	// SaveDeviceAuth persists status and the enrollment/credential fields.
	SaveDeviceAuth(ctx context.Context, d *model.Device) error
	ListDevices(ctx context.Context, limit, offset int) ([]model.Device, error)
	TouchLastSeen(ctx context.Context, id string, at time.Time) error
	// CountDevices returns devices seen at/after cutoff, and the total.
	CountDevices(ctx context.Context, cutoff time.Time) (online, total int, err error)

	// CreateCommand inserts c; if c.IdempotencyKey matches an existing command
	// for the device, that one is returned with created=false.
	CreateCommand(ctx context.Context, c *model.Command) (cmd *model.Command, created bool, err error)
	GetCommand(ctx context.Context, id string) (*model.Command, error)
	ListCommands(ctx context.Context, deviceID string, limit int) ([]model.Command, error)
	// ListRecentCommands returns the newest commands across all devices.
	ListRecentCommands(ctx context.Context, limit int) ([]model.Command, error)
	MarkDelivered(ctx context.Context, id string, at time.Time) error
	// ApplyAck is a no-op (applied=false) for unknown or already-terminal commands.
	// receivedAt is the device-reported receipt time, if it sent one.
	ApplyAck(ctx context.Context, id, status string, result map[string]any, receivedAt *time.Time, at time.Time) (applied bool, err error)

	AddMessage(ctx context.Context, m *model.DeviceMessage, retention time.Duration) error
	ListMessages(ctx context.Context, deviceID string, limit int, afterID int64) ([]model.DeviceMessage, error)
}
