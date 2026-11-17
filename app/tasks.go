package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

// CronJobs are a user's conversation's tasks, or all of the user's when thread is ""; a subagent has none.
func (a *App) CronJobs(ctx context.Context, u, thread string) ([]store.Task, error) {
	var jobs []store.Task
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

func (a *App) CreateCronJob(ctx context.Context, u, thread, name, spec, prompt string, times *int) (store.Task, error) {
	job, err := a.cal.Job(uuid.NewString(), thread, name, spec, prompt, times, time.Now().UTC())
	if err != nil {
		return store.Task{}, err
	}
	var task store.Task
	err = a.st.Update(ctx, func(tx store.Tx) error {
		if _, err := topThread(tx, u, thread); err != nil {
			return err
		}
		jobs, err := tx.CronJobs(u, thread)
		if err != nil {
			return err
		}
		if len(jobs) >= cron.MaxJobs {
			return fmt.Errorf("%w: a conversation has at most %d tasks", cron.ErrInvalidJob, cron.MaxJobs)
		}
		for _, j := range jobs {
			if j.Name == job.Name {
				return fmt.Errorf("%w: this conversation already has a task named %q", cron.ErrInvalidJob, job.Name)
			}
		}
		if err := tx.PutCronJob(job); err != nil {
			return err
		}
		task, err = cronJob(tx, u, thread, job.Name)
		return err
	})
	return task, err
}

func (a *App) PauseCronJob(ctx context.Context, u, thread, name string, paused bool) (store.Task, error) {
	var task store.Task
	err := a.st.Update(ctx, func(tx store.Tx) (err error) {
		if task, err = cronJob(tx, u, thread, name); err != nil {
			return err
		}
		if task.CronJob, err = a.cal.Pause(task.CronJob, paused, time.Now().UTC()); err != nil {
			return err
		}
		return tx.PutCronJob(task.CronJob)
	})
	return task, err
}

func (a *App) DeleteCronJob(ctx context.Context, u, thread, name string) error {
	return a.st.Update(ctx, func(tx store.Tx) error {
		job, err := cronJob(tx, u, thread, name)
		if err != nil {
			return err
		}
		return tx.DeleteCronJob(job.ID)
	})
}

// cronJob is a task of a user's conversation, which names it.
func cronJob(r store.Reader, u, thread, name string) (store.Task, error) {
	if _, err := topThread(r, u, thread); err != nil {
		return store.Task{}, err
	}
	jobs, err := r.CronJobs(u, thread)
	if err != nil {
		return store.Task{}, err
	}
	name = strings.TrimSpace(name)
	for _, j := range jobs {
		if j.Name == name {
			return j, nil
		}
	}
	return store.Task{}, fmt.Errorf("this conversation has no task named %q: %w", name, store.ErrNotFound)
}
