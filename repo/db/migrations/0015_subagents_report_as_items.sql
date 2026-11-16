-- A subagent's launch completes at once, as a call in the background does, and the subagent's report comes back to
-- the turn that launched it as an item of its own when it ends. A launch still waiting completes so; the turn it
-- belongs to waits on its subagents only while one of them still runs.
UPDATE `items` SET `status` = 'completed', `completed_at` = COALESCE(`completed_at`, `started_at`),
  `result` = json_object('output', json_object('subagent', json_extract(`await`, '$.handle'),
    'note', 'It works on its own; its report comes back to this conversation when it ends.')),
  `await` = NULL
WHERE `type` = 'toolCall' AND `status` = 'inProgress' AND json_extract(`await`, '$.by') = 'agent';
UPDATE `turns` SET `status` = 'ready' WHERE `status` = 'waitingOnAgent' AND NOT EXISTS (
  SELECT 1 FROM `threads` c JOIN `items` i ON i.`id` = c.`parent_item_id`
  WHERE i.`turn_id` = `turns`.`id` AND EXISTS (SELECT 1 FROM `turns` t WHERE t.`thread_id` = c.`id`
    AND NOT (t.`status` = 'completed' OR t.`status` = 'failed' OR t.`status` = 'interrupted')));
