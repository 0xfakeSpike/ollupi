-- A turn's messages are its items: a message sent while the conversation is busy joins the user's turn waiting behind
-- the current one, so a turn may have several, and the input a turn kept was its first message only.
ALTER TABLE `turns` DROP COLUMN `attachments`;
ALTER TABLE `turns` DROP COLUMN `input`;
