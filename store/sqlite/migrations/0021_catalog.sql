-- The deployment's catalog: the providers every user's turns run on, each an
-- API account with its key, and the models each serves. It is no user's: a
-- user only chooses among it. Order matters, as the first provider's first
-- model is the one a user starts with until they choose.
CREATE TABLE providers (
  id text PRIMARY KEY,
  position integer NOT NULL UNIQUE,
  api text NOT NULL,
  base_url text NOT NULL DEFAULT '',
  api_key text NOT NULL DEFAULT ''
);

CREATE TABLE provider_models (
  provider_id text NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  position integer NOT NULL,
  name text NOT NULL,
  calls integer NOT NULL DEFAULT 0,
  thinking integer,
  images integer NOT NULL DEFAULT 0,
  limits text NOT NULL CHECK (json_valid(limits)),
  PRIMARY KEY (provider_id, name),
  UNIQUE (provider_id, position)
);
