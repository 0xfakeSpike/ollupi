// Package cron runs scheduled tasks: a task is a prompt sent to its conversation at the times a cron spec names, in
// the deployment's time zone. The calendar holds the rules of a task; Run sends the tasks that are due.
package cron

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0xfakeSpike/ollupi/user"
	robfig "github.com/robfig/cron/v3"
)

const (
	// MaxJobs is how many tasks a conversation has at most.
	MaxJobs        = 64
	maxPromptRunes = 4000
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

var ErrInvalidJob = errors.New("invalid scheduled task")

type Calendar struct {
	location *time.Location
	parser   robfig.Parser
}

func NewCalendar(location *time.Location) Calendar {
	return Calendar{location: location, parser: robfig.NewParser(robfig.Minute | robfig.Hour | robfig.Dom | robfig.Month | robfig.Dow | robfig.Descriptor)}
}

// Job is a new task of thread, checked, with its first run.
func (c Calendar) Job(id, thread, name, spec, prompt string, times *int, now time.Time) (user.CronJob, error) {
	name, spec, prompt = strings.TrimSpace(name), strings.TrimSpace(spec), strings.TrimSpace(prompt)
	if !namePattern.MatchString(name) {
		return user.CronJob{}, fmt.Errorf("cron: %w: name %q must match %s", ErrInvalidJob, name, namePattern)
	}
	if prompt == "" || utf8.RuneCountInString(prompt) > maxPromptRunes {
		return user.CronJob{}, fmt.Errorf("cron: %w: prompt must be 1 to %d characters", ErrInvalidJob, maxPromptRunes)
	}
	if times != nil && *times < 1 {
		return user.CronJob{}, fmt.Errorf("cron: %w: times must be at least 1", ErrInvalidJob)
	}
	next, err := c.Next(spec, now)
	if err != nil {
		return user.CronJob{}, err
	}
	job := user.CronJob{ID: id, Thread: thread, Name: name, Spec: spec, Prompt: prompt, Times: times, NextRun: next, Created: now}
	return job, lastRun(job, now)
}

// Pause pauses a task or resumes it; one resumed runs next from now.
func (c Calendar) Pause(job user.CronJob, paused bool, now time.Time) (user.CronJob, error) {
	job.Paused = paused
	if paused {
		return job, nil
	}
	next, err := c.Next(job.Spec, now)
	if err != nil {
		return user.CronJob{}, err
	}
	job.NextRun = next
	return job, lastRun(job, now)
}

// Ran is the task after its run due now: next due, its run counted unless it was skipped; done once it has run as many
// times as it was to.
func (c Calendar) Ran(job user.CronJob, now time.Time, counted bool) (next user.CronJob, done bool, err error) {
	if counted {
		job.Runs++
	}
	if job.Times != nil && job.Runs >= *job.Times {
		return job, true, nil
	}
	if job.NextRun, err = c.Next(job.Spec, now); err != nil {
		return user.CronJob{}, false, err
	}
	return job, false, nil
}

// A cron spec has no year, so a one-time task whose date has passed would otherwise run on that date next year.
func lastRun(job user.CronJob, now time.Time) error {
	if job.Times != nil && *job.Times-job.Runs == 1 && job.NextRun.After(now.AddDate(1, -1, 0)) {
		return fmt.Errorf("cron: %w: the time of task %q's last run has passed; a spec has no year, so it would run next year", ErrInvalidJob, job.Name)
	}
	return nil
}

func (c Calendar) schedule(spec string) (robfig.Schedule, error) {
	if strings.HasPrefix(spec, "CRON_TZ=") || strings.HasPrefix(spec, "TZ=") {
		return nil, fmt.Errorf("cron: %w: time-zone prefixes are not allowed; tasks use %s", ErrInvalidJob, c.location)
	}
	if strings.HasPrefix(spec, "@") {
		switch spec {
		case "@yearly", "@annually", "@monthly", "@weekly", "@daily", "@midnight", "@hourly":
		default:
			return nil, fmt.Errorf("cron: %w: unsupported descriptor %q", ErrInvalidJob, spec)
		}
	} else if len(strings.Fields(spec)) != 5 {
		return nil, fmt.Errorf("cron: %w: cron spec %q must have 5 fields (minute hour day-of-month month day-of-week)", ErrInvalidJob, spec)
	}
	s, err := c.parser.Parse(spec)
	if err != nil {
		return nil, fmt.Errorf("cron: %w: cron spec %q: %v", ErrInvalidJob, spec, err)
	}
	return s, nil
}

// Next is spec's first time after after. The spec reads the wall clock of the calendar's zone, so a time a clock change
// skips runs as late after the change as it would have after its hour, and one it repeats runs once.
func (c Calendar) Next(spec string, after time.Time) (time.Time, error) {
	s, err := c.schedule(spec)
	if err != nil {
		return time.Time{}, err
	}
	local := after.In(c.location)
	wall := time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), time.UTC)
	for range 1000 {
		wall = s.Next(wall)
		if wall.IsZero() {
			break
		}
		at := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), 0, 0, c.location)
		got := at.In(c.location)
		if shown := time.Date(got.Year(), got.Month(), got.Day(), got.Hour(), got.Minute(), 0, 0, time.UTC); !shown.Equal(wall) {
			at = at.Add(wall.Sub(shown))
		}
		if at.After(after) {
			return at.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cron: spec %q has no next run", spec)
}
