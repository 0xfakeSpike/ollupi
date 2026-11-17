package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools/cron"
	"github.com/google/uuid"
)

func (sv *Service) StartThread(user, workspaceID, environmentID, title string) (db.ThreadRow, error) {
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
	return sv.createThread(db.ThreadRow{UserID: user, Title: title, WorkspaceID: db.Ref(workspaceID), EnvironmentID: db.Ref(env.ID)})
}

func (sv *Service) createThread(t db.ThreadRow) (db.ThreadRow, error) {
	now := time.Now().UTC()
	t.ID = uuid.NewString()
	t.CreatedAt, t.UpdatedAt = now, now
	if err := sv.cfg.DB.CreateThread(t); err != nil {
		return db.ThreadRow{}, fmt.Errorf("service: create: %w", err)
	}
	return t, nil
}

func (sv *Service) ListThreads(user string, f db.ThreadFilter) ([]db.ThreadSummary, bool, error) {
	rows, more, err := sv.cfg.DB.ThreadRows(user, f)
	if err != nil {
		return nil, false, err
	}
	out := make([]db.ThreadSummary, 0, len(rows))
	for _, r := range rows {
		status, err := sv.cfg.DB.ThreadStatus(r.ID)
		if err != nil {
			return nil, false, fmt.Errorf("service: %w", err)
		}
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
	sv.ev.Publish(event.Event{Kind: event.ThreadChanged, User: t.UserID, Thread: t})
	return t, nil
}

func (sv *Service) MarkThreadRead(user, id string) (db.ThreadRow, error) {
	if _, err := sv.topThread(user, id); err != nil {
		return db.ThreadRow{}, err
	}
	t, err := sv.cfg.DB.MarkThreadRead(user, id, time.Now())
	if err != nil {
		return db.ThreadRow{}, fmt.Errorf("service: thread %s: %w", id, err)
	}
	sv.ev.Publish(event.Event{Kind: event.ThreadChanged, User: t.UserID, Thread: t})
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
	sv.ev.Publish(event.Event{Kind: event.ThreadChanged, User: t.UserID, Thread: t})
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
	jobs, err := sv.cfg.DB.CronJobViews(user, id)
	if err != nil {
		return fmt.Errorf("service: thread %s: %w", id, err)
	}
	if err := sv.cfg.DB.DeleteThread(user, id); err != nil {
		return fmt.Errorf("service: thread %s: %w", id, err)
	}
	sv.ev.Publish(event.Event{Kind: event.ThreadDeleted, User: t.UserID, Thread: t})
	sv.emitCronJobs(t, jobs)
	if sv.cfg.Attachments == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), attachmentTimeout)
	defer cancel()
	sv.cfg.Attachments.DeletePrefix(ctx, id)
	return nil
}

// Subagents cannot schedule tasks, so a thread's own tasks are all of them.
func (sv *Service) emitCronJobs(t db.ThreadRow, jobs []db.CronJobView) {
	for _, j := range jobs {
		cron.Announce(sv.cfg.DB, sv.ev, t, j.ID)
	}
}

func threadTitle(title string, required bool) (string, error) {
	title = strings.TrimSpace(title)
	if (required && title == "") || len([]rune(title)) > maxTitleRunes {
		return "", fmt.Errorf("service: %w: a title must be 1 to %d characters", ErrInvalidName, maxTitleRunes)
	}
	return title, nil
}

// Another user's thread is refused as though it did not exist.
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
