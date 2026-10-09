// Package model holds the controller's domain types.
package model

import "time"

const (
	StatusPendingEnrollment = "pending_enrollment"
	StatusActive            = "active"
	StatusRevoked           = "revoked"

	CmdPending   = "pending"
	CmdDelivered = "delivered"
	CmdSuccess   = "success"
	CmdFailed    = "failed"
	CmdExpired   = "expired"
)

var (
	DeviceTypes    = map[string]bool{"switch": true, "gpu": true, "edge": true}
	AllowedActions = map[string]bool{"get_status": true, "get_system_info": true, "ping": true, "update_config": true}
)

func IsTerminal(status string) bool {
	return status == CmdSuccess || status == CmdFailed || status == CmdExpired
}

type Device struct {
	DeviceID                 string         `json:"device_id"`
	DeviceType               string         `json:"device_type"`
	Status                   string         `json:"status"`
	LastSeenAt               *time.Time     `json:"last_seen_at"`
	Attributes               map[string]any `json:"attributes"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"-"`
	EnrollmentTokenHash      *string        `json:"-"`
	EnrollmentTokenExpiresAt *time.Time     `json:"-"`
	CredentialHash           *string        `json:"-"`
}

type Command struct {
	CommandID      string         `json:"command_id"`
	DeviceID       string         `json:"device_id"`
	Action         string         `json:"action"`
	Params         map[string]any `json:"params"`
	Status         string         `json:"status"`
	Result         map[string]any `json:"result"`
	IdempotencyKey *string        `json:"-"`
	CreatedAt      time.Time      `json:"created_at"`
	DeliveredAt    *time.Time     `json:"delivered_at"`
	// ReceivedAt is when the device reports it received the command (device clock).
	ReceivedAt  *time.Time `json:"received_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

// DeviceMessage is a heartbeat/telemetry/ack kept briefly so the web UI can
// show recent device activity. Not a durable telemetry store.
type DeviceMessage struct {
	ID         int64          `json:"id"`
	DeviceID   string         `json:"device_id"`
	Kind       string         `json:"kind"`
	Payload    map[string]any `json:"payload"`
	ReceivedAt time.Time      `json:"received_at"`
}
