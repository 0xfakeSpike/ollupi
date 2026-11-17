-- Each turn names the role of agent that runs it, so that a run, or its recovery after a crash, rebuilds that agent
-- from the turn alone. A subagent thread's turns take the role of the tool its launching call named. Every other turn
-- is a full main agent's: a compact main agent was told apart only by the model's input, and a turn of it still in
-- progress at this step runs on as a full one.
ALTER TABLE `turns` ADD COLUMN `agent` text NOT NULL DEFAULT "full-main-agent";
UPDATE `turns` SET `agent` = CASE (
  SELECT `items`.`tool` FROM `threads` JOIN `items` ON `items`.`id` = `threads`.`parent_item_id`
  WHERE `threads`.`id` = `turns`.`thread_id`
) WHEN 'Agent' THEN 'general-sub-agent' WHEN 'BrowserAgent' THEN 'browser-sub-agent' ELSE `agent` END;
