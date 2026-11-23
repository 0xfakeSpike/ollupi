-- The API that served each model response. A usage row is historical: the
-- provider is what config said at the time, so removing the API from config
-- never rewrites or drops its rows.
ALTER TABLE usage ADD COLUMN provider text NOT NULL DEFAULT '';

-- The backfill below finds a turn's connection through its turnQueued entry.
CREATE INDEX entries_turn ON entries(turn_id, kind);

-- Every row written before the column existed takes the API from the turn it
-- belongs to: turnQueued kept the full connection, "api/model".
UPDATE usage SET provider = COALESCE((
  SELECT CASE
           WHEN instr(json_extract(e.body, '$.Model.connection'), '/') > 0
           THEN substr(json_extract(e.body, '$.Model.connection'), 1,
                       instr(json_extract(e.body, '$.Model.connection'), '/') - 1)
           ELSE ''
         END
  FROM entries e
  WHERE e.turn_id = usage.turn_id AND e.kind = 'turnQueued'
  LIMIT 1
), '')
WHERE provider = '';
