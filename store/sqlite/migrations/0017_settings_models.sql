-- Which of the deployment's models a user chose: the one a new conversation
-- starts with, and the one that learns their memories. The models themselves
-- are the deployment's; empty is its first model, and no learning.
ALTER TABLE settings ADD COLUMN model text NOT NULL DEFAULT '';
ALTER TABLE settings ADD COLUMN learn_model text NOT NULL DEFAULT '';
