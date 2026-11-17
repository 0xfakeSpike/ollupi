ALTER TABLE turns ADD COLUMN cron_job text NOT NULL DEFAULT '';
UPDATE turns SET cron_job = COALESCE(json_extract(body, '$.CronJob'), '');
CREATE INDEX turns_cron_job ON turns(cron_job, ord) WHERE cron_job <> '';

ALTER TABLE threads ADD COLUMN own_status text NOT NULL DEFAULT 'idle';
UPDATE threads SET own_status = status;
UPDATE threads SET status = COALESCE(
    (SELECT c.status FROM threads c WHERE c.parent_id = threads.id AND c.status IN ('waitingOnApproval', 'waitingOnUserInput')
      ORDER BY c.status = 'waitingOnApproval' DESC LIMIT 1),
    status)
  WHERE status NOT IN ('waitingOnApproval', 'waitingOnUserInput');
