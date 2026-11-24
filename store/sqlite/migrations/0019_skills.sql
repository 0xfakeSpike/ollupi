CREATE TABLE skills (
  name text PRIMARY KEY,
  description text NOT NULL,
  version text NOT NULL DEFAULT '',
  instructions text NOT NULL,
  digest text NOT NULL
);

CREATE TABLE skill_roles (
  name text NOT NULL REFERENCES skills(name) ON DELETE CASCADE,
  role text NOT NULL,
  PRIMARY KEY (name, role)
);
CREATE INDEX skill_roles_role ON skill_roles(role, name);

CREATE TABLE skill_files (
  name text NOT NULL REFERENCES skills(name) ON DELETE CASCADE,
  path text NOT NULL,
  body blob NOT NULL,
  executable integer NOT NULL DEFAULT 0,
  PRIMARY KEY (name, path)
);
