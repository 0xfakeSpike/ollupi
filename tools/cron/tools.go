package cron

import (
	"context"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/jsonschema-go/jsonschema"
)

const (
	ProviderName  = "cron"
	ToolCreateJob = "create_scheduled_job"
	ToolDeleteJob = "delete_scheduled_job"
	ToolPauseJob  = "pause_scheduled_job"
	ToolListJobs  = "list_scheduled_jobs"
)

func (j *Jobs) Name() (string, string) {
	return ProviderName, "Scheduled tasks: messages sent to this conversation later, on a cron schedule."
}

type CreateArgs struct {
	Name   string `json:"name" jsonschema:"Short task name, unique in this conversation, e.g. daily-market-report."`
	Spec   string `json:"spec" jsonschema:"5-field cron expression (minute hour day-of-month month day-of-week), or @hourly/@daily/@weekly/@monthly/@yearly."`
	Prompt string `json:"prompt" jsonschema:"The message sent at each run, as the user would write it."`
}

func (CreateArgs) Constrain(s *jsonschema.Schema) {
	s.Properties["name"].Pattern = namePattern.String()
	s.Properties["prompt"].MinLength, s.Properties["prompt"].MaxLength = jsonschema.Ptr(1), jsonschema.Ptr(maxPromptRunes)
}

type PauseArgs struct {
	Name   string `json:"name"`
	Paused bool   `json:"paused" jsonschema:"true to pause, false to resume."`
}

type NameArgs struct {
	Name string `json:"name"`
}

type ListArgs struct{}

type Deleted struct {
	ID   string `json:"id"`
	Name string `json:"deleted"`
}

type JobList struct {
	Jobs []repo.CronJobView `json:"jobs"`
}

func (j *Jobs) ToolList(context.Context) ([]*tools.Spec, error) {
	return []*tools.Spec{
		{
			Name:        ToolCreateJob,
			Annotations: &tools.Annotations{DestructiveHint: tools.Hint(false), OpenWorldHint: tools.Hint(false)},
			Description: fmt.Sprintf("Schedule a task in this conversation: at each time the cron schedule names (timezone %s, now %s), "+
				"prompt is sent here as a new message and you work on it with this conversation's history, project, and settings. "+
				"If a turn is still running then, the message waits as the next turn.",
				j.location, time.Now().In(j.location).Format("2006-01-02 15:04 -07:00")),
			InputSchema: tools.InputSchema[CreateArgs](),
		},
		{
			Name:        ToolPauseJob,
			Description: "Pause or resume a scheduled task of this conversation. A resumed task runs next at its next scheduled time.",
			Annotations: &tools.Annotations{DestructiveHint: tools.Hint(false), OpenWorldHint: tools.Hint(false)},
			InputSchema: tools.InputSchema[PauseArgs](),
		},
		{
			Name:        ToolDeleteJob,
			Description: "Delete a scheduled task of this conversation by name.",
			Annotations: &tools.Annotations{DestructiveHint: tools.Hint(true), OpenWorldHint: tools.Hint(false)},
			InputSchema: tools.InputSchema[NameArgs](),
		},
		{
			Name:        ToolListJobs,
			Description: "List the scheduled tasks of this conversation with their next run time.",
			Annotations: &tools.Annotations{ReadOnlyHint: true, OpenWorldHint: tools.Hint(false)},
			InputSchema: tools.InputSchema[ListArgs](),
		},
	}, nil
}

func (j *Jobs) Invoke(ctx context.Context, tool string, arguments map[string]any) (tools.Result, error) {
	caller := tools.CallerOf(ctx)
	switch tool {
	case ToolCreateJob:
		if caller.ThreadID == "" {
			return tools.Result{}, fmt.Errorf("cron: a task is created in a conversation")
		}
		if caller.Subagent {
			return tools.Result{}, fmt.Errorf("cron: a subagent cannot schedule tasks; report what should be scheduled instead")
		}
		args, err := tools.DecodeArguments[CreateArgs](arguments)
		if err != nil {
			return tools.Result{}, err
		}
		job, err := j.Create(caller.ThreadID, args.Name, args.Spec, args.Prompt)
		if err != nil {
			return tools.Result{}, err
		}
		return j.result(job.ID)
	case ToolPauseJob:
		args, err := tools.DecodeArguments[PauseArgs](arguments)
		if err != nil {
			return tools.Result{}, err
		}
		job, err := j.SetPaused(caller.ThreadID, args.Name, args.Paused)
		if err != nil {
			return tools.Result{}, err
		}
		return j.result(job.ID)
	case ToolDeleteJob:
		args, err := tools.DecodeArguments[NameArgs](arguments)
		if err != nil {
			return tools.Result{}, err
		}
		job, err := j.Delete(caller.ThreadID, args.Name)
		if err != nil {
			return tools.Result{}, err
		}
		return tools.Result{Output: Deleted{ID: job.ID, Name: job.Name}}, nil
	case ToolListJobs:
		jobs, err := j.List(caller.ThreadID)
		if err != nil {
			return tools.Result{}, err
		}
		if jobs == nil {
			jobs = []repo.CronJobView{}
		}
		return tools.Result{Output: JobList{Jobs: jobs}}, nil
	default:
		return tools.Result{}, fmt.Errorf("cron: unknown tool %q", tool)
	}
}

func (j *Jobs) result(id string) (tools.Result, error) {
	job, err := j.Job(id)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Output: job}, nil
}
