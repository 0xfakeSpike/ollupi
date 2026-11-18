package app

import (
	"context"
	"time"

	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/store"
)

func (a *App) CronJobs(ctx context.Context, u, thread string) ([]store.CronJobView, error) {
	var jobs []store.CronJobView
	err := a.st.Read(ctx, func(r store.Reader) error {
		if thread != "" {
			if _, err := topThread(r, u, thread); err != nil {
				return err
			}
		}
		var err error
		jobs, err = r.CronJobs(u, thread)
		return err
	})
	return jobs, err
}

func (a *App) inThread(ctx context.Context, u, thread string, change func(store.Tx, store.Thread) error) error {
	return a.st.Update(ctx, func(tx store.Tx) error {
		th, err := topThread(tx, u, thread)
		if err != nil {
			return err
		}
		return change(tx, th)
	})
}

func (a *App) CreateCronJob(ctx context.Context, u, thread string, d cron.Draft) (job store.CronJobView, err error) {
	err = a.inThread(ctx, u, thread, func(tx store.Tx, th store.Thread) (err error) {
		job, err = cron.Create(tx, a.cal, th, d, time.Now().UTC())
		return err
	})
	return job, err
}

func (a *App) PauseCronJob(ctx context.Context, u, thread, name string, paused bool) (job store.CronJobView, err error) {
	err = a.inThread(ctx, u, thread, func(tx store.Tx, th store.Thread) (err error) {
		job, err = cron.Pause(tx, a.cal, th, name, paused, time.Now().UTC())
		return err
	})
	return job, err
}

func (a *App) DeleteCronJob(ctx context.Context, u, thread, name string) error {
	return a.inThread(ctx, u, thread, func(tx store.Tx, th store.Thread) error { return cron.Delete(tx, th, name) })
}
