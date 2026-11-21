-- One row per model response (an answer or a context compaction), derived from
-- the modelResponded entries by the background recorder. Rows are never deleted
-- yet: a retention period and its cleanup are left for a later change, and
-- deleting a thread leaves its usage rows in place (still counted).
CREATE TABLE usage (
  id text PRIMARY KEY,
  thread_id text NOT NULL DEFAULT '',
  seq integer NOT NULL DEFAULT 0,
  user_id text NOT NULL,
  workspace_id text NOT NULL DEFAULT '',
  turn_id text NOT NULL DEFAULT '',
  item_id text NOT NULL DEFAULT '',
  at datetime NOT NULL,
  source text NOT NULL,
  model text NOT NULL DEFAULT '',
  input_tokens integer NOT NULL,
  cached_input_tokens integer NOT NULL,
  cache_write_input_tokens integer NOT NULL,
  output_tokens integer NOT NULL
);
CREATE INDEX usage_at ON usage(at);
CREATE INDEX usage_user_at ON usage(user_id, at);

CREATE INDEX entries_model_responded ON entries(thread_id, seq) WHERE kind = 'modelResponded';
