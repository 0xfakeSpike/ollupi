package app

import (
	"context"
	"time"

	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/store"
)

func (a *App) CronJobs(ctx context.Context, u, thread string) ([]store.Task, error) {
	var tasks []store.Task
	err := a.st.Read(ctx, func(r store.Reader) error {
		if thread != "" {
			if _, err := topThread(r, u, thread); err != nil {
				return err
			}
		}
		var err error
		tasks, err = r.CronJobs(u, thread)
		return err
	})
	return tasks, err
}

func (a *App) CreateCronJob(ctx context.Context, u, thread, name, spec, prompt string, times *int) (store.Task, error) {
	var task store.Task
	err := a.st.Update(ctx, func(tx store.Tx) error {
		th, err := topThread(tx, u, thread)
		if err != nil {
			return err
		}
		task, err = cron.Create(tx, a.cal, th, name, spec, prompt, times, time.Now().UTC())
		return err
	})
	return task, err
}

func (a *App) PauseCronJob(ctx context.Context, u, thread, name string, paused bool) (store.Task, error) {
	var task store.Task
	err := a.st.Update(ctx, func(tx store.Tx) error {
		th, err := topThread(tx, u, thread)
		if err != nil {
			return err
		}
		task, err = cron.Pause(tx, a.cal, th, name, paused, time.Now().UTC())
		return err
	})
	return task, err
}

func (a *App) DeleteCronJob(ctx context.Context, u, thread, name string) error {
	return a.st.Update(ctx, func(tx store.Tx) error {
		th, err := topThread(tx, u, thread)
		if err != nil {
			return err
		}
		return cron.Delete(tx, th, name)
	})
}
