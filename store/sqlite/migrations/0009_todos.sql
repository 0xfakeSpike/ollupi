CREATE TABLE todos (
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  id integer NOT NULL,
  title text NOT NULL,
  workspace_id text NOT NULL DEFAULT '',
  thread_id text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'handed', 'done', 'dropped')),
  notes text NOT NULL DEFAULT '',
  result text NOT NULL DEFAULT '',
  created_at datetime NOT NULL,
  updated_at datetime NOT NULL,
  done_at datetime,
  PRIMARY KEY (user_id, id)
);
