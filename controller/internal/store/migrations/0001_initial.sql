CREATE TABLE IF NOT EXISTS devices (
    device_id VARCHAR(128) PRIMARY KEY,
    device_type VARCHAR(32) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending_enrollment',
    enrollment_token_hash VARCHAR(256),
    enrollment_token_expires_at TIMESTAMPTZ,
    credential_hash VARCHAR(256),
    last_seen_at TIMESTAMPTZ,
    attributes JSON NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS commands (
    command_id VARCHAR(64) PRIMARY KEY,
    device_id VARCHAR(128) NOT NULL REFERENCES devices (device_id),
    action VARCHAR(64) NOT NULL,
    params JSON NOT NULL DEFAULT '{}',
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    result JSON,
    idempotency_key VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL,
    delivered_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    CONSTRAINT uq_command_device_idempotency UNIQUE (device_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS ix_commands_device_id_created_at ON commands (device_id, created_at);
