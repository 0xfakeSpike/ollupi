-- No call runs in the background any more: a long task is a subagent's, which reports to the turn that launched it.
-- A wait for background calls ends failed, and the turn that waited is ready again.
UPDATE `items` SET `status` = 'failed', `completed_at` = COALESCE(`completed_at`, `started_at`),
  `error` = 'background calls are no longer run: this wait ended with them', `await` = NULL
WHERE `type` = 'toolCall' AND `status` = 'inProgress' AND json_extract(`await`, '$.by') = 'background';
UPDATE `turns` SET `status` = 'ready' WHERE `status` = 'waitingOnBackground';
-- A background call's result stays in its conversation as a notice, with what it returned.
UPDATE `items` SET `type` = 'error',
  `text` = '[background call of ' || `tool` || ' ended: ' || `text` || ']'
    || CASE WHEN `error` <> '' THEN char(10) || `error` ELSE '' END
    || COALESCE(char(10) || json_extract(`result`, '$.output'), '')
WHERE `type` = 'backgroundResult';
ALTER TABLE `items` DROP COLUMN `background_id`;
DROP TABLE `background_calls`;
