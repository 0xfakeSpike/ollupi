-- The delegate role is gone, so the conversations it made go with it: what a
-- delegate held is deleted with it, and a todo handed to one goes back to open.
UPDATE todos SET thread_id = '', workspace_id = '',
  status = CASE WHEN status = 'handed' THEN 'open' ELSE status END
WHERE thread_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'
  OR parent_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'));

DELETE FROM entries WHERE thread_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'
  OR parent_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'));
DELETE FROM items WHERE thread_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'
  OR parent_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'));
DELETE FROM turns WHERE thread_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'
  OR parent_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'));
DELETE FROM responses WHERE thread_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'
  OR parent_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'));
DELETE FROM tasks WHERE thread_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'
  OR parent_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'));
DELETE FROM cron_jobs WHERE thread_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'
  OR parent_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent'));

DELETE FROM threads WHERE parent_id IN (SELECT id FROM threads WHERE role = 'delegate-sub-agent');
DELETE FROM threads WHERE role = 'delegate-sub-agent';
