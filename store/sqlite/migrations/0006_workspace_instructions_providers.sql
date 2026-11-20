ALTER TABLE workspaces ADD COLUMN instructions text NOT NULL DEFAULT '';
ALTER TABLE workspaces ADD COLUMN providers text NOT NULL DEFAULT '{}' CHECK (json_valid(providers));
