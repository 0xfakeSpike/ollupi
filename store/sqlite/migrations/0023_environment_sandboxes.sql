-- An environment is a sandbox of one of the deployment's providers: provider
-- names it, config is what the user chose for it, and state is what the
-- provider keeps of the sandbox to find it again (a cloud sandbox's id).
-- Every environment of before was this machine's (kind 'local') or a cloud
-- machine named and never made ('machine'): the first are the local
-- provider's, and the conversations of the second move to their user's
-- local one, which they go without.
UPDATE threads SET environment_id = (
  SELECT e.id FROM environments e WHERE e.user_id = threads.user_id AND e.kind = 'local'
) WHERE environment_id IN (SELECT id FROM environments WHERE kind <> 'local');
DELETE FROM environments WHERE kind <> 'local';

CREATE TABLE environments_next (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  provider text NOT NULL,
  config text NOT NULL DEFAULT '{}' CHECK (json_valid(config)),
  state text NOT NULL DEFAULT '{}' CHECK (json_valid(state)),
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  UNIQUE (user_id, name COLLATE NOCASE)
);
INSERT INTO environments_next(id, user_id, name, provider, created_at, updated_at)
  SELECT id, user_id, name, 'local', created_at, updated_at FROM environments;
DROP TABLE environments;
ALTER TABLE environments_next RENAME TO environments;

-- A workspace is a folder of one environment's machine: the same path in two
-- environments is two folders. Those of before are folders of this machine,
-- their user's first environment.
CREATE TABLE workspaces_next (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  environment_id text NOT NULL REFERENCES environments(id) ON DELETE RESTRICT,
  name text NOT NULL,
  root text NOT NULL,
  settings text NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  archived_at datetime,
  instructions text NOT NULL DEFAULT '',
  providers text NOT NULL DEFAULT '{}' CHECK (json_valid(providers)),
  kind text NOT NULL DEFAULT 'project',
  moving_to text NOT NULL DEFAULT '',
  UNIQUE (user_id, environment_id, root)
);
INSERT INTO workspaces_next(id, user_id, environment_id, name, root, settings, created_at, updated_at, archived_at, instructions, providers, kind, moving_to)
  SELECT w.id, w.user_id,
    (SELECT e.id FROM environments e WHERE e.user_id = w.user_id ORDER BY e.created_at, e.id LIMIT 1),
    w.name, w.root, w.settings, w.created_at, w.updated_at, w.archived_at, w.instructions, w.providers, w.kind, w.moving_to
  FROM workspaces w;
DROP TABLE workspaces;
ALTER TABLE workspaces_next RENAME TO workspaces;
CREATE INDEX workspaces_moving ON workspaces(id) WHERE moving_to <> '';
