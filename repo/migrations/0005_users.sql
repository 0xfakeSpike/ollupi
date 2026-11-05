-- A database is one shard, and the users on it share its tables. What belongs to a user hangs off workspaces, threads
-- and memories, which name their user; turns, items and scheduled tasks reach theirs through the thread. Settings and
-- the memory cursor become one row per user. What a database held before this step belongs to the default user.
-- A row with no user keeps the empty default, which no user has, so its foreign key refuses it.
CREATE TABLE IF NOT EXISTS `users` (
  `id` text,
  `created_at` datetime NOT NULL,
  PRIMARY KEY (`id`)
);
INSERT OR IGNORE INTO `users`(`id`, `created_at`) VALUES ('default', strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'));

CREATE TABLE `workspaces_new` (
  `id` text,
  `user_id` text NOT NULL DEFAULT "",
  `name` text NOT NULL,
  `root_path` text NOT NULL,
  `settings` text NOT NULL DEFAULT '{}',
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL,
  `archived_at` datetime,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_workspaces_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT,
  CONSTRAINT `workspaces_name` CHECK (length(trim(name)) > 0),
  CONSTRAINT `workspaces_settings` CHECK (json_valid(settings))
);
INSERT INTO `workspaces_new`(`id`, `user_id`, `name`, `root_path`, `settings`, `created_at`, `updated_at`, `archived_at`)
  SELECT `id`, 'default', `name`, `root_path`, `settings`, `created_at`, `updated_at`, `archived_at` FROM `workspaces`;
DROP TABLE `workspaces`;
ALTER TABLE `workspaces_new` RENAME TO `workspaces`;
CREATE INDEX `workspaces_archived_updated` ON `workspaces`(`archived_at`,`updated_at` desc);
CREATE UNIQUE INDEX `workspaces_user_root` ON `workspaces`(`user_id`,`root_path`);

CREATE TABLE `threads_new` (
  `id` text,
  `user_id` text NOT NULL DEFAULT "",
  `workspace_id` text,
  `title` text NOT NULL,
  `settings` text NOT NULL DEFAULT '{}',
  `parent_thread_id` text,
  `parent_item_id` text,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL,
  `archived_at` datetime,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_threads_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT,
  CONSTRAINT `fk_threads_workspace` FOREIGN KEY (`workspace_id`) REFERENCES `workspaces`(`id`) ON DELETE RESTRICT,
  CONSTRAINT `fk_threads_parent` FOREIGN KEY (`parent_thread_id`) REFERENCES `threads`(`id`) ON DELETE CASCADE,
  CONSTRAINT `threads_settings` CHECK (json_valid(settings))
);
INSERT INTO `threads_new`(`id`, `user_id`, `workspace_id`, `title`, `settings`, `parent_thread_id`, `parent_item_id`, `created_at`, `updated_at`, `archived_at`)
  SELECT `id`, 'default', `workspace_id`, `title`, `settings`, `parent_thread_id`, `parent_item_id`, `created_at`, `updated_at`, `archived_at` FROM `threads`;
DROP TABLE `threads`;
ALTER TABLE `threads_new` RENAME TO `threads`;
CREATE INDEX `threads_parent` ON `threads`(`parent_thread_id`) WHERE parent_thread_id IS NOT NULL;
CREATE INDEX `threads_workspace` ON `threads`(`workspace_id`);
CREATE INDEX `threads_workspace_list` ON `threads`(`workspace_id`,`updated_at`,`created_at`,`id`) WHERE parent_thread_id IS NULL;
CREATE INDEX `threads_list` ON `threads`(`user_id`,`updated_at`,`created_at`,`id`) WHERE parent_thread_id IS NULL;

CREATE TABLE `memories_new` (
  `id` text,
  `user_id` text NOT NULL DEFAULT "",
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
  CONSTRAINT `fk_memories_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT,
  CONSTRAINT `memories_scope_workspace` CHECK ((scope = 'workspace') = (workspace_id <> '')),
  CONSTRAINT `memories_origin` CHECK (scope = 'global' OR origin_workspace_id = ''),
  CONSTRAINT `memories_sources` CHECK (json_valid(sources))
);
INSERT INTO `memories_new`(`id`, `user_id`, `scope`, `workspace_id`, `origin_workspace_id`, `topic`, `kind`, `text`, `status`, `user_edited`, `sources`, `created_at`, `updated_at`)
  SELECT `id`, 'default', `scope`, `workspace_id`, `origin_workspace_id`, `topic`, `kind`, `text`, `status`, `user_edited`, `sources`, `created_at`, `updated_at` FROM `memories`;
DROP TABLE `memories`;
ALTER TABLE `memories_new` RENAME TO `memories`;
CREATE INDEX `memories_scope_status` ON `memories`(`user_id`,`scope`,`workspace_id`,`status`);
CREATE UNIQUE INDEX `memories_scope_topic` ON `memories`(`user_id`,`scope`,`workspace_id`,`topic`);

CREATE TABLE `memory_state_new` (
  `user_id` text,
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
  PRIMARY KEY (`user_id`),
  CONSTRAINT `fk_memory_state_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT,
  CONSTRAINT `memory_state_failure` CHECK (last_failure IS NULL OR json_valid(last_failure)),
  CONSTRAINT `memory_state_skip` CHECK (last_skip IS NULL OR json_valid(last_skip)),
  CONSTRAINT `memory_state_report` CHECK (last_report IS NULL OR json_valid(last_report))
);
INSERT INTO `memory_state_new`(`user_id`, `cursor_at`, `cursor_id`, `cursor_seq`, `attempts`, `next_attempt`, `skipped`, `last_success`, `last_failure`, `last_skip`, `last_report`)
  SELECT 'default', `cursor_at`, `cursor_id`, `cursor_seq`, `attempts`, `next_attempt`, `skipped`, `last_success`, `last_failure`, `last_skip`, `last_report` FROM `memory_state`;
DROP TABLE `memory_state`;
ALTER TABLE `memory_state_new` RENAME TO `memory_state`;

CREATE TABLE `settings_new` (
  `user_id` text,
  `redaction` numeric NOT NULL DEFAULT true,
  `redaction_key` text NOT NULL,
  PRIMARY KEY (`user_id`),
  CONSTRAINT `fk_settings_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT
);
INSERT INTO `settings_new`(`user_id`, `redaction`, `redaction_key`)
  SELECT 'default', `redaction`, `redaction_key` FROM `settings`;
DROP TABLE `settings`;
ALTER TABLE `settings_new` RENAME TO `settings`;
