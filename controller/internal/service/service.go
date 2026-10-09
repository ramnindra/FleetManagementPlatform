// Package service holds the controller's device and command business logic.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ramnindra/fleetmanagementplatform/controller/internal/model"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/security"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/store"
)

// MessageRetention bounds device_messages: the table feeds the web UI, not a
// telemetry archive (that belongs in Prometheus / a time-series store).
const MessageRetention = time.Hour

var (
	ErrDeviceConflict = errors.New("device already registered")
	ErrDeviceNotFound = errors.New("device not found")
)

// EnrollmentError is a client-visible enrollment/credential lifecycle failure.
type EnrollmentError struct{ Msg string }

func (e *EnrollmentError) Error() string { return e.Msg }

func enrollErr(format string, a ...any) error { return &EnrollmentError{fmt.Sprintf(format, a...)} }

type Service struct{ Store store.Store }

func New(s store.Store) *Service { return &Service{Store: s} }

func (s *Service) RegisterDevice(ctx context.Context, id, deviceType string, attrs map[string]any, ttl time.Duration) (*model.Device, string, error) {
	rawToken := security.GenerateToken()
	hash := security.HashSecret(rawToken)
	expires := time.Now().UTC().Add(ttl)
	if attrs == nil {
		attrs = map[string]any{}
	}
	d := &model.Device{
		DeviceID: id, DeviceType: deviceType, Status: model.StatusPendingEnrollment,
		EnrollmentTokenHash: &hash, EnrollmentTokenExpiresAt: &expires, Attributes: attrs,
	}
	if err := s.Store.CreateDevice(ctx, d); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, "", fmt.Errorf("device %s already registered: %w", id, ErrDeviceConflict)
		}
		return nil, "", err
	}
	slog.Info("device_registered", "device_id", id)
	return d, rawToken, nil
}

func (s *Service) EnrollDevice(ctx context.Context, id, token string) (*model.Device, string, error) {
	d, err := s.Store.GetDevice(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", enrollErr("unknown device_id")
	}
	if err != nil {
		return nil, "", err
	}
	switch {
	case d.Status == model.StatusActive:
		return nil, "", enrollErr("device already enrolled")
	case d.Status == model.StatusRevoked:
		return nil, "", enrollErr("device has been revoked")
	case d.EnrollmentTokenHash == nil || d.EnrollmentTokenExpiresAt == nil:
		return nil, "", enrollErr("no enrollment token issued for this device")
	case time.Now().After(*d.EnrollmentTokenExpiresAt):
		return nil, "", enrollErr("enrollment token expired")
	case !security.VerifySecret(token, *d.EnrollmentTokenHash):
		return nil, "", enrollErr("invalid enrollment token")
	}

	rawCred := security.GenerateToken()
	h := security.HashSecret(rawCred)
	d.CredentialHash = &h
	d.Status = model.StatusActive
	d.EnrollmentTokenHash, d.EnrollmentTokenExpiresAt = nil, nil
	if err := s.Store.SaveDeviceAuth(ctx, d); err != nil {
		return nil, "", err
	}
	slog.Info("device_enrolled", "device_id", id)
	return d, rawCred, nil
}

func (s *Service) GetDevice(ctx context.Context, id string) (*model.Device, error) {
	d, err := s.Store.GetDevice(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrDeviceNotFound
	}
	return d, err
}

func (s *Service) ListDevices(ctx context.Context, limit, offset int) ([]model.Device, error) {
	return s.Store.ListDevices(ctx, limit, offset)
}

// IsOnline reports whether the device heartbeat is within the threshold, and
// the age of the last heartbeat in seconds (nil if never seen).
func IsOnline(d *model.Device, threshold time.Duration) (bool, *float64) {
	if d.LastSeenAt == nil {
		return false, nil
	}
	age := time.Since(*d.LastSeenAt).Seconds()
	return age < threshold.Seconds(), &age
}

func (s *Service) RecordHeartbeat(ctx context.Context, id string) error {
	if _, err := s.Store.GetDevice(ctx, id); errors.Is(err, store.ErrNotFound) {
		slog.Warn("heartbeat_from_unknown_device", "device_id", id)
		return nil
	} else if err != nil {
		return err
	}
	return s.Store.TouchLastSeen(ctx, id, time.Now().UTC())
}

func (s *Service) AuthenticateDevice(ctx context.Context, id, credential string) bool {
	d, err := s.Store.GetDevice(ctx, id)
	if err != nil || d.Status != model.StatusActive || d.CredentialHash == nil {
		return false
	}
	return security.VerifySecret(credential, *d.CredentialHash)
}

// RotateCredential revokes the current credential immediately and re-opens
// enrollment with a fresh one-time token. The device is offline until it
// re-enrolls (deliberate simplification; see docs/provisioning.md).
func (s *Service) RotateCredential(ctx context.Context, id string, ttl time.Duration) (*model.Device, string, error) {
	d, err := s.Store.GetDevice(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", enrollErr("unknown device_id")
	}
	if err != nil {
		return nil, "", err
	}
	if d.Status != model.StatusActive {
		return nil, "", enrollErr("cannot rotate credential for device in status '%s'", d.Status)
	}
	raw := security.GenerateToken()
	h := security.HashSecret(raw)
	exp := time.Now().UTC().Add(ttl)
	d.CredentialHash = nil
	d.Status = model.StatusPendingEnrollment
	d.EnrollmentTokenHash, d.EnrollmentTokenExpiresAt = &h, &exp
	if err := s.Store.SaveDeviceAuth(ctx, d); err != nil {
		return nil, "", err
	}
	slog.Info("device_credential_rotated", "device_id", id)
	return d, raw, nil
}

// RevokeDevice permanently revokes a device; device_ids are never recycled.
func (s *Service) RevokeDevice(ctx context.Context, id string) (*model.Device, error) {
	d, err := s.Store.GetDevice(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, enrollErr("unknown device_id")
	}
	if err != nil {
		return nil, err
	}
	d.Status = model.StatusRevoked
	d.CredentialHash, d.EnrollmentTokenHash, d.EnrollmentTokenExpiresAt = nil, nil, nil
	if err := s.Store.SaveDeviceAuth(ctx, d); err != nil {
		return nil, err
	}
	slog.Info("device_revoked", "device_id", id)
	return d, nil
}

// CreateCommand returns (command, created). created=false means an existing
// command with the same idempotency key was returned.
func (s *Service) CreateCommand(ctx context.Context, deviceID, action string, params map[string]any, idemKey *string) (*model.Command, bool, error) {
	if _, err := s.Store.GetDevice(ctx, deviceID); errors.Is(err, store.ErrNotFound) {
		return nil, false, fmt.Errorf("device %s not found: %w", deviceID, ErrDeviceNotFound)
	} else if err != nil {
		return nil, false, err
	}
	if idemKey != nil && *idemKey == "" {
		idemKey = nil
	}
	if params == nil {
		params = map[string]any{}
	}
	c := &model.Command{
		CommandID: uuid.NewString(), DeviceID: deviceID, Action: action, Params: params,
		Status: model.CmdPending, IdempotencyKey: idemKey,
	}
	cmd, created, err := s.Store.CreateCommand(ctx, c)
	if err == nil && created {
		slog.Info("command_created", "device_id", deviceID, "command_id", cmd.CommandID)
	}
	return cmd, created, err
}

func (s *Service) GetCommand(ctx context.Context, id string) (*model.Command, error) {
	return s.Store.GetCommand(ctx, id)
}

func (s *Service) ListCommands(ctx context.Context, deviceID string, limit int) ([]model.Command, error) {
	return s.Store.ListCommands(ctx, deviceID, limit)
}

func (s *Service) MarkDelivered(ctx context.Context, id string) error {
	return s.Store.MarkDelivered(ctx, id, time.Now().UTC())
}

// ApplyAck is idempotent: a duplicate ack for a terminal command is a no-op.
func (s *Service) ApplyAck(ctx context.Context, id, status string, result map[string]any) (bool, error) {
	applied, err := s.Store.ApplyAck(ctx, id, status, result, time.Now().UTC())
	if err != nil {
		return false, err
	}
	if !applied {
		slog.Info("ack_ignored", "command_id", id)
	}
	return applied, nil
}

func (s *Service) RecordMessage(ctx context.Context, deviceID, kind string, payload map[string]any) error {
	return s.Store.AddMessage(ctx, &model.DeviceMessage{DeviceID: deviceID, Kind: kind, Payload: payload}, MessageRetention)
}

func (s *Service) ListMessages(ctx context.Context, deviceID string, limit int, afterID int64) ([]model.DeviceMessage, error) {
	return s.Store.ListMessages(ctx, deviceID, limit, afterID)
}
