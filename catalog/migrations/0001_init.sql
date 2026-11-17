CREATE TABLE IF NOT EXISTS `shards` (`id` text,`address` text NOT NULL,`accepting` numeric NOT NULL,PRIMARY KEY (`id`));
CREATE TABLE IF NOT EXISTS `placements` (`user_id` text,`shard_id` text NOT NULL,PRIMARY KEY (`user_id`));
CREATE INDEX IF NOT EXISTS `idx_placements_shard_id` ON `placements`(`shard_id`);
