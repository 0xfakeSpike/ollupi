package cron

import (
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

func Create(tx store.Tx, cal Calendar, th store.Thread, d Draft, now time.Time) (store.Task, error) {
	job, err := cal.NewJob(uuid.NewString(), th.ID, d, now)
	if err != nil {
		return store.Task{}, err
	}
	tasks, err := tx.CronJobs(th.User, th.ID)
	if err != nil {
		return store.Task{}, err
	}
	if len(tasks) >= MaxJobs {
		return store.Task{}, fmt.Errorf("%w: a conversation has at most %d tasks", ErrInvalidJob, MaxJobs)
	}
	for _, t := range tasks {
		if t.Name == job.Name {
			return store.Task{}, fmt.Errorf("%w: this conversation already has a task named %q", ErrInvalidJob, job.Name)
		}
	}
	if err := tx.PutCronJob(job); err != nil {
		return store.Task{}, err
	}
	return named(tx, th, job.Name)
}

func Pause(tx store.Tx, cal Calendar, th store.Thread, name string, paused bool, now time.Time) (store.Task, error) {
	task, err := named(tx, th, name)
	if err != nil {
		return store.Task{}, err
	}
	if task.CronJob, err = cal.Pause(task.CronJob, paused, now); err != nil {
		return store.Task{}, err
	}
	return task, tx.PutCronJob(task.CronJob)
}

func Delete(tx store.Tx, th store.Thread, name string) error {
	task, err := named(tx, th, name)
	if err != nil {
		return err
	}
	return tx.DeleteCronJob(task.ID)
}

func named(r store.Reader, th store.Thread, name string) (store.Task, error) {
	tasks, err := r.CronJobs(th.User, th.ID)
	if err != nil {
		return store.Task{}, err
	}
	name = strings.TrimSpace(name)
	for _, t := range tasks {
		if t.Name == name {
			return t, nil
		}
	}
	return store.Task{}, fmt.Errorf("this conversation has no task named %q: %w", name, store.ErrNotFound)
}
