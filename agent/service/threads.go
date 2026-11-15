package service

import (
	"context"
	"fmt"
	"github.com/0xfakeSpike/ollupi/agent/event"
	"log/slog"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/google/uuid"
)

// StartThread starts a conversation in the environment it names, which must be one of the user's: the environment
// on this machine when it names none. Its workspace is a folder in whichever environment it runs in.
func (sv *Service) StartThread(user, workspaceID, environmentID, title string, settings permission.Settings) (db.ThreadRow, error) {
	title, err := threadTitle(title, false)
	if err != nil {
		return db.ThreadRow{}, err
	}
	env, err := sv.environmentFor(user, environmentID)
	if err != nil {
		return db.ThreadRow{}, err
	}
	w, err := sv.workspaceFor(user, db.Ref(workspaceID))
	if err != nil {
		return db.ThreadRow{}, err
	}
	if err := usable(w); err != nil {
		return db.ThreadRow{}, err
	}
	if err := sv.validateLayer(user, permission.Base(sv.cfg.Settings, sv.res.Shared(), w), permission.Settings{}, permission.ThreadLayer(settings)); err != nil {
		return db.ThreadRow{}, err
	}
	return sv.createThread(db.ThreadRow{UserID: user, Title: title, Settings: settings, WorkspaceID: db.Ref(workspaceID), EnvironmentID: db.Ref(env.ID)})
}

func (sv *Service) UpdateThreadSettings(user, threadID string, settings permission.Settings) (db.ThreadRow, error) {
	return sv.updateThreadSettings(user, threadID, func(permission.Settings) permission.Settings { return settings })
}

// updateThreadSettings checks what change makes of the thread's settings as they were read, and writes what it makes
// of them as they are when written.
func (sv *Service) updateThreadSettings(user, threadID string, change func(permission.Settings) permission.Settings) (db.ThreadRow, error) {
	t, err := sv.topThread(user, threadID)
	if err != nil {
		return db.ThreadRow{}, err
	}
	w, err := sv.workspaceOf(t.WorkspaceID)
	if err != nil {
		return db.ThreadRow{}, err
	}
	if err := sv.validateLayer(user, permission.Base(sv.cfg.Settings, sv.res.Shared(), w), t.Settings, permission.ThreadLayer(change(t.Settings))); err != nil {
		return db.ThreadRow{}, err
	}
	t, err = sv.cfg.DB.UpdateThreadSettings(user, threadID, change)
	if err != nil {
		return db.ThreadRow{}, fmt.Errorf("service: thread %s: %w", threadID, err)
	}
	sv.ev.Emit(event.Event{Kind: event.ThreadChanged, Thread: t})
	return t, nil
}

func (sv *Service) createThread(t db.ThreadRow) (db.ThreadRow, error) {
	now := time.Now().UTC()
	t.ID = uuid.NewString()
	t.CreatedAt, t.UpdatedAt = now, now
	if err := sv.cfg.DB.CreateThread(t); err != nil {
		return db.ThreadRow{}, fmt.Errorf("service: create: %w", err)
	}
	sv.ev.Emit(event.Event{Kind: event.ThreadStarted, Thread: t})
	return t, nil
}

func (sv *Service) ListThreads(user string, f db.ThreadFilter) ([]db.ThreadSummary, bool, error) {
	rows, more, err := sv.cfg.DB.ThreadRows(user, f)
	if err != nil {
		return nil, false, err
	}
	out := make([]db.ThreadSummary, 0, len(rows))
	for _, r := range rows {
		status := event.ThreadStatus(sv.cfg.DB, r.ID)
		out = append(out, db.ThreadSummary{ThreadRow: r, Status: status})
	}
	return out, more, nil
}

const maxTitleRunes = 120

func (sv *Service) RenameThread(user, id, title string) (db.ThreadRow, error) {
	title, err := threadTitle(title, true)
	if err != nil {
		return db.ThreadRow{}, err
	}
	if _, err := sv.topThread(user, id); err != nil {
		return db.ThreadRow{}, err
	}
	t, err := sv.cfg.DB.SetThreadTitle(user, id, title)
	if err != nil {
		return db.ThreadRow{}, fmt.Errorf("service: thread %s: %w", id, err)
	}
	sv.ev.Emit(event.Event{Kind: event.ThreadChanged, Thread: t})
	return t, nil
}

// MarkThreadRead records that the user has read a thread as it is now, so it is no longer unread until another turn
// ends. Other clients hear of it as a thread change.
func (sv *Service) MarkThreadRead(user, id string) (db.ThreadRow, error) {
	if _, err := sv.topThread(user, id); err != nil {
		return db.ThreadRow{}, err
	}
	t, err := sv.cfg.DB.MarkThreadRead(user, id, time.Now())
	if err != nil {
		return db.ThreadRow{}, fmt.Errorf("service: thread %s: %w", id, err)
	}
	sv.ev.Emit(event.Event{Kind: event.ThreadChanged, Thread: t})
	return t, nil
}

func (sv *Service) ArchiveThread(user, id string, archived bool) (db.ThreadRow, error) {
	if _, err := sv.topThread(user, id); err != nil {
		return db.ThreadRow{}, err
	}
	var at *time.Time
	if archived {
		if err := sv.cfg.DB.CheckIdle(id); err != nil {
			return db.ThreadRow{}, err
		}
		if err := sv.o.StopBackground(id, "the conversation was archived"); err != nil {
			return db.ThreadRow{}, err
		}
		now := time.Now().UTC()
		at = &now
	}
	jobs, err := sv.cfg.DB.CronJobViews(user, id)
	if err != nil {
		return db.ThreadRow{}, fmt.Errorf("service: thread %s: %w", id, err)
	}
	t, err := sv.cfg.DB.SetThreadArchived(user, id, at)
	if err != nil {
		return db.ThreadRow{}, fmt.Errorf("service: thread %s: %w", id, err)
	}
	sv.ev.Emit(event.Event{Kind: event.ThreadChanged, Thread: t})
	sv.emitCronJobs(t, jobs)
	return t, nil
}

func (sv *Service) DeleteThread(user, id string) error {
	t, err := sv.topThread(user, id)
	if err != nil {
		return err
	}
	if err := sv.cfg.DB.CheckIdle(id); err != nil {
		return err
	}
	if err := sv.o.StopBackground(id, "the conversation was deleted"); err != nil {
		return err
	}
	jobs, err := sv.cfg.DB.CronJobViews(user, id)
	if err != nil {
		return fmt.Errorf("service: thread %s: %w", id, err)
	}
	if err := sv.cfg.DB.DeleteThread(user, id); err != nil {
		return fmt.Errorf("service: thread %s: %w", id, err)
	}
	sv.ev.Emit(event.Event{Kind: event.ThreadDeleted, Thread: t})
	sv.emitCronJobs(t, jobs)
	if sv.cfg.Attachments == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(sv.ctx), attachmentTimeout)
	defer cancel()
	if err := sv.cfg.Attachments.DeletePrefix(ctx, id); err != nil {
		slog.Warn("thread attachments not removed", "thread", id, "err", err)
	}
	return nil
}

// emitCronJobs reports the scheduled tasks of a thread that was archived or deleted. Subagents cannot schedule
// tasks, so a thread's own tasks are all of them.
func (sv *Service) emitCronJobs(t db.ThreadRow, jobs []db.CronJobView) {
	for _, j := range jobs {
		sv.ev.Emit(event.Event{Kind: event.CronJobChanged, Thread: t, CronJobID: j.ID})
	}
}

func threadTitle(title string, required bool) (string, error) {
	title = strings.TrimSpace(title)
	if (required && title == "") || len([]rune(title)) > maxTitleRunes {
		return "", fmt.Errorf("service: %w: a title must be 1 to %d characters", ErrInvalidName, maxTitleRunes)
	}
	return title, nil
}

// owned refuses a thread that is not the user's as though it did not exist.
func (sv *Service) owned(user, id string) error {
	if _, err := sv.cfg.DB.ThreadOf(user, id); err != nil {
		return fmt.Errorf("service: thread %s: %w", id, err)
	}
	return nil
}

func (sv *Service) topThread(user, id string) (db.ThreadRow, error) {
	t, err := sv.cfg.DB.ThreadOf(user, id)
	if err != nil {
		return db.ThreadRow{}, fmt.Errorf("service: thread %s: %w", id, err)
	}
	if t.ParentThreadID != "" {
		return db.ThreadRow{}, ErrSubagentThread
	}
	return t, nil
}
