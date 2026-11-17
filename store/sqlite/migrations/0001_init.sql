-- A shard's store: users, the headers of their conversations, each conversation's log, and the views folded from it.
CREATE TABLE users (
  id text PRIMARY KEY,
  created_at datetime NOT NULL
);

CREATE TABLE threads (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  title text NOT NULL DEFAULT '',
  settings text NOT NULL DEFAULT '{}' CHECK (json_valid(settings)),
  workspace_id text NOT NULL DEFAULT '',
  environment_id text NOT NULL DEFAULT '',
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
CREATE INDEX threads_user_list ON threads(user_id, updated_at DESC, id) WHERE parent_id IS NULL;
CREATE INDEX threads_parent ON threads(parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX threads_runnable ON threads(runnable_since, id) WHERE runnable_since IS NOT NULL;

-- id numbers every entry of the shard in the order committed: the feed's seq.
CREATE TABLE entries (
  id integer PRIMARY KEY AUTOINCREMENT,
  thread_id text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  seq integer NOT NULL,
  turn_id text NOT NULL,
  item_id text NOT NULL DEFAULT '',
  at datetime NOT NULL,
  kind text NOT NULL,
  body text NOT NULL CHECK (json_valid(body)),
  UNIQUE (thread_id, seq)
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

CREATE TABLE items (
  thread_id text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  id text NOT NULL,
  turn_id text NOT NULL,
  seq integer NOT NULL,
  updated integer NOT NULL,
  kind text NOT NULL,
  status text NOT NULL,
  body text NOT NULL CHECK (json_valid(body)),
  PRIMARY KEY (thread_id, id)
);
CREATE INDEX items_thread_seq ON items(thread_id, seq);

CREATE TABLE responses (
  thread_id text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  id text NOT NULL,
  seq integer NOT NULL,
  body text NOT NULL CHECK (json_valid(body)),
  PRIMARY KEY (thread_id, id)
);
CREATE INDEX responses_thread_seq ON responses(thread_id, seq);

-- One row: the epoch of the shard's latest holder, which only grows.
CREATE TABLE shard_epoch (
  id integer PRIMARY KEY CHECK (id = 1),
  epoch integer NOT NULL
);
