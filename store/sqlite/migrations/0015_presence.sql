-- One row per stretch of time the user spent in the app, written by the desktop
-- client as it observes focus, idle and screen lock. A row is appended when the
-- stretch ends and is only extended, never moved, when the same id is written
-- again: the client may repeat a stretch it already sent.
--
-- 'seen' rows are stretches spent on a thread, 'in_app' stretches in the app
-- with no thread on screen, and 'elsewhere' stretches while the user was away
-- from the app: they only tell how long the app was not in use, so nothing is
-- grouped by them.
--
-- The thread and todo of a row are a snapshot taken when it is written: a
-- thread archived and reopened elsewhere still adds up to the same todo.
CREATE TABLE presence (
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  id text NOT NULL,
  thread_id text NOT NULL DEFAULT '',
  todo_id integer NOT NULL DEFAULT 0,
  workspace_id text NOT NULL DEFAULT '',
  state text NOT NULL CHECK (state IN ('seen', 'in_app', 'elsewhere')),
  started_at datetime NOT NULL,
  ended_at datetime NOT NULL,
  PRIMARY KEY (user_id, id),
  CHECK (strftime('%s', ended_at) >= strftime('%s', started_at)),
  CHECK ((state = 'seen') = (thread_id <> ''))
);

CREATE INDEX presence_thread ON presence(user_id, thread_id, started_at) WHERE thread_id <> '';
CREATE INDEX presence_todo ON presence(user_id, todo_id, started_at) WHERE todo_id <> 0;
CREATE INDEX presence_user_at ON presence(user_id, started_at);
