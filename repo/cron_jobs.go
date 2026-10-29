package repo

import (
	"fmt"
	"time"

	"gorm.io/gorm"
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

type CronJobView struct {
	CronJobRow
	ThreadTitle    string     `json:"threadTitle"`
	WorkspaceID    string     `json:"workspaceId,omitempty"`
	ParentThreadID string     `json:"parentThreadId,omitempty"`
	ThreadArchived bool       `json:"threadArchived,omitempty"`
	LastRunAt      *time.Time `json:"lastRunAt,omitempty"`
	LastTurnID     string     `json:"lastTurnId,omitempty"`
}

func (d *DB) CronJobViews(threadID string) ([]CronJobView, error) {
	var rows []CronJobView
	q := d.cronJobViews().Order("j.thread_id, j.name")
	if threadID != "" {
		q = q.Where("j.thread_id = ?", threadID)
	}
	return rows, q.Scan(&rows).Error
}

func (d *DB) CronJobView(id string) (CronJobView, error) {
	var rows []CronJobView
	if err := d.cronJobViews().Where("j.id = ?", id).Scan(&rows).Error; err != nil {
		return CronJobView{}, err
	}
	if len(rows) == 0 {
		return CronJobView{}, ErrNotFound
	}
	return rows[0], nil
}

func (d *DB) cronJobViews() *gorm.DB {
	return d.gorm.Table("cron_jobs AS j").
		Select("j.*, t.title AS thread_title, COALESCE(t.workspace_id, '') AS workspace_id, " +
			"COALESCE(t.parent_thread_id, '') AS parent_thread_id, COALESCE(lt.id, '') AS last_turn_id, lt.started_at AS last_run_at, " +
			"(t.archived_at IS NOT NULL OR p.archived_at IS NOT NULL) AS thread_archived").
		Joins("JOIN threads t ON t.id = j.thread_id").
		Joins("LEFT JOIN threads p ON p.id = t.parent_thread_id").
		Joins("LEFT JOIN turns lt ON lt.id = (SELECT id FROM turns WHERE cron_job_id = j.id ORDER BY started_at DESC, id DESC LIMIT 1)")
}

func (d *DB) DueCronJobs(now time.Time) ([]CronJobRow, error) {
	var rows []CronJobRow
	err := d.gorm.Table("cron_jobs AS j").Select("j.*").
		Joins("JOIN threads t ON t.id = j.thread_id").
		Joins("LEFT JOIN threads p ON p.id = t.parent_thread_id").
		Where("j.paused = ? AND j.next_run_at <= ? AND t.archived_at IS NULL AND p.archived_at IS NULL", false, now.UTC()).
		Order("j.next_run_at, j.id").Find(&rows).Error
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
			out = row
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
