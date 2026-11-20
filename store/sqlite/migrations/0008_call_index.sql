CREATE INDEX items_calls ON items(json_extract(body, '$.Call.Tool'), thread_id) WHERE kind = 'toolCall';
