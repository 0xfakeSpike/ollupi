ALTER TABLE threads ADD COLUMN role text NOT NULL DEFAULT '';
UPDATE threads SET role = coalesce((SELECT json_extract(t.body, '$.Role') FROM turns t WHERE t.thread_id = threads.id ORDER BY t.ord LIMIT 1), '')
  WHERE parent_id IS NOT NULL;
DROP INDEX threads_user_list;
CREATE INDEX threads_user_list ON threads(user_id, updated_at DESC, created_at DESC, id DESC) WHERE parent_id IS NULL OR role = 'delegate-sub-agent';
