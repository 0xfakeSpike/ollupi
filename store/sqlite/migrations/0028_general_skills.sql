INSERT OR IGNORE INTO skill_roles (user_id, name, role)
SELECT DISTINCT user_id, name, 'general-sub-agent' FROM skill_roles
WHERE role IN ('full-main-agent', 'compact-main-agent');
