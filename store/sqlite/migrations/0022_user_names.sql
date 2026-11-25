-- What a user is called where people see them, as a client lists the users
-- of a deployment. Empty is a user nobody named yet.
ALTER TABLE users ADD COLUMN name text NOT NULL DEFAULT '';
