package service

import (
	"context"
	"fmt"

	"github.com/0xfakeSpike/ollupi/repo/db"
)

func (sv *Service) CronJobs(ctx context.Context, user, threadID string) ([]db.CronJobView, error) {
	return sv.cfg.DB.WithContext(ctx).CronJobViews(user, threadID)
}

func (sv *Service) CreateCronJob(user, threadID, name, spec, prompt string, times *int) (db.CronJobRow, error) {
	if err := sv.cronThread(user, threadID); err != nil {
		return db.CronJobRow{}, err
	}
	return sv.cfg.Jobs.Create(threadID, name, spec, prompt, times)
}

func (sv *Service) PauseCronJob(user, threadID, name string, paused bool) (db.CronJobRow, error) {
	if err := sv.cronThread(user, threadID); err != nil {
		return db.CronJobRow{}, err
	}
	return sv.cfg.Jobs.SetPaused(threadID, name, paused)
}

func (sv *Service) DeleteCronJob(user, threadID, name string) error {
	if err := sv.cronThread(user, threadID); err != nil {
		return err
	}
	_, err := sv.cfg.Jobs.Delete(threadID, name)
	return err
}

func (sv *Service) cronThread(user, threadID string) error {
	if sv.cfg.Jobs == nil {
		return fmt.Errorf("service: scheduled tasks are %w", ErrUnavailable)
	}
	return sv.owned(user, threadID)
}
