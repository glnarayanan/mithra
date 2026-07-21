ALTER TABLE household_openai_settings
ADD COLUMN provider_id TEXT NOT NULL DEFAULT 'openai'
CHECK (length(provider_id) BETWEEN 1 AND 32);

ALTER TABLE household_openai_settings
ADD COLUMN provider_model TEXT NOT NULL DEFAULT 'gpt-5.4-mini'
CHECK (length(provider_model) BETWEEN 1 AND 256);

ALTER TABLE household_openai_settings
ADD COLUMN provider_base_url TEXT NOT NULL DEFAULT 'https://api.openai.com/v1'
CHECK (length(provider_base_url) BETWEEN 8 AND 512);
