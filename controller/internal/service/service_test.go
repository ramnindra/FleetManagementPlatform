package service

import (
	"errors"
	"testing"
	"time"

	"github.com/ramnindra/fleetmanagementplatform/controller/internal/model"
	"github.com/ramnindra/fleetmanagementplatform/controller/internal/store"
)

func newSvc() (*Service, *store.Memory) {
	m := store.NewMemory()
	return New(m), m
}

func TestEnrollmentTokenExpiry(t *testing.T) {
	s, _ := newSvc()
	_, tok, err := s.RegisterDevice(t.Context(), "d", "edge", nil, -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.EnrollDevice(t.Context(), "d", tok)
	var ee *EnrollmentError
	if !errors.As(err, &ee) || ee.Msg != "enrollment token expired" {
		t.Fatalf("want expired error, got %v", err)
	}
}

func TestEnrollRejectsWrongToken(t *testing.T) {
	s, _ := newSvc()
	s.RegisterDevice(t.Context(), "d", "edge", nil, time.Hour)
	if _, _, err := s.EnrollDevice(t.Context(), "d", "wrong"); err == nil {
		t.Fatal("expected error")
	}
}

func TestRegisterConflict(t *testing.T) {
	s, _ := newSvc()
	s.RegisterDevice(t.Context(), "d", "edge", nil, time.Hour)
	if _, _, err := s.RegisterDevice(t.Context(), "d", "edge", nil, time.Hour); !errors.Is(err, ErrDeviceConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestAuthenticateDevice(t *testing.T) {
	s, _ := newSvc()
	_, tok, _ := s.RegisterDevice(t.Context(), "d", "edge", nil, time.Hour)
	if s.AuthenticateDevice(t.Context(), "d", "x") {
		t.Fatal("pending device must not authenticate")
	}
	_, cred, _ := s.EnrollDevice(t.Context(), "d", tok)
	if !s.AuthenticateDevice(t.Context(), "d", cred) {
		t.Fatal("active device should authenticate")
	}
	s.RevokeDevice(t.Context(), "d")
	if s.AuthenticateDevice(t.Context(), "d", cred) {
		t.Fatal("revoked device must not authenticate")
	}
}

func TestAckIsIdempotent(t *testing.T) {
	s, _ := newSvc()
	s.RegisterDevice(t.Context(), "d", "edge", nil, time.Hour)
	c, _, _ := s.CreateCommand(t.Context(), "d", "ping", nil, nil)

	if ok, _ := s.ApplyAck(t.Context(), c.CommandID, "success", map[string]any{"a": 1}); !ok {
		t.Fatal("first ack should apply")
	}
	if ok, _ := s.ApplyAck(t.Context(), c.CommandID, "failed", nil); ok {
		t.Fatal("duplicate ack must be ignored")
	}
	got, _ := s.GetCommand(t.Context(), c.CommandID)
	if got.Status != model.CmdSuccess {
		t.Fatalf("status = %s", got.Status)
	}
	if ok, _ := s.ApplyAck(t.Context(), "unknown", "success", nil); ok {
		t.Fatal("unknown command ack must not apply")
	}
}

func TestMarkDeliveredDoesNotRegressTerminal(t *testing.T) {
	s, _ := newSvc()
	s.RegisterDevice(t.Context(), "d", "edge", nil, time.Hour)
	c, _, _ := s.CreateCommand(t.Context(), "d", "ping", nil, nil)
	s.ApplyAck(t.Context(), c.CommandID, "success", nil)
	s.MarkDelivered(t.Context(), c.CommandID)
	got, _ := s.GetCommand(t.Context(), c.CommandID)
	if got.Status != model.CmdSuccess {
		t.Fatalf("terminal status regressed to %s", got.Status)
	}
}

func TestIsOnline(t *testing.T) {
	d := &model.Device{}
	if on, age := IsOnline(d, 90*time.Second); on || age != nil {
		t.Fatal("never-seen device is offline")
	}
	now := time.Now()
	d.LastSeenAt = &now
	if on, _ := IsOnline(d, 90*time.Second); !on {
		t.Fatal("fresh heartbeat is online")
	}
	old := now.Add(-time.Hour)
	d.LastSeenAt = &old
	if on, _ := IsOnline(d, 90*time.Second); on {
		t.Fatal("stale heartbeat is offline")
	}
}

func TestMessageRetention(t *testing.T) {
	s, m := newSvc()
	m.AddMessage(t.Context(), &model.DeviceMessage{DeviceID: "d", Kind: "heartbeat"}, MessageRetention)
	// A zero-length window prunes everything older than "now".
	time.Sleep(2 * time.Millisecond)
	m.AddMessage(t.Context(), &model.DeviceMessage{DeviceID: "d", Kind: "heartbeat"}, time.Millisecond)
	msgs, _ := s.ListMessages(t.Context(), "d", 50, 0)
	if len(msgs) != 1 {
		t.Fatalf("want 1 retained message, got %d", len(msgs))
	}
}
