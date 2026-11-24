-- Where a workspace's folder is being moved to, while it moves: empty when
-- it is not moving. Kept in the row, so a move left by a crash is settled
-- when the host starts again.
ALTER TABLE workspaces ADD COLUMN moving_to text NOT NULL DEFAULT '';
CREATE INDEX workspaces_moving ON workspaces(id) WHERE moving_to <> '';
