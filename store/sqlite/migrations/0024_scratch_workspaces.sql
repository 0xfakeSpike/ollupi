-- Every conversation is in a workspace. Each environment has a scratch
-- workspace, its own folder: its root is empty, the folder being the
-- environment's, as its sandbox names it. A conversation that was in no
-- workspace is in the scratch workspace of its environment, and runs in the
-- environment of its workspace, which it no longer names itself.
INSERT INTO workspaces(id, user_id, environment_id, name, root, created_at, updated_at, kind)
  SELECT lower(hex(randomblob(4)) || '-' || hex(randomblob(2)) || '-4' || substr(hex(randomblob(2)), 2) || '-'
      || substr('89ab', 1 + (abs(random()) % 4), 1) || substr(hex(randomblob(2)), 2) || '-' || hex(randomblob(6))),
    e.user_id, e.id, 'Scratch', '', e.created_at, e.created_at, 'scratch'
  FROM environments e;

UPDATE threads SET workspace_id = (
  SELECT w.id FROM workspaces w WHERE w.environment_id = threads.environment_id AND w.kind = 'scratch'
) WHERE workspace_id IS NULL;

-- The memories of conversations outside any workspace are those of the
-- scratch workspace of the user's first environment, and a global memory
-- that came from them goes back there.
UPDATE memories SET scope = 'workspace', workspace_id = (
  SELECT w.id FROM workspaces w JOIN environments e ON e.id = w.environment_id
  WHERE w.user_id = memories.user_id AND w.kind = 'scratch'
  ORDER BY e.created_at, e.id LIMIT 1
) WHERE scope = 'general';
UPDATE memories SET origin_workspace_id = (
  SELECT w.id FROM workspaces w JOIN environments e ON e.id = w.environment_id
  WHERE w.user_id = memories.user_id AND w.kind = 'scratch'
  ORDER BY e.created_at, e.id LIMIT 1
) WHERE scope = 'global' AND origin_workspace_id = '';

CREATE TABLE memories_next (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  scope text NOT NULL CHECK (scope IN ('global', 'workspace')),
  workspace_id text NOT NULL DEFAULT '',
  origin_workspace_id text NOT NULL DEFAULT '',
  topic text NOT NULL,
  kind text NOT NULL,
  text text NOT NULL,
  status text NOT NULL CHECK (status IN ('active', 'retired', 'forgotten')),
  user_edited integer NOT NULL DEFAULT 0,
  sources text NOT NULL CHECK (json_valid(sources)),
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  CHECK ((scope = 'workspace') = (workspace_id <> '')),
  CHECK ((scope = 'global') = (origin_workspace_id <> ''))
);
INSERT INTO memories_next SELECT id, user_id, scope, workspace_id, origin_workspace_id, topic, kind, text, status, user_edited, sources, created_at, updated_at FROM memories;
DROP TABLE memories;
ALTER TABLE memories_next RENAME TO memories;
CREATE UNIQUE INDEX memories_topic ON memories(user_id, scope, workspace_id, topic);

CREATE TABLE threads_next (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  title text NOT NULL DEFAULT '',
  settings text NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),
  workspace_id text NOT NULL REFERENCES workspaces(id) ON DELETE RESTRICT,
  parent_id text REFERENCES threads(id) ON DELETE CASCADE,
  parent_item text NOT NULL DEFAULT '',
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  archived_at datetime,
  replied_at datetime,
  read_at datetime,
  head integer NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'idle',
  runnable_since datetime,
  own_status text NOT NULL DEFAULT 'idle',
  role text NOT NULL DEFAULT ''
);
INSERT INTO threads_next(id, user_id, title, settings, workspace_id, parent_id, parent_item, created_at, updated_at, archived_at, replied_at, read_at, head, status, runnable_since, own_status, role)
  SELECT id, user_id, title, settings, workspace_id, parent_id, parent_item, created_at, updated_at, archived_at, replied_at, read_at, head, status, runnable_since, own_status, role FROM threads;
DROP TABLE threads;
ALTER TABLE threads_next RENAME TO threads;
CREATE INDEX threads_workspace ON threads(workspace_id);
CREATE INDEX threads_parent ON threads(parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX threads_runnable ON threads(runnable_since, id) WHERE runnable_since IS NOT NULL;
CREATE INDEX threads_user_list ON threads(user_id, updated_at DESC, created_at DESC, id DESC) WHERE parent_id IS NULL;
