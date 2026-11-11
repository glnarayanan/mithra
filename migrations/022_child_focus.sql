-- Which measure a child's page and the Home panel lead with. Empty means the
-- default: time since the last feed.
ALTER TABLE household_people ADD COLUMN focus TEXT NOT NULL DEFAULT '' CHECK(focus IN ('', 'feeds', 'weight', 'bottle', 'diapers'));
