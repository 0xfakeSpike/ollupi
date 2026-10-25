package agent

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

func (a *Agent) StartThread(workspaceID string, settings permission.Settings) (repo.ThreadRow, error) {
	if err := a.checkWorkspace(workspaceID); err != nil {
		return repo.ThreadRow{}, err
	}
	w, err := a.workspaceOf(repo.Ref(workspaceID))
	if err != nil {
		return repo.ThreadRow{}, err
	}
	if err := a.validateLayer(permission.Resolve(a.cfg.Settings, w.Settings), settings); err != nil {
		return repo.ThreadRow{}, err
	}
	return a.createThread(repo.ThreadRow{Settings: settings, WorkspaceID: repo.Ref(workspaceID)})
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
	if err := a.validateLayer(permission.Resolve(a.cfg.Settings, w.Settings), settings); err != nil {
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

func (a *Agent) ReadThread(id string) (repo.ThreadView, error) {
	v, err := a.cfg.DB.Thread(id)
	if err != nil {
		return repo.ThreadView{}, fmt.Errorf("agent: thread %s: %w", id, err)
	}
	return v, nil
}

const pageBytes = 4 << 20

func (a *Agent) ReadThreadPage(id string, before, limit int) (repo.ThreadView, bool, error) {
	v, more, err := a.cfg.DB.ThreadPage(id, before, limit)
	if err != nil {
		return repo.ThreadView{}, false, fmt.Errorf("agent: thread %s: %w", id, err)
	}
	size := 0
	for i := len(v.Items) - 1; i >= 0; i-- {
		size += itemBytes(v.Items[i])
		if size > pageBytes && i < len(v.Items)-1 {
			v.Items, more = v.Items[i+1:], true
			break
		}
	}
	return v, more, nil
}

func itemBytes(it repo.ItemRow) int {
	raw, err := json.Marshal(it)
	if err != nil {
		return pageBytes
	}
	return len(raw)
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
