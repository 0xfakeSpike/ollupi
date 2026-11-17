-- turns gains created_at, the time it was submitted, which started_at no longer stands in for once the turn starts;
-- its scheduled-task index holds only the turns a task sent. In turns and items the columns that can be large come
-- last, so reading the small ones does not walk the overflow pages of a long text, a tool result or a native record.

CREATE TABLE `turns_new` (
  `id` text,
  `thread_id` text NOT NULL,
  `seq` integer NOT NULL,
  `status` text NOT NULL,
  `model` text NOT NULL,
  `input_tokens` integer NOT NULL,
  `cached_input_tokens` integer NOT NULL,
  `cache_write_input_tokens` integer NOT NULL,
  `output_tokens` integer NOT NULL,
  `error` text NOT NULL DEFAULT "",
  `cron_job_id` text NOT NULL DEFAULT "",
  `created_at` datetime NOT NULL,
  `started_at` datetime NOT NULL,
  `completed_at` datetime,
  `attachments` text,
  `input` text NOT NULL DEFAULT "",
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_turns_thread` FOREIGN KEY (`thread_id`) REFERENCES `threads`(`id`) ON DELETE CASCADE,
  CONSTRAINT `turns_model` CHECK (json_valid(model))
);
INSERT INTO `turns_new` (`id`, `thread_id`, `seq`, `status`, `model`, `input_tokens`, `cached_input_tokens`,
  `cache_write_input_tokens`, `output_tokens`, `error`, `cron_job_id`, `created_at`, `started_at`, `completed_at`,
  `attachments`, `input`)
SELECT `id`, `thread_id`, `seq`, `status`, `model`, `input_tokens`, `cached_input_tokens`,
  `cache_write_input_tokens`, `output_tokens`, `error`, `cron_job_id`, `started_at`, `started_at`, `completed_at`,
  `attachments`, `input`
FROM `turns`;
DROP TABLE `turns`;
ALTER TABLE `turns_new` RENAME TO `turns`;
CREATE INDEX `turns_cron_job` ON `turns`(`cron_job_id`,`created_at`) WHERE cron_job_id <> '';
CREATE UNIQUE INDEX `turns_one_active` ON `turns`(`thread_id`) WHERE NOT (status = 'queued' OR status = 'completed' OR status = 'failed' OR status = 'interrupted');
CREATE INDEX `turns_open` ON `turns`(`thread_id`) WHERE NOT (status = 'completed' OR status = 'failed' OR status = 'interrupted');
CREATE INDEX `turns_ready` ON `turns`(`started_at`) WHERE status = 'ready';
CREATE UNIQUE INDEX `turns_thread_seq` ON `turns`(`thread_id`,`seq`);
CREATE INDEX `turns_ended` ON `turns`(`completed_at`,`id`) WHERE (status = 'completed' OR status = 'failed' OR status = 'interrupted') AND completed_at IS NOT NULL;

CREATE TABLE `items_new` (
  `id` text,
  `thread_id` text NOT NULL,
  `turn_id` text NOT NULL,
  `seq` integer NOT NULL,
  `type` text NOT NULL,
  `status` text NOT NULL,
  `call_id` text NOT NULL DEFAULT "",
  `tool` text NOT NULL DEFAULT "",
  `server` text NOT NULL DEFAULT "",
  `response_id` text NOT NULL DEFAULT "",
  `error` text NOT NULL DEFAULT "",
  `started_at` datetime NOT NULL,
  `completed_at` datetime,
  `approval` text,
  `await` text,
  `annotations` text,
  `usage` text,
  `context_stats` text,
  `memory` text,
  `attachments` text,
  `text` text NOT NULL DEFAULT "",
  `arguments` text NOT NULL DEFAULT "",
  `prepared_execution` text,
  `result` text,
  `native` blob,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_items_thread` FOREIGN KEY (`thread_id`) REFERENCES `threads`(`id`) ON DELETE CASCADE,
  CONSTRAINT `fk_items_turn` FOREIGN KEY (`turn_id`) REFERENCES `turns`(`id`) ON DELETE CASCADE
);
INSERT INTO `items_new` (`id`, `thread_id`, `turn_id`, `seq`, `type`, `status`, `call_id`, `tool`, `server`,
  `response_id`, `error`, `started_at`, `completed_at`, `approval`, `await`, `annotations`, `usage`, `context_stats`,
  `memory`, `attachments`, `text`, `arguments`, `prepared_execution`, `result`, `native`)
SELECT `id`, `thread_id`, `turn_id`, `seq`, `type`, `status`, `call_id`, `tool`, `server`,
  `response_id`, `error`, `started_at`, `completed_at`, `approval`, `await`, `annotations`, `usage`, `context_stats`,
  `memory`, `attachments`, `text`, `arguments`, `prepared_execution`, `result`, `native`
FROM `items`;
DROP TABLE `items`;
ALTER TABLE `items_new` RENAME TO `items`;
CREATE INDEX `items_turn` ON `items`(`turn_id`,`type`,`status`,`seq`);
CREATE INDEX `items_carried` ON `items`(`thread_id`,`seq`) WHERE type = 'userMessage' OR (type = 'toolCall' AND (approval ->> '$.decision' = 'acceptForSession' OR (status = 'completed' AND (tool = 'load_tools' OR result -> '$.artifacts' IS NOT NULL))));
CREATE INDEX `items_compactions` ON `items`(`thread_id`,`seq`) WHERE type = 'contextCompaction' AND status = 'completed';
CREATE UNIQUE INDEX `items_thread_seq` ON `items`(`thread_id`,`seq`);

DROP INDEX IF EXISTS `threads_workspace`;
CREATE INDEX `threads_workspace` ON `threads`(`workspace_id`);
