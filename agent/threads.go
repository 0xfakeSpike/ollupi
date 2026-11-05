package agent

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

func (a *Agent) StartThread(user, workspaceID, title string, settings permission.Settings) (repo.ThreadRow, error) {
	title, err := threadTitle(title, false)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	w, err := a.workspaceFor(user, repo.Ref(workspaceID))
	if err != nil {
		return repo.ThreadRow{}, err
	}
	if err := usable(w); err != nil {
		return repo.ThreadRow{}, err
	}
	if err := a.validateLayer(user, a.layers(w), permission.Settings{}, threadLayer(settings)); err != nil {
		return repo.ThreadRow{}, err
	}
	return a.createThread(repo.ThreadRow{UserID: user, Title: title, Settings: settings, WorkspaceID: repo.Ref(workspaceID)})
}

func (a *Agent) UpdateThreadSettings(user, threadID string, settings permission.Settings) (repo.ThreadRow, error) {
	t, err := a.topThread(user, threadID)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	w, err := a.workspaceOf(t.WorkspaceID)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	if err := a.validateLayer(user, a.layers(w), t.Settings, threadLayer(settings)); err != nil {
		return repo.ThreadRow{}, err
	}
	t, err = a.cfg.DB.SetThreadSettings(threadID, settings)
	if err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	a.emit(Event{Kind: EventThreadChanged, Thread: t})
	return t, nil
}

func (a *Agent) createThread(t repo.ThreadRow) (repo.ThreadRow, error) {
	now := time.Now().UTC()
	t.ID = uuid.NewString()
	t.CreatedAt, t.UpdatedAt = now, now
	if err := a.cfg.DB.CreateThread(t); err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: create: %w", err)
	}
	a.emit(Event{Kind: EventThreadStarted, Thread: t})
	return t, nil
}

func (a *Agent) ListThreads(user string, f repo.ThreadFilter) ([]repo.ThreadSummary, bool, error) {
	rows, more, err := a.cfg.DB.ThreadRows(user, f)
	if err != nil {
		return nil, false, err
	}
	out := make([]repo.ThreadSummary, 0, len(rows))
	for _, r := range rows {
		status := a.threadStatus(r.ID)
		out = append(out, repo.ThreadSummary{ThreadRow: r, Status: status})
	}
	return out, more, nil
}

// threadStatus reads a conversation's status off its current turn: waiting on a person when the turn is, or when
// a subagent it waits on is, and otherwise active or idle.
func (a *Agent) threadStatus(id string) repo.ThreadStatus {
	turn, err := a.cfg.DB.ActiveTurn(id)
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
		st, err := a.cfg.DB.ThreadState(id)
		if err != nil {
			return ""
		}
		for _, call := range st.AgentCalls() {
			if cs := a.threadStatus(call.Await.ThreadID); cs == repo.ThreadWaitingOnApproval || cs == repo.ThreadWaitingOnUserInput {
				return cs
			}
		}
	}
	return repo.ThreadActive
}

const maxTitleRunes = 120

func (a *Agent) RenameThread(user, id, title string) (repo.ThreadRow, error) {
	title, err := threadTitle(title, true)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	var t repo.ThreadRow
	err = a.call(func() error {
		if _, err := a.topThread(user, id); err != nil {
			return err
		}
		row, err := a.cfg.DB.SetThreadTitle(id, title)
		if err != nil {
			return fmt.Errorf("agent: thread %s: %w", id, err)
		}
		t = row
		a.emit(Event{Kind: EventThreadChanged, Thread: t})
		return nil
	})
	return t, err
}

func (a *Agent) ArchiveThread(user, id string, archived bool) (repo.ThreadRow, error) {
	var t repo.ThreadRow
	err := a.call(func() error {
		if _, err := a.topThread(user, id); err != nil {
			return err
		}
		var at *time.Time
		if archived {
			if err := a.idle(id); err != nil {
				return err
			}
			now := time.Now().UTC()
			at = &now
		}
		jobs, err := a.cfg.DB.CronJobViews(user, id)
		if err != nil {
			return fmt.Errorf("agent: thread %s: %w", id, err)
		}
		row, err := a.cfg.DB.SetThreadArchived(id, at)
		if err != nil {
			return fmt.Errorf("agent: thread %s: %w", id, err)
		}
		t = row
		a.emit(Event{Kind: EventThreadChanged, Thread: t})
		a.emitCronJobs(t, jobs)
		return nil
	})
	return t, err
}

func (a *Agent) DeleteThread(user, id string) error {
	return a.call(func() error {
		t, err := a.topThread(user, id)
		if err != nil {
			return err
		}
		if err := a.idle(id); err != nil {
			return err
		}
		jobs, err := a.cfg.DB.CronJobViews(user, id)
		if err != nil {
			return fmt.Errorf("agent: thread %s: %w", id, err)
		}
		if err := a.cfg.DB.DeleteThread(id); err != nil {
			return fmt.Errorf("agent: thread %s: %w", id, err)
		}
		if a.cfg.Attachments != nil {
			if err := a.cfg.Attachments.DeletePrefix(a.ctx, id); err != nil {
				slog.Warn("thread attachments not removed", "thread", id, "err", err)
			}
		}
		a.emit(Event{Kind: EventThreadDeleted, Thread: t})
		a.emitCronJobs(t, jobs)
		return nil
	})
}

// emitCronJobs reports the scheduled tasks of a thread that was archived or deleted. Subagents cannot schedule
// tasks, so a thread's own tasks are all of them.
func (a *Agent) emitCronJobs(t repo.ThreadRow, jobs []repo.CronJobView) {
	for _, j := range jobs {
		a.emit(Event{Kind: EventCronJobChanged, Thread: t, CronJobID: j.ID})
	}
}

func threadTitle(title string, required bool) (string, error) {
	title = strings.TrimSpace(title)
	if (required && title == "") || len([]rune(title)) > maxTitleRunes {
		return "", fmt.Errorf("agent: %w: a title must be 1 to %d characters", ErrInvalidName, maxTitleRunes)
	}
	return title, nil
}

// owned refuses a thread that is not the user's as though it did not exist.
func (a *Agent) owned(user, id string) error {
	if _, err := a.cfg.DB.ThreadOf(user, id); err != nil {
		return fmt.Errorf("agent: thread %s: %w", id, err)
	}
	return nil
}

func (a *Agent) topThread(user, id string) (repo.ThreadRow, error) {
	t, err := a.cfg.DB.ThreadOf(user, id)
	if err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: thread %s: %w", id, err)
	}
	if t.ParentThreadID != "" {
		return repo.ThreadRow{}, ErrSubagentThread
	}
	return t, nil
}

func (a *Agent) idle(id string) error {
	st, err := a.cfg.DB.ThreadState(id)
	if err != nil {
		return fmt.Errorf("agent: thread %s: %w", id, err)
	}
	if _, active := st.ActiveTurn(); active || len(st.QueuedTurns()) > 0 {
		return ErrThreadBusy
	}
	return nil
}
