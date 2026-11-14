-- A tool call may run in the background: the turn that made it goes on, and its result comes back to its thread when
-- it ends. A call still running when its process stopped is ended as interrupted when the next one starts, and a
-- call records when its result came back, so that one that ended as its process stopped still comes back then.
-- A database from before migrations already has the table as gorm made it, so this step makes it only if it is not there.
CREATE TABLE IF NOT EXISTS `background_calls` (`id` text,`thread_id` text NOT NULL,`item_id` text NOT NULL,`tool` text NOT NULL,`status` text NOT NULL,`error` text NOT NULL DEFAULT "",`started_at` datetime NOT NULL,`completed_at` datetime,`delivered_at` datetime,`arguments` text NOT NULL DEFAULT "",`result` text,PRIMARY KEY (`id`),CONSTRAINT `fk_background_calls_thread` FOREIGN KEY (`thread_id`) REFERENCES `threads`(`id`) ON DELETE CASCADE);
CREATE INDEX IF NOT EXISTS `background_calls_running` ON `background_calls`(`status`) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS `background_calls_thread` ON `background_calls`(`thread_id`);
CREATE INDEX IF NOT EXISTS `background_calls_undelivered` ON `background_calls`(`thread_id`) WHERE delivered_at IS NULL AND (status = 'completed' OR status = 'failed' OR status = 'interrupted');
