CREATE TABLE IF NOT EXISTS device_messages (
    id BIGSERIAL PRIMARY KEY,
    device_id VARCHAR(128) NOT NULL,
    kind VARCHAR(16) NOT NULL,
    payload JSON NOT NULL DEFAULT '{}',
    received_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_device_messages_device_id_received_at ON device_messages (device_id, received_at);
