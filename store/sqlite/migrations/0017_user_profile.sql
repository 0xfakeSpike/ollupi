-- A profile's own name and colour, beside the id every row of its data is
-- kept under. Both are display only: permissions and the data itself are
-- unchanged by them.
ALTER TABLE users ADD COLUMN name text NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN color text NOT NULL DEFAULT '';
