-- A conversation runs somewhere: on this machine, whose folders its workspace names, or on a cloud sandbox of the
-- user's that the host reaches. Environments are the user's to name, and a conversation chooses one at its start.
-- A database from before migrations already has these tables as gorm made them, so this step adds to them rather
-- than making them again.
CREATE TABLE IF NOT EXISTS `environments` (
  `id` text,
  `user_id` text NOT NULL DEFAULT "",
  `name` text NOT NULL,
  `kind` text NOT NULL,
  `created_at` datetime NOT NULL,
  `updated_at` datetime NOT NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `fk_environments_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT,
  CONSTRAINT `environments_name` CHECK (length(trim(name)) > 0),
  CONSTRAINT `environments_kind` CHECK (kind in ('local','machine'))
);
CREATE UNIQUE INDEX IF NOT EXISTS `environments_user_name` ON `environments`(`user_id`,`name`);

-- Every user has the environment on this machine: their workspaces are folders in it.
INSERT OR IGNORE INTO `environments`(`id`, `user_id`, `name`, `kind`, `created_at`, `updated_at`)
  SELECT lower(hex(randomblob(16))), `id`, 'This computer', 'local',
    strftime('%Y-%m-%d %H:%M:%f+00:00', 'now'), strftime('%Y-%m-%d %H:%M:%f+00:00', 'now')
  FROM `users`;

-- A conversation from before environments ran on this machine, as it does now.
-- A step from before environments rebuilt the table without the column, so a database from before migrations has
-- it here first and comes to it the same way.
ALTER TABLE `threads` ADD COLUMN `environment_id` text REFERENCES `environments`(`id`) ON DELETE RESTRICT;
UPDATE `threads` SET `environment_id` = (
  SELECT `id` FROM `environments` WHERE `environments`.`user_id` = `threads`.`user_id` AND `kind` = 'local' LIMIT 1
);
CREATE INDEX `threads_environment` ON `threads`(`environment_id`);
