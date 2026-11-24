DROP TABLE skill_files;
DROP TABLE skill_roles;
DROP TABLE skills;

CREATE TABLE skills (
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name text NOT NULL,
  description text NOT NULL,
  version text NOT NULL DEFAULT '',
  instructions text NOT NULL,
  digest text NOT NULL,
  PRIMARY KEY (user_id, name)
);

CREATE TABLE skill_roles (
  user_id text NOT NULL,
  name text NOT NULL,
  role text NOT NULL,
  PRIMARY KEY (user_id, name, role),
  FOREIGN KEY (user_id, name) REFERENCES skills(user_id, name) ON DELETE CASCADE
);

CREATE TABLE skill_files (
  user_id text NOT NULL,
  name text NOT NULL,
  path text NOT NULL,
  body blob NOT NULL,
  executable integer NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, name, path),
  FOREIGN KEY (user_id, name) REFERENCES skills(user_id, name) ON DELETE CASCADE
);
