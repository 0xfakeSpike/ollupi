-- Messages are found by substring through a trigram index over the text of completed user and agent messages,
-- kept by triggers as items change. It reads that text from items by rowid; items has no INTEGER PRIMARY KEY,
-- so a VACUUM, or a step that rebuilds items, renumbers the rowids and must rebuild this index after it.
CREATE VIRTUAL TABLE `items_search` USING fts5(text, content='items', content_rowid='rowid', tokenize='trigram');
INSERT INTO `items_search`(rowid, text) SELECT rowid, text FROM `items`
  WHERE (type = 'userMessage' OR type = 'agentMessage') AND status = 'completed';

CREATE TRIGGER `items_search_insert` AFTER INSERT ON `items`
  WHEN (new.type = 'userMessage' OR new.type = 'agentMessage') AND new.status = 'completed'
BEGIN
  INSERT INTO `items_search`(rowid, text) VALUES (new.rowid, new.text);
END;

CREATE TRIGGER `items_search_delete` AFTER DELETE ON `items`
  WHEN (old.type = 'userMessage' OR old.type = 'agentMessage') AND old.status = 'completed'
BEGIN
  INSERT INTO `items_search`(items_search, rowid, text) VALUES ('delete', old.rowid, old.text);
END;

-- One trigger takes the old text out before it puts the new in, so an update that keeps the text keeps it indexed.
CREATE TRIGGER `items_search_update` AFTER UPDATE OF text, status ON `items`
  WHEN (old.type = 'userMessage' OR old.type = 'agentMessage') AND (old.status = 'completed' OR new.status = 'completed')
BEGIN
  INSERT INTO `items_search`(items_search, rowid, text) SELECT 'delete', old.rowid, old.text WHERE old.status = 'completed';
  INSERT INTO `items_search`(rowid, text) SELECT new.rowid, new.text WHERE new.status = 'completed';
END;
