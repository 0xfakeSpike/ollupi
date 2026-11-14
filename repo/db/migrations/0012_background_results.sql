-- A background call's result comes back to its thread as an item of its own, which names the call.
ALTER TABLE `items` ADD COLUMN `background_id` text NOT NULL DEFAULT "";
