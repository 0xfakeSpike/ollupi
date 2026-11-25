INSERT OR IGNORE INTO skill_roles (user_id, name, role)
SELECT user_id, name, 'full-main-agent' FROM skill_roles
WHERE role IN ('browser-sub-agent', 'design-sub-agent', 'coding-sub-agent', 'writing-sub-agent', 'frontend-sub-agent', 'backend-sub-agent');
INSERT OR IGNORE INTO skill_roles (user_id, name, role)
SELECT user_id, name, 'compact-main-agent' FROM skill_roles
WHERE role IN ('browser-sub-agent', 'design-sub-agent', 'coding-sub-agent', 'writing-sub-agent', 'frontend-sub-agent', 'backend-sub-agent');
INSERT OR IGNORE INTO skill_roles (user_id, name, role)
SELECT user_id, name, 'explore-sub-agent' FROM skill_roles
WHERE role IN ('codebase-sub-agent', 'research-sub-agent');
DELETE FROM skill_roles
WHERE role IN ('browser-sub-agent', 'design-sub-agent', 'coding-sub-agent', 'writing-sub-agent', 'frontend-sub-agent', 'backend-sub-agent',
  'codebase-sub-agent', 'research-sub-agent');

UPDATE threads SET role = CASE WHEN role IN ('codebase-sub-agent', 'research-sub-agent') THEN 'explore-sub-agent' ELSE 'general-sub-agent' END
WHERE role IN ('browser-sub-agent', 'design-sub-agent', 'coding-sub-agent', 'writing-sub-agent', 'frontend-sub-agent', 'backend-sub-agent',
  'codebase-sub-agent', 'research-sub-agent');

UPDATE turns SET body = json_set(body, '$.Role',
  CASE WHEN json_extract(body, '$.Role') IN ('codebase-sub-agent', 'research-sub-agent') THEN 'explore-sub-agent' ELSE 'general-sub-agent' END)
WHERE json_extract(body, '$.Role') IN ('browser-sub-agent', 'design-sub-agent', 'coding-sub-agent', 'writing-sub-agent', 'frontend-sub-agent', 'backend-sub-agent',
  'codebase-sub-agent', 'research-sub-agent');

UPDATE entries SET body = json_set(body, '$.Role',
  CASE WHEN json_extract(body, '$.Role') IN ('codebase-sub-agent', 'research-sub-agent') THEN 'explore-sub-agent' ELSE 'general-sub-agent' END)
WHERE kind = 'turnQueued'
  AND json_extract(body, '$.Role') IN ('browser-sub-agent', 'design-sub-agent', 'coding-sub-agent', 'writing-sub-agent', 'frontend-sub-agent', 'backend-sub-agent',
  'codebase-sub-agent', 'research-sub-agent');
