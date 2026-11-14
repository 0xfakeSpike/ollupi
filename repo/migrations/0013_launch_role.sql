-- A call that launches a subagent names the role it launches in what it waits on, so that the subagent is launched by
-- its role rather than by the call's tool. A launch that waits at this step takes the role of its tool.
UPDATE `items` SET `await` = json_set(`await`, '$.request.role', CASE `tool` WHEN 'Agent' THEN 'general-sub-agent' ELSE 'browser-sub-agent' END)
WHERE `type` = 'toolCall' AND `tool` IN ('Agent', 'BrowserAgent') AND json_extract(`await`, '$.by') = 'agent';
