-- A turn submitted behind another is written ready, with its message: a thread's current turn is its first that has
-- not ended, and no turn waits queued. The items read in the order of their turns, and within a turn as they were
-- written, so a message written behind a turn in progress follows all of that turn.
ALTER TABLE `items` ADD COLUMN `turn_seq` integer NOT NULL DEFAULT 0;
UPDATE `items` SET `turn_seq` = (SELECT `seq` FROM `turns` WHERE `turns`.`id` = `items`.`turn_id`);
-- The queued turns get the message they would have written once they started, after the items of their thread.
INSERT INTO `items` (`id`, `thread_id`, `turn_id`, `turn_seq`, `seq`, `type`, `status`, `text`, `attachments`,
  `started_at`, `completed_at`)
SELECT lower(hex(randomblob(16))), q.`thread_id`, q.`id`, q.`seq`,
  (SELECT COALESCE(MAX(i.`seq`), 0) FROM `items` i WHERE i.`thread_id` = q.`thread_id`)
    + (SELECT count(*) FROM `turns` e WHERE e.`thread_id` = q.`thread_id` AND e.`status` = 'queued' AND e.`seq` <= q.`seq`),
  'userMessage', 'completed', q.`input`, q.`attachments`, q.`created_at`, q.`created_at`
FROM `turns` q WHERE q.`status` = 'queued';
DROP INDEX `turns_one_active`;
UPDATE `turns` SET `status` = 'ready' WHERE `status` = 'queued';
CREATE UNIQUE INDEX `turns_one_running` ON `turns`(`thread_id`) WHERE status = 'inProgress';
DROP INDEX `items_carried`;
DROP INDEX `items_compactions`;
CREATE INDEX `items_thread_order` ON `items`(`thread_id`,`turn_seq`,`seq`);
CREATE INDEX `items_carried` ON `items`(`thread_id`,`turn_seq`,`seq`) WHERE type = 'userMessage' OR (type = 'toolCall' AND (approval ->> '$.decision' = 'acceptForSession' OR (status = 'completed' AND (tool = 'load_tools' OR result -> '$.artifacts' IS NOT NULL))));
CREATE INDEX `items_compactions` ON `items`(`thread_id`,`turn_seq`,`seq`) WHERE type = 'contextCompaction' AND status = 'completed';
