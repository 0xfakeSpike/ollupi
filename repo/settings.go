package repo

type SettingsRow struct {
	UserID    string   `gorm:"primaryKey" json:"-"`
	User      *UserRow `gorm:"foreignKey:UserID;constraint:OnDelete:RESTRICT" json:"-"`
	Redaction bool     `gorm:"not null;default:true" json:"redaction"`
	// RedactionKey keys the redaction placeholders. It is made once per user, so a value
	// masks to the same placeholder across restarts and the transcript sent to the model stays stable.
	RedactionKey string `gorm:"not null" json:"-"`
}

func (SettingsRow) TableName() string { return "settings" }

func (d *DB) Settings(user string) (SettingsRow, error) {
	var row SettingsRow
	return row, d.gorm.Where("user_id = ?", user).Take(&row).Error
}

func (d *DB) SetRedaction(user string, on bool) (SettingsRow, error) {
	if err := checkOne(d.gorm.Model(&SettingsRow{}).Where("user_id = ?", user).Update("redaction", on)); err != nil {
		return SettingsRow{}, err
	}
	return SettingsRow{UserID: user, Redaction: on}, nil
}
