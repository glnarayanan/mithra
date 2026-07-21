UPDATE household_openai_settings
SET key_fingerprint = lower(hex(randomblob(8)));
