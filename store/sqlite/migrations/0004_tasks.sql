CREATE TABLE tasks (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  thread_id text NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  turn_id text NOT NULL,
  item_id text NOT NULL,
  call text NOT NULL CHECK (json_valid(call)),
  created_at datetime NOT NULL,
  started_at datetime
);
CREATE INDEX tasks_queue ON tasks(created_at, id);
CREATE INDEX tasks_thread ON tasks(thread_id);
