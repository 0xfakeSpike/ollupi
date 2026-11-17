-- A user keeps their own permission layer, whether they use long-term memory, and their MCP servers.
ALTER TABLE `settings` ADD COLUMN `permissions` text NOT NULL DEFAULT '{}' CONSTRAINT `settings_permissions` CHECK (json_valid(permissions));
ALTER TABLE `settings` ADD COLUMN `memory` numeric NOT NULL DEFAULT true;
CREATE TABLE `mcp_servers` (
  `user_id` text,
  `name` text,
  `command` text NOT NULL DEFAULT "",
  `args` text NOT NULL DEFAULT '[]',
  `env` text NOT NULL DEFAULT '{}',
  `url` text NOT NULL DEFAULT "",
  `headers` text NOT NULL DEFAULT '{}',
  PRIMARY KEY (`user_id`,`name`),
  CONSTRAINT `fk_mcp_servers_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`) ON DELETE RESTRICT,
  CONSTRAINT `mcp_servers_args` CHECK (json_valid(args)),
  CONSTRAINT `mcp_servers_env` CHECK (json_valid(env)),
  CONSTRAINT `mcp_servers_headers` CHECK (json_valid(headers))
);
