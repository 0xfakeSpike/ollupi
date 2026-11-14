-- A background call waits as queued until the orchestrator starts it, so the calls not yet ended are the queued ones
-- and the running ones.
DROP INDEX IF EXISTS `background_calls_running`;
CREATE INDEX IF NOT EXISTS `background_calls_unended` ON `background_calls`(`status`) WHERE status = 'queued' OR status = 'running';
