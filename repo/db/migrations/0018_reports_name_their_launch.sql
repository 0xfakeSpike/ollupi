-- A subagent's report names the call that launched it. Its call id could not: a provider may number calls afresh in
-- each response. A report already written names the latest call of its call id before it in its conversation.
ALTER TABLE `items` ADD COLUMN `launch_item_id` text NOT NULL DEFAULT "";
UPDATE `items` SET `launch_item_id` = COALESCE((
  SELECT c.`id` FROM `items` c
  WHERE c.`thread_id` = `items`.`thread_id` AND c.`type` = 'toolCall' AND c.`call_id` = `items`.`call_id`
    AND (c.`turn_seq` < `items`.`turn_seq` OR (c.`turn_seq` = `items`.`turn_seq` AND c.`seq` < `items`.`seq`))
  ORDER BY c.`turn_seq` DESC, c.`seq` DESC LIMIT 1), "")
WHERE `type` = 'agentReport';
