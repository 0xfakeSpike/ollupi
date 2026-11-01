-- The schema ollupi had when migrations began, as gorm created it.

CREATE TABLE `workspaces` (
  `id` text,
  `name` text NOT NULL,
  `root_path` text NOT NULL,
  `settings` text NOT NULL DEFAULT '{}',
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL,
  `archived_at` datetime,
  PRIMARY KEY (`id`),
  CONSTRAINT `workspaces_name` CHECK (length(trim(name)) > 0),
  CONSTRAINT `workspaces_settings` CHECK (json_valid(settings))
);
CREATE INDEX `workspaces_archived_updated` ON `workspaces`(`archived_at`,`updated_at` desc);
CREATE UNIQUE INDEX `idx_workspaces_root_path` ON `workspaces`(`root_path`);

CREATE TABLE `threads` (
  `id` text,
  `workspace_id` text,
  `title` text NOT NULL,
  `settings` text NOT NULL DEFAULT '{}',
  `parent_thread_id` text,
  `parent_item_id` text,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL,
  `archived_at` datetime,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_threads_workspace` FOREIGN KEY (`workspace_id`) REFERENCES `workspaces`(`id`) ON DELETE RESTRICT,
  CONSTRAINT `fk_threads_parent` FOREIGN KEY (`parent_thread_id`) REFERENCES `threads`(`id`) ON DELETE CASCADE,
  CONSTRAINT `threads_settings` CHECK (json_valid(settings))
);
CREATE INDEX `threads_parent` ON `threads`(`parent_thread_id`) WHERE parent_thread_id IS NOT NULL;
CREATE INDEX `threads_workspace_list` ON `threads`(`workspace_id`,`updated_at`,`created_at`,`id`) WHERE parent_thread_id IS NULL;
CREATE INDEX `threads_list` ON `threads`(`updated_at`,`created_at`,`id`) WHERE parent_thread_id IS NULL;

CREATE TABLE `turns` (
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
  `input` text NOT NULL DEFAULT "",
  `attachments` text,
  `cron_job_id` text NOT NULL DEFAULT "",
  `started_at` datetime NOT NULL,
  `completed_at` datetime,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_turns_thread` FOREIGN KEY (`thread_id`) REFERENCES `threads`(`id`) ON DELETE CASCADE,
  CONSTRAINT `turns_model` CHECK (json_valid(model))
);
CREATE INDEX `turns_cron_job` ON `turns`(`cron_job_id`,`started_at`);
CREATE INDEX `turns_open` ON `turns`(`thread_id`) WHERE status = 'inProgress' OR status = 'queued';
CREATE UNIQUE INDEX `turns_one_active` ON `turns`(`thread_id`) WHERE status = 'inProgress';
CREATE UNIQUE INDEX `turns_thread_seq` ON `turns`(`thread_id`,`seq`);
CREATE INDEX `turns_ended` ON `turns`(`completed_at`,`id`) WHERE (status = 'completed' OR status = 'failed' OR status = 'interrupted') AND completed_at IS NOT NULL;

CREATE TABLE `items` (
  `id` text,
  `thread_id` text NOT NULL,
  `turn_id` text NOT NULL,
  `seq` integer NOT NULL,
  `type` text NOT NULL,
  `status` text NOT NULL,
  `text` text NOT NULL DEFAULT "",
  `response_id` text NOT NULL DEFAULT "",
  `context_stats` text,
  `usage` text,
  `native` blob,
  `memory` text,
  `attachments` text,
  `call_id` text NOT NULL DEFAULT "",
  `tool` text NOT NULL DEFAULT "",
  `arguments` text NOT NULL DEFAULT "",
  `server` text NOT NULL DEFAULT "",
  `annotations` text,
  `approval` text,
  `prepared_execution` text,
  `await` text,
  `result` text,
  `error` text NOT NULL DEFAULT "",
  `started_at` datetime NOT NULL,
  `completed_at` datetime,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_items_thread` FOREIGN KEY (`thread_id`) REFERENCES `threads`(`id`) ON DELETE CASCADE,
  CONSTRAINT `fk_items_turn` FOREIGN KEY (`turn_id`) REFERENCES `turns`(`id`) ON DELETE CASCADE
);
CREATE INDEX `items_turn` ON `items`(`turn_id`,`type`,`status`,`seq`);
CREATE INDEX `items_carried` ON `items`(`thread_id`,`seq`) WHERE type = 'userMessage' OR (type = 'toolCall' AND (approval ->> '$.decision' = 'acceptForSession' OR (status = 'completed' AND (tool = 'load_tools' OR result -> '$.artifacts' IS NOT NULL))));
CREATE INDEX `items_compactions` ON `items`(`thread_id`,`seq`) WHERE type = 'contextCompaction' AND status = 'completed';
CREATE UNIQUE INDEX `items_thread_seq` ON `items`(`thread_id`,`seq`);

CREATE TABLE `cron_jobs` (
  `id` text,
  `thread_id` text NOT NULL,
  `name` text NOT NULL,
  `spec` text NOT NULL,
  `prompt` text NOT NULL,
  `times` integer,
  `runs` integer NOT NULL DEFAULT 0,
  `paused` numeric NOT NULL,
  `next_run_at` datetime NOT NULL,
  `created_at` datetime NOT NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_cron_jobs_thread` FOREIGN KEY (`thread_id`) REFERENCES `threads`(`id`) ON DELETE CASCADE
);
CREATE INDEX `cron_jobs_due` ON `cron_jobs`(`paused`,`next_run_at`);
CREATE UNIQUE INDEX `cron_jobs_thread_name` ON `cron_jobs`(`thread_id`,`name`);

CREATE TABLE `memories` (
  `id` text,
  `scope` text NOT NULL,
  `workspace_id` text NOT NULL DEFAULT "",
  `origin_workspace_id` text NOT NULL DEFAULT "",
  `topic` text NOT NULL,
  `kind` text NOT NULL,
  `text` text NOT NULL,
  `status` text NOT NULL,
  `user_edited` numeric NOT NULL,
  `sources` text NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `memories_scope_workspace` CHECK ((scope = 'workspace') = (workspace_id <> '')),
  CONSTRAINT `memories_origin` CHECK (scope = 'global' OR origin_workspace_id = ''),
  CONSTRAINT `memories_sources` CHECK (json_valid(sources))
);
CREATE INDEX `memories_scope_status` ON `memories`(`scope`,`workspace_id`,`status`);
CREATE UNIQUE INDEX `memories_scope_topic` ON `memories`(`scope`,`workspace_id`,`topic`);

CREATE TABLE `memory_state` (
  `id` integer,
  `cursor_at` datetime,
  `cursor_id` text NOT NULL,
  `cursor_seq` integer NOT NULL DEFAULT 0,
  `attempts` integer NOT NULL DEFAULT 0,
  `next_attempt` datetime,
  `skipped` integer NOT NULL DEFAULT 0,
  `last_success` datetime,
  `last_failure` text,
  `last_skip` text,
  `last_report` text,
  PRIMARY KEY (`id`),
  CONSTRAINT `memory_state_one` CHECK (id = 1),
  CONSTRAINT `memory_state_failure` CHECK (last_failure IS NULL OR json_valid(last_failure)),
  CONSTRAINT `memory_state_skip` CHECK (last_skip IS NULL OR json_valid(last_skip)),
  CONSTRAINT `memory_state_report` CHECK (last_report IS NULL OR json_valid(last_report))
);

CREATE TABLE `settings` (
  `id` integer,
  `redaction` numeric NOT NULL DEFAULT true,
  `redaction_key` text NOT NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `settings_one` CHECK (id = 1)
);
