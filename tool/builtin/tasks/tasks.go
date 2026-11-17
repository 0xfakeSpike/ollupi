// Package tasks gives the main agent the tools of its conversation's scheduled tasks: creating, pausing, deleting and
// listing them, as the user would.
package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/0xfakeSpike/ollupi/user"
	"github.com/google/jsonschema-go/jsonschema"
)

const (
	CreateTool = "create_scheduled_job"
	DeleteTool = "delete_scheduled_job"
	PauseTool  = "pause_scheduled_job"
	ListTool   = "list_scheduled_jobs"
)

// Jobs are the commands on a user's conversation's tasks, which the app has.
type Jobs interface {
	CronJobs(ctx context.Context, u, thread string) ([]user.CronJob, error)
	CreateCronJob(ctx context.Context, u, thread, name, spec, prompt string, times *int) (user.CronJob, error)
	PauseCronJob(ctx context.Context, u, thread, name string, paused bool) (user.CronJob, error)
	DeleteCronJob(ctx context.Context, u, thread, name string) error
}

type Provider struct {
	jobs     Jobs
	location *time.Location
}

// New takes the zone the deployment's calendar reads specs in, which the model is told.
func New(jobs Jobs, location *time.Location) Provider { return Provider{jobs: jobs, location: location} }

func (Provider) Name() (string, string) {
	return agent.CronProvider, "Scheduled tasks: messages sent to this conversation later, on a cron schedule, repeatedly or a set number of times."
}

type createArgs struct {
	Name   string `json:"name" jsonschema:"Short task name, unique in this conversation, e.g. daily-market-report."`
	Spec   string `json:"spec" jsonschema:"5-field cron expression (minute hour day-of-month month day-of-week), or @hourly/@daily/@weekly/@monthly/@yearly."`
	Prompt string `json:"prompt" jsonschema:"The message sent at each run, as the user would write it."`
	Times  *int   `json:"times,omitempty" jsonschema:"How many times the task runs before it ends. Leave it out to repeat until deleted. For a task that runs once, give a spec naming its date and time (minute hour day-of-month month *) and times 1."`
}

func (createArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["name"].Pattern = cron.NamePattern.String()
	s.Properties["prompt"].MinLength, s.Properties["prompt"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(cron.MaxPromptRunes)
	s.Properties["times"].Minimum = jsonschema.Ptr(1.0)
}

type pauseArgs struct {
	Name   string `json:"name"`
	Paused bool   `json:"paused" jsonschema:"true to pause, false to resume."`
}

type nameArgs struct {
	Name string `json:"name"`
}

func (p Provider) Tools(context.Context) ([]kit.Spec, error) {
	return []kit.Spec{
		{
			Name:        CreateTool,
			Annotations: tool.Annotations{DestructiveHint: kit.Hint(false), OpenWorldHint: kit.Hint(false)},
			Description: fmt.Sprintf("Schedule a task in this conversation: at each time the cron schedule names (timezone %s), "+
				"prompt is sent here as a new message and you work on it with this conversation's history, project, and settings. "+
				"If a turn is still running then, the message waits as the next turn. "+
				"With times, the task ends after that many runs; a spec has no year, so a one-time task must name a date still ahead.",
				p.location),
			InputSchema: kit.InputSchema[createArgs](),
		},
		{
			Name:        PauseTool,
			Description: "Pause or resume a scheduled task of this conversation. A resumed task runs next at its next scheduled time.",
			Annotations: tool.Annotations{DestructiveHint: kit.Hint(false), OpenWorldHint: kit.Hint(false)},
			InputSchema: kit.InputSchema[pauseArgs](),
		},
		{
			Name:        DeleteTool,
			Description: "Delete a scheduled task of this conversation by name.",
			Annotations: tool.Annotations{DestructiveHint: kit.Hint(true), OpenWorldHint: kit.Hint(false)},
			InputSchema: kit.InputSchema[nameArgs](),
		},
		{
			Name:        ListTool,
			Description: "List the scheduled tasks of this conversation with their next run time.",
			Annotations: tool.Annotations{ReadOnlyHint: true, OpenWorldHint: kit.Hint(false)},
			InputSchema: kit.InputSchema[struct{}](),
		},
	}, nil
}

// job is a task as the model reads it.
type job struct {
	Name    string    `json:"name"`
	Spec    string    `json:"spec"`
	Prompt  string    `json:"prompt"`
	Times   *int      `json:"times,omitempty"`
	Runs    int       `json:"runs"`
	Paused  bool      `json:"paused"`
	NextRun time.Time `json:"nextRun"`
}

func shown(j user.CronJob) job {
	return job{Name: j.Name, Spec: j.Spec, Prompt: j.Prompt, Times: j.Times, Runs: j.Runs, Paused: j.Paused, NextRun: j.NextRun}
}

func (p Provider) Invoke(ctx context.Context, c tool.Call) (kit.Result, error) {
	switch c.Tool {
	case CreateTool:
		args, _ := kit.DecodeArguments[createArgs](c.Arguments) // the schema checked its fields: it cannot fail
		j, err := p.jobs.CreateCronJob(ctx, c.User, c.Thread, args.Name, args.Spec, args.Prompt, args.Times)
		return kit.Result{Output: shown(j)}, err
	case PauseTool:
		args, _ := kit.DecodeArguments[pauseArgs](c.Arguments) // the schema checked its fields: it cannot fail
		j, err := p.jobs.PauseCronJob(ctx, c.User, c.Thread, args.Name, args.Paused)
		return kit.Result{Output: shown(j)}, err
	case DeleteTool:
		args, _ := kit.DecodeArguments[nameArgs](c.Arguments) // the schema checked its fields: it cannot fail
		err := p.jobs.DeleteCronJob(ctx, c.User, c.Thread, args.Name)
		return kit.Result{Output: map[string]string{"deleted": args.Name}}, err
	case ListTool:
		jobs, err := p.jobs.CronJobs(ctx, c.User, c.Thread)
		out := []job{}
		for _, j := range jobs {
			out = append(out, shown(j))
		}
		return kit.Result{Output: map[string]any{"jobs": out}}, err
	}
	return kit.Result{}, fmt.Errorf("tasks: unknown tool %q", c.Tool)
}
