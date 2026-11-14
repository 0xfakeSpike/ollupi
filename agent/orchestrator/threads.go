package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

// StartThread starts a conversation in the environment it names, which must be one of the user's: the environment
// on this machine when it names none. Its workspace is a folder in whichever environment it runs in.
func (o *Orchestrator) StartThread(user, workspaceID, environmentID, title string, settings permission.Settings) (repo.ThreadRow, error) {
	title, err := threadTitle(title, false)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	env, err := o.environmentFor(user, environmentID)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	w, err := o.workspaceFor(user, repo.Ref(workspaceID))
	if err != nil {
		return repo.ThreadRow{}, err
	}
	if err := usable(w); err != nil {
		return repo.ThreadRow{}, err
	}
	if err := o.validateLayer(user, o.layers(w), permission.Settings{}, threadLayer(settings)); err != nil {
		return repo.ThreadRow{}, err
	}
	return o.createThread(repo.ThreadRow{UserID: user, Title: title, Settings: settings, WorkspaceID: repo.Ref(workspaceID), EnvironmentID: repo.Ref(env.ID)})
}

func (o *Orchestrator) UpdateThreadSettings(user, threadID string, settings permission.Settings) (repo.ThreadRow, error) {
	t, err := o.topThread(user, threadID)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	w, err := o.workspaceOf(t.WorkspaceID)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	if err := o.validateLayer(user, o.layers(w), t.Settings, threadLayer(settings)); err != nil {
		return repo.ThreadRow{}, err
	}
	t, err = o.cfg.DB.SetThreadSettings(user, threadID, settings)
	if err != nil {
		return repo.ThreadRow{}, fmt.Errorf("orchestrator: thread %s: %w", threadID, err)
	}
	o.emit(Event{Kind: EventThreadChanged, Thread: t})
	return t, nil
}

func (o *Orchestrator) createThread(t repo.ThreadRow) (repo.ThreadRow, error) {
	now := time.Now().UTC()
	t.ID = uuid.NewString()
	t.CreatedAt, t.UpdatedAt = now, now
	if err := o.cfg.DB.CreateThread(t); err != nil {
		return repo.ThreadRow{}, fmt.Errorf("orchestrator: create: %w", err)
	}
	o.emit(Event{Kind: EventThreadStarted, Thread: t})
	return t, nil
}

func (o *Orchestrator) ListThreads(user string, f repo.ThreadFilter) ([]repo.ThreadSummary, bool, error) {
	rows, more, err := o.cfg.DB.ThreadRows(user, f)
	if err != nil {
		return nil, false, err
	}
	out := make([]repo.ThreadSummary, 0, len(rows))
	for _, r := range rows {
		status := o.threadStatus(r.ID)
		out = append(out, repo.ThreadSummary{ThreadRow: r, Status: status})
	}
	return out, more, nil
}

// threadStatus reads a conversation's status off its current turn: waiting on a person when the turn is, or when
// a subagent it waits on is, and otherwise active or idle.
func (o *Orchestrator) threadStatus(id string) repo.ThreadStatus {
	turn, err := o.cfg.DB.ActiveTurn(id)
	if errors.Is(err, repo.ErrNotFound) {
		return repo.ThreadIdle
	}
	if err != nil {
		return ""
	}
	switch turn.Status {
	case repo.TurnWaitingOnApproval:
		return repo.ThreadWaitingOnApproval
	case repo.TurnWaitingOnUserInput:
		return repo.ThreadWaitingOnUserInput
	case repo.TurnWaitingOnAgent:
		st, err := o.cfg.DB.ThreadState(id)
		if err != nil {
			return ""
		}
		for _, call := range st.AgentCalls() {
			if cs := o.threadStatus(call.Await.Handle); cs == repo.ThreadWaitingOnApproval || cs == repo.ThreadWaitingOnUserInput {
				return cs
			}
		}
	}
	return repo.ThreadActive
}

const maxTitleRunes = 120

func (o *Orchestrator) RenameThread(user, id, title string) (repo.ThreadRow, error) {
	title, err := threadTitle(title, true)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	var t repo.ThreadRow
	err = o.call(func() error {
		if _, err := o.topThread(user, id); err != nil {
			return err
		}
		row, err := o.cfg.DB.SetThreadTitle(user, id, title)
		if err != nil {
			return fmt.Errorf("orchestrator: thread %s: %w", id, err)
		}
		t = row
		o.emit(Event{Kind: EventThreadChanged, Thread: t})
		return nil
	})
	return t, err
}

// MarkThreadRead records that the user has read a thread as it is now, so it is no longer unread until another turn
// ends. Other clients hear of it as a thread change.
func (o *Orchestrator) MarkThreadRead(user, id string) (repo.ThreadRow, error) {
	var t repo.ThreadRow
	err := o.call(func() error {
		if _, err := o.topThread(user, id); err != nil {
			return err
		}
		row, err := o.cfg.DB.MarkThreadRead(user, id, time.Now())
		if err != nil {
			return fmt.Errorf("orchestrator: thread %s: %w", id, err)
		}
		t = row
		o.emit(Event{Kind: EventThreadChanged, Thread: t})
		return nil
	})
	return t, err
}

func (o *Orchestrator) ArchiveThread(user, id string, archived bool) (repo.ThreadRow, error) {
	var t repo.ThreadRow
	err := o.call(func() error {
		if _, err := o.topThread(user, id); err != nil {
			return err
		}
		var at *time.Time
		if archived {
			if err := o.idle(id); err != nil {
				return err
			}
			if err := o.cancelBackgroundOf(id, "the conversation was archived"); err != nil {
				return err
			}
			now := time.Now().UTC()
			at = &now
		}
		jobs, err := o.cfg.DB.CronJobViews(user, id)
		if err != nil {
			return fmt.Errorf("orchestrator: thread %s: %w", id, err)
		}
		row, err := o.cfg.DB.SetThreadArchived(user, id, at)
		if err != nil {
			return fmt.Errorf("orchestrator: thread %s: %w", id, err)
		}
		t = row
		o.emit(Event{Kind: EventThreadChanged, Thread: t})
		o.emitCronJobs(t, jobs)
		return nil
	})
	return t, err
}

func (o *Orchestrator) DeleteThread(user, id string) error {
	err := o.call(func() error {
		t, err := o.topThread(user, id)
		if err != nil {
			return err
		}
		if err := o.idle(id); err != nil {
			return err
		}
		if err := o.cancelBackgroundOf(id, "the conversation was deleted"); err != nil {
			return err
		}
		jobs, err := o.cfg.DB.CronJobViews(user, id)
		if err != nil {
			return fmt.Errorf("orchestrator: thread %s: %w", id, err)
		}
		if err := o.cfg.DB.DeleteThread(user, id); err != nil {
			return fmt.Errorf("orchestrator: thread %s: %w", id, err)
		}
		o.emit(Event{Kind: EventThreadDeleted, Thread: t})
		o.emitCronJobs(t, jobs)
		return nil
	})
	if err != nil || o.cfg.Attachments == nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(o.ctx), attachmentTimeout)
	defer cancel()
	if err := o.cfg.Attachments.DeletePrefix(ctx, id); err != nil {
		slog.Warn("thread attachments not removed", "thread", id, "err", err)
	}
	return nil
}

// emitCronJobs reports the scheduled tasks of a thread that was archived or deleted. Subagents cannot schedule
// tasks, so a thread's own tasks are all of them.
func (o *Orchestrator) emitCronJobs(t repo.ThreadRow, jobs []repo.CronJobView) {
	for _, j := range jobs {
		o.emit(Event{Kind: EventCronJobChanged, Thread: t, CronJobID: j.ID})
	}
}

func threadTitle(title string, required bool) (string, error) {
	title = strings.TrimSpace(title)
	if (required && title == "") || len([]rune(title)) > maxTitleRunes {
		return "", fmt.Errorf("orchestrator: %w: a title must be 1 to %d characters", ErrInvalidName, maxTitleRunes)
	}
	return title, nil
}

// owned refuses a thread that is not the user's as though it did not exist.
func (o *Orchestrator) owned(user, id string) error {
	if _, err := o.cfg.DB.ThreadOf(user, id); err != nil {
		return fmt.Errorf("orchestrator: thread %s: %w", id, err)
	}
	return nil
}

func (o *Orchestrator) topThread(user, id string) (repo.ThreadRow, error) {
	t, err := o.cfg.DB.ThreadOf(user, id)
	if err != nil {
		return repo.ThreadRow{}, fmt.Errorf("orchestrator: thread %s: %w", id, err)
	}
	if t.ParentThreadID != "" {
		return repo.ThreadRow{}, ErrSubagentThread
	}
	return t, nil
}

func (o *Orchestrator) idle(id string) error {
	st, err := o.cfg.DB.ThreadState(id)
	if err != nil {
		return fmt.Errorf("orchestrator: thread %s: %w", id, err)
	}
	if _, active := st.ActiveTurn(); active || len(st.QueuedTurns()) > 0 {
		return ErrThreadBusy
	}
	return nil
}
