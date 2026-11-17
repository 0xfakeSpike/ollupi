ALTER TABLE `shards` ADD COLUMN `owner` text NOT NULL DEFAULT "";
ALTER TABLE `shards` ADD COLUMN `epoch` integer NOT NULL DEFAULT 0;
ALTER TABLE `shards` ADD COLUMN `expires_at` integer NOT NULL DEFAULT 0;
