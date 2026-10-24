package repo

import (
	"fmt"
	"time"
)

type CronJobRow struct {
	ID        string     `gorm:"primaryKey" json:"id"`
	ThreadID  string     `gorm:"not null;uniqueIndex:cron_jobs_thread_name,priority:1" json:"threadId"`
	Name      string     `gorm:"not null;uniqueIndex:cron_jobs_thread_name,priority:2" json:"name"`
	Thread    *ThreadRow `gorm:"constraint:OnDelete:CASCADE" json:"-"`
	Spec      string     `gorm:"not null" json:"spec"`
	Prompt    string     `gorm:"not null" json:"prompt"`
	Paused    bool       `gorm:"not null;index:cron_jobs_due,priority:1" json:"paused"`
	NextRunAt time.Time  `gorm:"not null;index:cron_jobs_due,priority:2" json:"nextRunAt"`
	CreatedAt time.Time  `gorm:"not null;autoCreateTime:false" json:"createdAt"`
}

func (CronJobRow) TableName() string { return "cron_jobs" }

func (d *DB) CreateCronJob(job CronJobRow, max int) error {
	job.NextRunAt, job.CreatedAt = job.NextRunAt.UTC(), job.CreatedAt.UTC()
	return d.Tx(func(tx *DB) error {
		var count int64
		if err := tx.gorm.Model(&CronJobRow{}).Where("thread_id = ?", job.ThreadID).Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(max) {
			return fmt.Errorf("cron: a conversation has at most %d tasks", max)
		}
		var taken int64
		if err := tx.gorm.Model(&CronJobRow{}).Where("thread_id = ? AND name = ?", job.ThreadID, job.Name).Count(&taken).Error; err != nil {
			return err
		}
		if taken > 0 {
			return fmt.Errorf("cron: this conversation already has a task named %q", job.Name)
		}
		if err := tx.gorm.Create(&job).Error; err != nil {
			return fmt.Errorf("cron: store task %q: %w", job.Name, err)
		}
		return nil
	})
}

func (d *DB) CronJobs(threadID string) ([]CronJobRow, error) {
	var rows []CronJobRow
	q := d.gorm.Order("thread_id, name")
	if threadID != "" {
		q = q.Where("thread_id = ?", threadID)
	}
	return rows, q.Find(&rows).Error
}

func (d *DB) DueCronJobs(now time.Time) ([]CronJobRow, error) {
	var rows []CronJobRow
	err := d.gorm.Where("paused = ? AND next_run_at <= ?", false, now.UTC()).Order("next_run_at, id").Find(&rows).Error
	return rows, err
}

func (d *DB) UpdateCronJob(threadID, name string, change func(CronJobRow) (*CronJobRow, error)) (CronJobRow, error) {
	var out CronJobRow
	err := d.Tx(func(tx *DB) error {
		var row CronJobRow
		if err := notFound(tx.gorm.Where("thread_id = ? AND name = ?", threadID, name).Take(&row).Error); err != nil {
			return err
		}
		id := row.ID
		next, err := change(row)
		if err != nil {
			return err
		}
		if next == nil {
			return tx.gorm.Delete(&CronJobRow{}, "id = ?", id).Error
		}
		out = *next
		return tx.gorm.Model(&CronJobRow{}).Where("id = ?", id).
			Updates(map[string]any{"paused": next.Paused, "next_run_at": next.NextRunAt.UTC()}).Error
	})
	return out, err
}

func (d *DB) SetCronJobNextRun(id string, due, next time.Time) error {
	return d.gorm.Model(&CronJobRow{}).Where("id = ? AND next_run_at = ?", id, due.UTC()).Update("next_run_at", next.UTC()).Error
}

func (d *DB) CronJobWithScope(id string) (CronJobRow, string, error) {
	var row struct {
		CronJobRow
		Scope string
	}
	err := d.gorm.Table("cron_jobs AS j").Select("j.*, COALESCE(t.workspace_id, '') AS scope").
		Joins("JOIN threads t ON t.id = j.thread_id").Where("j.id = ?", id).Take(&row).Error
	return row.CronJobRow, row.Scope, notFound(err)
}
