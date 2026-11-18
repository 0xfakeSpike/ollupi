package cron

import (
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/store"
	"github.com/google/uuid"
)

func Create(tx store.Tx, cal Calendar, th store.Thread, d Draft, now time.Time) (store.CronJobView, error) {
	job, err := cal.NewJob(uuid.NewString(), th.ID, d, now)
	if err != nil {
		return store.CronJobView{}, err
	}
	jobs, err := tx.CronJobs(th.User, th.ID)
	if err != nil {
		return store.CronJobView{}, err
	}
	if len(jobs) >= MaxJobs {
		return store.CronJobView{}, fmt.Errorf("%w: a conversation has at most %d scheduled jobs", ErrInvalidJob, MaxJobs)
	}
	for _, t := range jobs {
		if t.Name == job.Name {
			return store.CronJobView{}, fmt.Errorf("%w: this conversation already has a scheduled job named %q", ErrInvalidJob, job.Name)
		}
	}
	if err := tx.PutCronJob(job); err != nil {
		return store.CronJobView{}, err
	}
	return named(tx, th, job.Name)
}

func Pause(tx store.Tx, cal Calendar, th store.Thread, name string, paused bool, now time.Time) (store.CronJobView, error) {
	job, err := named(tx, th, name)
	if err != nil {
		return store.CronJobView{}, err
	}
	if job.CronJob, err = cal.Pause(job.CronJob, paused, now); err != nil {
		return store.CronJobView{}, err
	}
	return job, tx.PutCronJob(job.CronJob)
}

func Delete(tx store.Tx, th store.Thread, name string) error {
	job, err := named(tx, th, name)
	if err != nil {
		return err
	}
	return tx.DeleteCronJob(job.ID)
}

func named(r store.Reader, th store.Thread, name string) (store.CronJobView, error) {
	jobs, err := r.CronJobs(th.User, th.ID)
	if err != nil {
		return store.CronJobView{}, err
	}
	name = strings.TrimSpace(name)
	for _, t := range jobs {
		if t.Name == name {
			return t, nil
		}
	}
	return store.CronJobView{}, fmt.Errorf("this conversation has no scheduled job named %q: %w", name, store.ErrNotFound)
}
