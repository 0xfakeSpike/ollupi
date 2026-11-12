-- The catalog as gorm made it before migrations. A catalog from then has these already, and takes this step as it is.
CREATE TABLE IF NOT EXISTS `shards` (`id` text,`address` text NOT NULL,`accepting` numeric NOT NULL,PRIMARY KEY (`id`));
CREATE TABLE IF NOT EXISTS `placements` (`user_id` text,`shard_id` text NOT NULL,PRIMARY KEY (`user_id`));
CREATE INDEX IF NOT EXISTS `idx_placements_shard_id` ON `placements`(`shard_id`);
