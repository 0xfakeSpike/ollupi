package repo

// SettingsRow holds the user's global settings. Redaction turns masking of
// sensitive values before they reach a model on or off.
type SettingsRow struct {
	ID        int  `gorm:"primaryKey;autoIncrement:false;check:settings_one,id = 1" json:"-"`
	Redaction bool `gorm:"not null;default:true" json:"redaction"`
}

func (SettingsRow) TableName() string { return "settings" }

func (d *DB) Settings() (SettingsRow, error) {
	var row SettingsRow
	return row, d.gorm.Where("id = 1").Take(&row).Error
}

func (d *DB) SetRedaction(on bool) (SettingsRow, error) {
	if err := checkOne(d.gorm.Model(&SettingsRow{}).Where("id = 1").Update("redaction", on)); err != nil {
		return SettingsRow{}, err
	}
	return SettingsRow{ID: 1, Redaction: on}, nil
}
