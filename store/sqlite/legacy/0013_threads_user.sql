-- The threads of one user, found for what belongs to them across all their threads: the memory to extract, the
-- background calls they may run, and their attachments.
CREATE INDEX `threads_user` ON `threads`(`user_id`);
