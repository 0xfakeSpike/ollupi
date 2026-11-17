-- The shard's lease is kept in the catalog now. The shard keeps only the epoch of its latest holder, which refuses
-- the writes of an earlier one.
DROP TABLE `shard_lease`;
CREATE TABLE `shard_epoch` (
  `id` integer,
  `epoch` integer NOT NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `shard_epoch_one` CHECK (id = 1)
);
