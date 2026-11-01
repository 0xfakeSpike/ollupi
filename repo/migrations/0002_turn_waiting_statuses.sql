-- A turn out of its slot is no longer inProgress: it is ready for a slot or waiting on what its calls await.
-- inProgress turns left by an older ollupi are put right on start, like any a stopped process leaves.
DROP INDEX IF EXISTS `turns_one_active`;
DROP INDEX IF EXISTS `turns_open`;
DROP INDEX IF EXISTS `turns_ready`;
CREATE UNIQUE INDEX `turns_one_active` ON `turns`(`thread_id`) WHERE NOT (status = 'queued' OR status = 'completed' OR status = 'failed' OR status = 'interrupted');
CREATE INDEX `turns_open` ON `turns`(`thread_id`) WHERE NOT (status = 'completed' OR status = 'failed' OR status = 'interrupted');
CREATE INDEX `turns_ready` ON `turns`(`started_at`) WHERE status = 'ready';
