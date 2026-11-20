package app

import (
	"context"
	"time"

	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/store"
)

func (a *App) CronJobs(ctx context.Context, user, id string) ([]store.CronJobView, error) {
	var jobs []store.CronJobView
	err := a.st.Read(ctx, func(r store.Reader) error {
		if id != "" {
			if _, err := thread(r, user, id); err != nil {
				return err
			}
		}
		var err error
		jobs, err = r.CronJobs(user, id)
		return err
	})
	return jobs, err
}

func (a *App) inThread(ctx context.Context, user, id string, change func(store.Tx, store.Thread) error) error {
	return a.st.Update(ctx, func(tx store.Tx) error {
		th, err := thread(tx, user, id)
		if err != nil {
			return err
		}
		return change(tx, th)
	})
}

func (a *App) CreateCronJob(ctx context.Context, user, thread string, d cron.Draft) (job store.CronJobView, err error) {
	err = a.inThread(ctx, user, thread, func(tx store.Tx, th store.Thread) (err error) {
		job, err = cron.Create(tx, a.cal, th, d, time.Now().UTC())
		return err
	})
	return job, err
}

func (a *App) PauseCronJob(ctx context.Context, user, thread, name string, paused bool) (job store.CronJobView, err error) {
	err = a.inThread(ctx, user, thread, func(tx store.Tx, th store.Thread) (err error) {
		job, err = cron.Pause(tx, a.cal, th, name, paused, time.Now().UTC())
		return err
	})
	return job, err
}

func (a *App) DeleteCronJob(ctx context.Context, user, thread, name string) error {
	return a.inThread(ctx, user, thread, func(tx store.Tx, th store.Thread) error { return cron.Delete(tx, th, name) })
}
