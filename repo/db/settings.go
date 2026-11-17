package db

type SettingsRow struct {
	UserID    string   `gorm:"primaryKey" json:"-"`
	User      *UserRow `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT" json:"-"`
	Redaction bool     `gorm:"not null;default:true" json:"redaction"`
	// Made once per user, so a value masks to the same placeholder across restarts and the transcript sent to the
	// model stays stable.
	RedactionKey string `gorm:"not null" json:"-"`
	// The user's layer, under the permissions of their workspaces and conversations and over the deployment's.
	Permissions PermissionSettings `gorm:"not null;default:'{}';serializer:json;check:settings_permissions,json_valid(permissions)" json:"permissions"`
	// Off, the user's conversations neither recall nor search long-term memory, and none is made from them.
	Memory bool `gorm:"not null;default:true" json:"memory"`
}

func (SettingsRow) TableName() string { return "settings" }

func (d *DB) Settings(user string) (SettingsRow, error) {
	var row SettingsRow
	return row, notFound(d.gorm.Where("user_id = ?", user).Take(&row).Error)
}

// change is applied to the settings as they are when written.
func (d *DB) UpdateSettings(user string, change func(SettingsRow) SettingsRow) (SettingsRow, error) {
	var out SettingsRow
	err := d.Tx(func(tx *DB) error {
		row, err := tx.Settings(user)
		if err != nil {
			return err
		}
		out = change(row)
		return tx.gorm.Model(&SettingsRow{}).Where("user_id = ?", user).
			Select("redaction", "permissions", "memory").Updates(&out).Error
	})
	return out, err
}

type MCPServerRow struct {
	UserID  string            `gorm:"primaryKey" json:"-"`
	User    *UserRow          `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT" json:"-"`
	Name    string            `gorm:"primaryKey" json:"name"`
	Command string            `gorm:"not null;default:''" json:"command,omitempty"`
	Args    []string          `gorm:"not null;default:'[]';serializer:json;check:mcp_servers_args,json_valid(args)" json:"args,omitempty"`
	Env     map[string]string `gorm:"not null;default:'{}';serializer:json;check:mcp_servers_env,json_valid(env)" json:"env,omitempty"`
	URL     string            `gorm:"not null;default:''" json:"url,omitempty"`
	Headers map[string]string `gorm:"not null;default:'{}';serializer:json;check:mcp_servers_headers,json_valid(headers)" json:"headers,omitempty"`
}

func (MCPServerRow) TableName() string { return "mcp_servers" }

func (d *DB) MCPServers(user string) ([]MCPServerRow, error) {
	var rows []MCPServerRow
	return rows, d.gorm.Where("user_id = ?", user).Order("name").Find(&rows).Error
}

func (d *DB) SetMCPServer(row MCPServerRow) error {
	return d.gorm.Save(&row).Error
}

func (d *DB) DeleteMCPServer(user, name string) error {
	return checkOne(d.gorm.Where("user_id = ? AND name = ?", user, name).Delete(&MCPServerRow{}))
}
