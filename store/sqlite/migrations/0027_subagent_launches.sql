UPDATE items SET body = json_set(body, '$.Call.Tool',
  CASE WHEN json_extract(body, '$.Call.Tool') IN ('codebase-sub-agent', 'research-sub-agent') THEN 'explore-sub-agent' ELSE 'general-sub-agent' END)
WHERE kind = 'toolCall'
  AND json_extract(body, '$.Call.Tool') IN ('browser-sub-agent', 'design-sub-agent', 'coding-sub-agent', 'writing-sub-agent', 'frontend-sub-agent', 'backend-sub-agent',
  'codebase-sub-agent', 'research-sub-agent');
