DROP INDEX threads_user_list;
CREATE INDEX threads_user_list ON threads(user_id, updated_at DESC, created_at DESC, id DESC) WHERE parent_id IS NULL;
