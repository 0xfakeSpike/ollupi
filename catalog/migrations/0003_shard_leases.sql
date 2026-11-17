-- A shard's lease is kept here, beside its address: the process that holds it is the one its address names. Each
-- taking of a shard has an epoch above every one before it, which the shard's own database keeps to refuse the writes
-- of an earlier holder.
ALTER TABLE `shards` ADD COLUMN `owner` text NOT NULL DEFAULT "";
ALTER TABLE `shards` ADD COLUMN `epoch` integer NOT NULL DEFAULT 0;
ALTER TABLE `shards` ADD COLUMN `expires_at` integer NOT NULL DEFAULT 0;
