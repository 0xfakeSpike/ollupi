-- A shard's store: its users and what each owns and sets, the headers of their conversations, each conversation's log
-- and the views folded from it.
CREATE TABLE users (
  id text PRIMARY KEY,
  created_at datetime NOT NULL
);

CREATE TABLE settings (
  user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  redaction integer NOT NULL,
  redaction_key text NOT NULL,
  memory integer NOT NULL,
  permissions text NOT NULL DEFAULT '{}' CHECK (json_valid(permissions))
);

CREATE TABLE workspaces (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  root text NOT NULL,
  settings text NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  archived_at datetime,
  UNIQUE (user_id, root)
);

CREATE TABLE environments (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  kind text NOT NULL,
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  UNIQUE (user_id, name COLLATE NOCASE)
);

CREATE TABLE mcp_servers (
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  command text NOT NULL DEFAULT '',
  args text NOT NULL DEFAULT '[]' CHECK (json_valid(args)),
  env text NOT NULL DEFAULT '{}' CHECK (json_valid(env)),
  url text NOT NULL DEFAULT '',
  headers text NOT NULL DEFAULT '{}' CHECK (json_valid(headers)),
  PRIMARY KEY (user_id, name)
);

CREATE TABLE threads (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  title text NOT NULL DEFAULT '',
  settings text NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),
  workspace_id text REFERENCES workspaces(id) ON DELETE RESTRICT,
  environment_id text REFERENCES environments(id) ON DELETE RESTRICT,
  parent_id text REFERENCES threads(id) ON DELETE CASCADE,
  parent_item text NOT NULL DEFAULT '',
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  archived_at datetime,
  replied_at datetime,
  read_at datetime,
  -- Folded from the log, beside the header, so that the scheduler and lists read them without folding.
  head integer NOT NULL DEFAULT 0,
  status text NOT NULL DEFAULT 'idle',
  runnable_since datetime
);
CREATE INDEX threads_user_list ON threads(user_id, updated_at DESC, created_at DESC, id DESC) WHERE parent_id IS NULL;
CREATE INDEX threads_workspace ON threads(workspace_id);
CREATE INDEX threads_environment ON threads(environment_id);
CREATE INDEX threads_parent ON threads(parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX threads_runnable ON threads(runnable_since, id) WHERE runnable_since IS NOT NULL;

CREATE TABLE cron_jobs (
  id text PRIMARY KEY,
  thread_id text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  name text NOT NULL,
  spec text NOT NULL,
  prompt text NOT NULL,
  times integer,
  runs integer NOT NULL DEFAULT 0,
  paused integer NOT NULL DEFAULT 0,
  next_run_at datetime NOT NULL,
  created_at datetime NOT NULL,
  UNIQUE (thread_id, name)
);
CREATE INDEX cron_jobs_due ON cron_jobs(next_run_at) WHERE paused = 0;

CREATE TABLE entries (
  thread_id text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  seq integer NOT NULL,
  turn_id text NOT NULL,
  item_id text NOT NULL DEFAULT '',
  at datetime NOT NULL,
  kind text NOT NULL,
  body text NOT NULL CHECK (json_valid(body)),
  PRIMARY KEY (thread_id, seq)
);

CREATE TABLE turns (
  thread_id text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  id text NOT NULL,
  ord integer NOT NULL,
  ended integer NOT NULL DEFAULT 0,
  body text NOT NULL CHECK (json_valid(body)),
  PRIMARY KEY (thread_id, id)
);
CREATE INDEX turns_open ON turns(thread_id, ord) WHERE ended = 0;

-- text is a message's, for search; the rest of an item is its body.
CREATE TABLE items (
  thread_id text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  id text NOT NULL,
  turn_id text NOT NULL,
  seq integer NOT NULL,
  updated integer NOT NULL,
  kind text NOT NULL,
  status text NOT NULL,
  at datetime NOT NULL,
  text text NOT NULL DEFAULT '',
  body text NOT NULL CHECK (json_valid(body)),
  PRIMARY KEY (thread_id, id)
);
CREATE INDEX items_thread_seq ON items(thread_id, seq);

-- Messages never change once written, so the index takes them as they are written and drops them as they go.
CREATE VIRTUAL TABLE items_search USING fts5(text, content='items', content_rowid='rowid', tokenize='trigram');
CREATE TRIGGER items_search_insert AFTER INSERT ON items WHEN new.text <> ''
BEGIN
  INSERT INTO items_search(rowid, text) VALUES (new.rowid, new.text);
END;
CREATE TRIGGER items_search_delete AFTER DELETE ON items WHEN old.text <> ''
BEGIN
  INSERT INTO items_search(items_search, rowid, text) VALUES ('delete', old.rowid, old.text);
END;

CREATE TABLE responses (
  thread_id text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  id text NOT NULL,
  seq integer NOT NULL,
  body text NOT NULL CHECK (json_valid(body)),
  PRIMARY KEY (thread_id, id)
);
CREATE INDEX responses_thread_seq ON responses(thread_id, seq);

-- One row: the latest commit, as the feed numbers them.
CREATE TABLE feed (
  id integer PRIMARY KEY CHECK (id = 1),
  seq integer NOT NULL
);
INSERT INTO feed(id, seq) VALUES (1, 0);

-- One row: the epoch of the shard's latest holder, which only grows.
CREATE TABLE shard_epoch (
  id integer PRIMARY KEY CHECK (id = 1),
  epoch integer NOT NULL
);
