package agent

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

func (a *Agent) StartThread(workspaceID, title string, settings permission.Settings) (repo.ThreadRow, error) {
	title, err := threadTitle(title, false)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	if err := a.checkWorkspace(workspaceID); err != nil {
		return repo.ThreadRow{}, err
	}
	w, err := a.workspaceOf(repo.Ref(workspaceID))
	if err != nil {
		return repo.ThreadRow{}, err
	}
	layer := permission.Layer{Of: "this conversation's permission settings", Settings: settings}
	if err := a.validateLayer(a.layers(w), permission.Settings{}, layer); err != nil {
		return repo.ThreadRow{}, err
	}
	return a.createThread(repo.ThreadRow{Title: title, Settings: settings, WorkspaceID: repo.Ref(workspaceID)})
}

func (a *Agent) UpdateThreadSettings(threadID string, settings permission.Settings) (repo.ThreadRow, error) {
	t, err := a.cfg.DB.ThreadRow(threadID)
	if err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	if t.ParentThreadID != "" {
		return repo.ThreadRow{}, ErrSubagentThread
	}
	w, err := a.workspaceOf(t.WorkspaceID)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	layer := permission.Layer{Of: "this conversation's permission settings", Settings: settings}
	if err := a.validateLayer(a.layers(w), t.Settings, layer); err != nil {
		return repo.ThreadRow{}, err
	}
	t, err = a.cfg.DB.SetThreadSettings(threadID, settings)
	if err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	return t, nil
}

func (a *Agent) createThread(t repo.ThreadRow) (repo.ThreadRow, error) {
	now := time.Now().UTC()
	t.ID = uuid.NewString()
	t.CreatedAt, t.UpdatedAt = now, now
	if err := a.cfg.DB.CreateThread(t); err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: create: %w", err)
	}
	return t, nil
}

func (a *Agent) ReadThreadPage(id string, before, limit int) (repo.ThreadView, bool, error) {
	v, more, err := a.cfg.DB.ThreadPage(id, before, limit)
	if err != nil {
		return repo.ThreadView{}, false, fmt.Errorf("agent: thread %s: %w", id, err)
	}
	return v, more, nil
}

func (a *Agent) ListThreads(f repo.ThreadFilter) ([]repo.ThreadSummary, error) {
	rows, err := a.cfg.DB.ThreadRows(f)
	if err != nil {
		return nil, err
	}
	out := make([]repo.ThreadSummary, 0, len(rows))
	for _, r := range rows {
		status := a.threadStatus(r.ID)
		out = append(out, repo.ThreadSummary{ThreadRow: r, Status: status})
	}
	return out, nil
}

func (a *Agent) threadStatus(id string) repo.ThreadStatus {
	st, err := a.cfg.DB.ThreadState(id)
	if err != nil {
		return ""
	}
	status := st.Status()
	if status != repo.ThreadActive {
		return status
	}
	for _, call := range st.AgentCalls() {
		if child, err := a.cfg.DB.ThreadState(call.Await.ThreadID); err == nil {
			if cs := child.Status(); cs == repo.ThreadWaitingOnApproval || cs == repo.ThreadWaitingOnUserInput {
				return cs
			}
		}
	}
	return status
}

const maxTitleRunes = 120

func (a *Agent) RenameThread(id, title string) (repo.ThreadRow, error) {
	title, err := threadTitle(title, true)
	if err != nil {
		return repo.ThreadRow{}, err
	}
	var t repo.ThreadRow
	err = a.call(func() error {
		if _, err := a.topThread(id); err != nil {
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

func (a *Agent) ArchiveThread(id string, archived bool) (repo.ThreadRow, error) {
	var t repo.ThreadRow
	err := a.call(func() error {
		if _, err := a.topThread(id); err != nil {
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
		row, err := a.cfg.DB.SetThreadArchived(id, at)
		if err != nil {
			return fmt.Errorf("agent: thread %s: %w", id, err)
		}
		t = row
		a.emit(Event{Kind: EventThreadChanged, Thread: t})
		return nil
	})
	return t, err
}

func (a *Agent) DeleteThread(id string) error {
	return a.call(func() error {
		t, err := a.topThread(id)
		if err != nil {
			return err
		}
		if err := a.idle(id); err != nil {
			return err
		}
		if err := a.cfg.DB.DeleteThread(id); err != nil {
			return fmt.Errorf("agent: thread %s: %w", id, err)
		}
		if a.cfg.AttachmentsDir != "" {
			if err := os.RemoveAll(filepath.Join(a.cfg.AttachmentsDir, id)); err != nil {
				log.Printf("agent: thread %s: remove its attachments: %v", id, err)
			}
		}
		a.emit(Event{Kind: EventThreadDeleted, Thread: t})
		return nil
	})
}

func threadTitle(title string, required bool) (string, error) {
	title = strings.TrimSpace(title)
	if (required && title == "") || len([]rune(title)) > maxTitleRunes {
		return "", fmt.Errorf("agent: %w: a title must be 1 to %d characters", ErrInvalidName, maxTitleRunes)
	}
	return title, nil
}

func (a *Agent) topThread(id string) (repo.ThreadRow, error) {
	t, err := a.cfg.DB.ThreadRow(id)
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

func (a *Agent) Search(terms []string, workspaceID string, limit int) (repo.Found, error) {
	return a.cfg.DB.Search(terms, workspaceID, limit)
}
