package agent

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/google/uuid"
)

func (a *orchestrator) startThread(workspaceID string, settings permission.Settings) (repo.ThreadRow, error) {
	if err := a.checkWorkspace(workspaceID); err != nil {
		return repo.ThreadRow{}, err
	}
	env, err := a.environmentOf(repo.ThreadRow{WorkspaceID: repo.Ref(workspaceID)})
	if err != nil {
		return repo.ThreadRow{}, err
	}
	if err := a.validateLayer(env.settings, settings); err != nil {
		return repo.ThreadRow{}, err
	}
	return a.createThread(repo.ThreadRow{Settings: settings, WorkspaceID: repo.Ref(workspaceID)})
}

func (a *orchestrator) updateThreadSettings(threadID string, settings permission.Settings) (repo.ThreadRow, error) {
	t, err := a.DB.ThreadRow(threadID)
	if err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	if t.ParentThreadID != "" {
		return repo.ThreadRow{}, ErrSubagentThread
	}
	env, err := a.environmentOf(repo.ThreadRow{WorkspaceID: t.WorkspaceID})
	if err != nil {
		return repo.ThreadRow{}, err
	}
	if err := a.validateLayer(env.settings, settings); err != nil {
		return repo.ThreadRow{}, err
	}
	t, err = a.DB.SetThreadSettings(threadID, settings)
	if err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	return t, nil
}

func (a *orchestrator) createThread(t repo.ThreadRow) (repo.ThreadRow, error) {
	now := time.Now().UTC()
	t.ID = uuid.NewString()
	t.CreatedAt, t.UpdatedAt = now, now
	if err := a.DB.CreateThread(t); err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: create: %w", err)
	}
	return t, nil
}

func (a *orchestrator) readThread(id string) (repo.ThreadView, error) {
	v, err := a.DB.Thread(id)
	if err != nil {
		return repo.ThreadView{}, fmt.Errorf("agent: thread %s: %w", id, err)
	}
	return v, nil
}

const pageBytes = 4 << 20

func (a *orchestrator) readThreadPage(id string, before, limit int) (repo.ThreadView, bool, error) {
	v, more, err := a.DB.ThreadPage(id, before, limit)
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

func (a *orchestrator) listThreads(f repo.ThreadFilter) ([]repo.ThreadSummary, error) {
	rows, err := a.DB.ThreadRows(f)
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

func (a *orchestrator) threadStatus(id string) repo.ThreadStatus {
	v, err := a.readThread(id)
	if err != nil {
		return ""
	}
	status := v.Status()
	if status != repo.ThreadActive {
		return status
	}
	for _, call := range v.AgentCalls() {
		if child, err := a.readThread(call.Await.ThreadID); err == nil {
			if cs := child.Status(); cs == repo.ThreadWaitingOnApproval || cs == repo.ThreadWaitingOnUserInput {
				return cs
			}
		}
	}
	return status
}
