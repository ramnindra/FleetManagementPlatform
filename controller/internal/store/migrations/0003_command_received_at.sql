-- When the device itself says it received the command (device clock), as
-- opposed to delivered_at, which only means the broker accepted the publish.
ALTER TABLE commands ADD COLUMN IF NOT EXISTS received_at TIMESTAMPTZ;
