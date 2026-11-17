-- How busy each shard says it is, for placing new users on the lighter of two; a shard that never says counts as idle.
ALTER TABLE `shards` ADD COLUMN `load` integer NOT NULL DEFAULT 0;
