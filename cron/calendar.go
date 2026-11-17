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
	MaxJobs        = 64
	MaxPromptRunes = 4000
)

var NamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

var ErrInvalidJob = errors.New("invalid scheduled task")

type Calendar struct {
	location *time.Location
	parser   robfig.Parser
}

func NewCalendar(location *time.Location) Calendar {
	return Calendar{location: location, parser: robfig.NewParser(robfig.Minute | robfig.Hour | robfig.Dom | robfig.Month | robfig.Dow | robfig.Descriptor)}
}

func (c Calendar) Job(id, thread, name, spec, prompt string, times *int, now time.Time) (user.CronJob, error) {
	name, spec, prompt = strings.TrimSpace(name), strings.TrimSpace(spec), strings.TrimSpace(prompt)
	if !NamePattern.MatchString(name) {
		return user.CronJob{}, fmt.Errorf("cron: %w: name %q must match %s", ErrInvalidJob, name, NamePattern)
	}
	if prompt == "" || utf8.RuneCountInString(prompt) > MaxPromptRunes {
		return user.CronJob{}, fmt.Errorf("cron: %w: prompt must be 1 to %d characters", ErrInvalidJob, MaxPromptRunes)
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
