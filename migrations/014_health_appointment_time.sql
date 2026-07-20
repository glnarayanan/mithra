ALTER TABLE health_appointments
ADD COLUMN scheduled_at TEXT NOT NULL DEFAULT ''
CHECK (length(scheduled_at) <= 16);
