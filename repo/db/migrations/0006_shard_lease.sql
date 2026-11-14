-- One process runs a shard at a time. It holds this row while it does; another waits for the row to expire or go.
CREATE TABLE IF NOT EXISTS `shard_lease` (
  `id` integer,
  `owner` text NOT NULL,
  `expires_at` integer NOT NULL,
  PRIMARY KEY (`id`),
  CONSTRAINT `shard_lease_one` CHECK (id = 1)
);
