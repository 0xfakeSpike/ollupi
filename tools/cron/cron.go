package cron

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
	robfig "github.com/robfig/cron/v3"
)

const (
	maxJobs        = 64
	maxPromptRunes = 4000
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

type Jobs struct {
	db       *repo.DB
	location *time.Location
	parser   robfig.Parser
	onChange func(id, threadID string)
}

// New makes the scheduler. onChange, if not nil, is called after every
// change to a task, whoever made it: created, paused, resumed, deleted, or
// its next run moved on after a run; the gateway reads the task then.
func New(db *repo.DB, location *time.Location, onChange func(id, threadID string)) *Jobs {
	return &Jobs{
		db:       db,
		location: location,
		parser:   robfig.NewParser(robfig.Minute | robfig.Hour | robfig.Dom | robfig.Month | robfig.Dow | robfig.Descriptor),
		onChange: onChange,
	}
}

func (j *Jobs) changed(id, threadID string) {
	if j.onChange != nil {
		j.onChange(id, threadID)
	}
}

func (j *Jobs) schedule(spec string) (robfig.Schedule, error) {
	if strings.HasPrefix(spec, "CRON_TZ=") || strings.HasPrefix(spec, "TZ=") {
		return nil, fmt.Errorf("cron: time-zone prefixes are not allowed; tasks use %s", j.location)
	}
	if strings.HasPrefix(spec, "@") {
		switch spec {
		case "@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly":
		default:
			return nil, fmt.Errorf("cron: unsupported descriptor %q", spec)
		}
	} else if len(strings.Fields(spec)) != 5 {
		return nil, fmt.Errorf("cron: cron spec %q must have 5 fields (minute hour day-of-month month day-of-week)", spec)
	}
	s, err := j.parser.Parse(spec)
	if err != nil {
		return nil, fmt.Errorf("cron: invalid cron spec %q: %w", spec, err)
	}
	return s, nil
}

func (j *Jobs) next(spec string, after time.Time) (time.Time, error) {
	s, err := j.schedule(spec)
	if err != nil {
		return time.Time{}, err
	}
	local := after.In(j.location)
	wall := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), time.UTC)
	for range 1000 {
		wall = s.Next(wall)
		if wall.IsZero() {
			break
		}
		at := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, j.location)
		got := at.In(j.location)
		if shown := time.Date(got.Year(), got.Month(), got.Day(), got.Hour(), got.Minute(), 0, 0, time.UTC); !shown.Equal(wall) {
			at = at.Add(wall.Sub(shown))
		}
		if at.After(after) {
			return at.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cron: spec %q has no next run", spec)
}

func (j *Jobs) Create(threadID, name, spec, prompt string) (repo.CronJobRow, error) {
	name, spec, prompt = strings.TrimSpace(name), strings.TrimSpace(spec), strings.TrimSpace(prompt)
	if threadID == "" {
		return repo.CronJobRow{}, fmt.Errorf("cron: a task belongs to a conversation")
	}
	if !namePattern.MatchString(name) {
		return repo.CronJobRow{}, fmt.Errorf("cron: name %q must match %s", name, namePattern)
	}
	if prompt == "" || len([]rune(prompt)) > maxPromptRunes {
		return repo.CronJobRow{}, fmt.Errorf("cron: prompt must be 1 to %d characters", maxPromptRunes)
	}
	now := time.Now().UTC()
	next, err := j.next(spec, now)
	if err != nil {
		return repo.CronJobRow{}, err
	}
	job := repo.CronJobRow{ID: uuid.NewString(), ThreadID: threadID, Name: name, Spec: spec, Prompt: prompt, NextRunAt: next, CreatedAt: now}
	if err := j.db.CreateCronJob(job, maxJobs); err != nil {
		return repo.CronJobRow{}, err
	}
	j.changed(job.ID, job.ThreadID)
	return job, nil
}

// List lists the tasks of threadID, or of every conversation when it is
// empty, with what a list shows of their conversations and latest runs.
func (j *Jobs) List(threadID string) ([]repo.CronJobView, error) {
	return j.db.CronJobViews(threadID)
}

// Job reads one task as List shows it; repo.ErrNotFound once it is deleted.
func (j *Jobs) Job(id string) (repo.CronJobView, error) {
	return j.db.CronJobView(id)
}

func (j *Jobs) Delete(threadID, name string) error {
	_, err := j.change(threadID, name, func(repo.CronJobRow) (*repo.CronJobRow, error) { return nil, nil })
	return err
}

func (j *Jobs) SetPaused(threadID, name string, paused bool) (repo.CronJobRow, error) {
	return j.change(threadID, name, func(job repo.CronJobRow) (*repo.CronJobRow, error) {
		job.Paused = paused
		if !paused {
			next, err := j.next(job.Spec, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			job.NextRunAt = next
		}
		return &job, nil
	})
}

func (j *Jobs) Sent(job repo.CronJobRow, now time.Time) error {
	next, err := j.next(job.Spec, now)
	if err != nil {
		return err
	}
	if err := j.db.SetCronJobNextRun(job.ID, job.NextRunAt, next); err != nil {
		return err
	}
	j.changed(job.ID, job.ThreadID)
	return nil
}

func (j *Jobs) change(threadID, name string, fn func(repo.CronJobRow) (*repo.CronJobRow, error)) (repo.CronJobRow, error) {
	if threadID == "" {
		return repo.CronJobRow{}, fmt.Errorf("cron: a task is changed from its conversation")
	}
	row, err := j.db.UpdateCronJob(threadID, strings.TrimSpace(name), fn)
	if errors.Is(err, repo.ErrNotFound) {
		return repo.CronJobRow{}, fmt.Errorf("cron: this conversation has no task named %q: %w", name, repo.ErrNotFound)
	}
	if err == nil {
		j.changed(row.ID, row.ThreadID)
	}
	return row, err
}

func (j *Jobs) Run(ctx context.Context, send func(threadID, text, jobID string) error) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		j.sendDue(ctx, send)
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (j *Jobs) sendDue(ctx context.Context, send func(threadID, text, jobID string) error) {
	now := time.Now().UTC()
	due, err := j.db.DueCronJobs(now)
	if err != nil {
		log.Printf("cron: due tasks: %v", err)
		return
	}
	for _, job := range due {
		if ctx.Err() != nil {
			return
		}
		waiting, err := j.db.HasQueuedTurn(job.ThreadID, job.ID)
		if err != nil {
			log.Printf("cron: task %s: %v", job.ID, err)
			continue
		}
		if !waiting {
			if err := send(job.ThreadID, job.Prompt, job.ID); err != nil && ctx.Err() == nil {
				log.Printf("cron: task %s could not start in thread %s: %v", job.ID, job.ThreadID, err)
			}
		}
		if err := j.Sent(job, now); err != nil {
			log.Printf("cron: task %s: record its run: %v", job.ID, err)
		}
	}
}
