-- A thread is unread while one of its turns ended after the user last read it: replied_at is when a turn last
-- ended, read_at when the user last read it (thread/markRead). What ended before this step counts as read, so a
-- database does not wake up with every thread unread.
-- The rebuild of threads in 0005 leaves a database from before migrations without these columns, so it comes to
-- them here like any other.
ALTER TABLE `threads` ADD COLUMN `replied_at` datetime;
ALTER TABLE `threads` ADD COLUMN `read_at` datetime;
UPDATE `threads` SET `replied_at` = (
  SELECT max(`completed_at`) FROM `turns` WHERE `turns`.`thread_id` = `threads`.`id` AND `completed_at` IS NOT NULL
);
UPDATE `threads` SET `read_at` = `replied_at`;
