package memory

const triggersSQL = `
CREATE TRIGGER memory_turn_ended AFTER UPDATE OF status ON turns
WHEN NEW.status IN ('completed','failed','interrupted') AND OLD.status IS NOT NEW.status BEGIN
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  VALUES(COALESCE((SELECT workspace_id FROM threads WHERE id=NEW.thread_id),''),'turns',NEW.id,'upsert',json_object('status',NEW.status));
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  SELECT COALESCE((SELECT workspace_id FROM threads WHERE id=NEW.thread_id),''),'items',id,'upsert',json_object('type',type)
  FROM items WHERE turn_id=NEW.id AND type IN ('userMessage','agentMessage','toolCall','error')
    AND status IN ('completed','failed','declined');
END;

CREATE TRIGGER memory_workspace_insert AFTER INSERT ON workspaces BEGIN
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  VALUES(NEW.id,'workspaces',NEW.id,'upsert',json_object('archived',NEW.archived_at IS NOT NULL));
END;

CREATE TRIGGER memory_workspace_update AFTER UPDATE ON workspaces
WHEN NEW.name IS NOT OLD.name OR NEW.root_path IS NOT OLD.root_path OR NEW.archived_at IS NOT OLD.archived_at BEGIN
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  VALUES(NEW.id,'workspaces',NEW.id,'upsert',json_object('archived',NEW.archived_at IS NOT NULL));
END;

CREATE TRIGGER memory_workspace_delete BEFORE DELETE ON workspaces BEGIN
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  VALUES(OLD.id,'workspaces',OLD.id,'delete',json_object('identity',json_object('name',OLD.name)));
END;

CREATE TRIGGER memory_thread_insert AFTER INSERT ON threads BEGIN
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  VALUES(COALESCE(NEW.workspace_id,''),'threads',NEW.id,'upsert','{}');
END;

CREATE TRIGGER memory_thread_update AFTER UPDATE ON threads
WHEN NEW.workspace_id IS NOT OLD.workspace_id OR NEW.parent_thread_id IS NOT OLD.parent_thread_id BEGIN
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  VALUES(COALESCE(NEW.workspace_id,''),'threads',NEW.id,'upsert','{}');
END;

CREATE TRIGGER memory_thread_delete BEFORE DELETE ON threads BEGIN
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  VALUES(COALESCE(OLD.workspace_id,''),'threads',OLD.id,'delete','{}');
END;

CREATE TRIGGER memory_thread_children BEFORE DELETE ON threads BEGIN
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  SELECT COALESCE(OLD.workspace_id,''),'items',id,'delete',json_object('type',type)
  FROM items WHERE thread_id=OLD.id AND type NOT IN ('reasoning','contextCompaction','memoryContext');
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  SELECT COALESCE(OLD.workspace_id,''),'turns',id,'delete','{}' FROM turns WHERE thread_id=OLD.id;
END;

CREATE TRIGGER memory_cron_insert AFTER INSERT ON cron_jobs BEGIN
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  VALUES(COALESCE((SELECT workspace_id FROM threads WHERE id=NEW.thread_id),''),'cron_jobs',NEW.id,'upsert','{}');
END;

CREATE TRIGGER memory_cron_update AFTER UPDATE ON cron_jobs
WHEN NEW.spec IS NOT OLD.spec OR NEW.prompt IS NOT OLD.prompt BEGIN
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  VALUES(COALESCE((SELECT workspace_id FROM threads WHERE id=NEW.thread_id),''),'cron_jobs',NEW.id,'upsert','{}');
END;

CREATE TRIGGER memory_cron_delete BEFORE DELETE ON cron_jobs BEGIN
  INSERT INTO memory_events(workspace_id,source_table,source_id,operation,metadata)
  VALUES(COALESCE((SELECT workspace_id FROM threads WHERE id=OLD.thread_id),''),'cron_jobs',OLD.id,'delete',json_object('identity',json_object('prompt',OLD.prompt)));
END;
`
