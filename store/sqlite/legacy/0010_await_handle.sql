-- What fulfils a call's wait is named by a handle, not a thread: a subagent's thread now, a background call later.
UPDATE `items` SET `await` = json_remove(json_set(`await`, '$.handle', json_extract(`await`, '$.threadId')), '$.threadId')
WHERE `await` IS NOT NULL AND json_extract(`await`, '$.threadId') IS NOT NULL;
