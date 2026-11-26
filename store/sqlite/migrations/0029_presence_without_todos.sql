-- A stretch of presence names its thread and no todo: a todo's time is the time
-- of the thread it is linked to, read through that link when it is wanted.
DROP INDEX presence_todo;
ALTER TABLE presence DROP COLUMN todo_id;
