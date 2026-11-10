-- Display choices only: how dates, times and numbers are written, and which
-- currency sign Money shows. Records keep no currency meaning.
ALTER TABLE households ADD COLUMN locale TEXT NOT NULL DEFAULT '' CHECK(locale IN ('', 'en-IN', 'en-GB', 'en-US'));
ALTER TABLE households ADD COLUMN currency TEXT NOT NULL DEFAULT '' CHECK(currency IN ('', 'INR', 'USD', 'GBP', 'EUR'));
