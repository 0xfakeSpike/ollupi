package agent

import (
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
	base := a.environmentOf(repo.ThreadRow{WorkspaceID: repo.Ref(workspaceID)}).settings
	if err := a.validateLayer(base, settings); err != nil {
		return repo.ThreadRow{}, err
	}
	return a.createThread(repo.ThreadRow{Settings: settings, WorkspaceID: repo.Ref(workspaceID)})
}

func (a *Agent) UpdateThreadSettings(threadID string, settings permission.Settings) (repo.ThreadRow, error) {
	t, err := a.DB.ThreadRow(threadID)
	if err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	if t.ParentThreadID != "" {
		return repo.ThreadRow{}, ErrSubagentThread
	}
	base := a.environmentOf(repo.ThreadRow{WorkspaceID: t.WorkspaceID}).settings
	if err := a.validateLayer(base, settings); err != nil {
		return repo.ThreadRow{}, err
	}
	t, err = a.DB.SetThreadSettings(threadID, settings)
	if err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: thread %s: %w", threadID, err)
	}
	return t, nil
}

func (a *Agent) createThread(t repo.ThreadRow) (repo.ThreadRow, error) {
	now := time.Now().UTC()
	t.ID = uuid.NewString()
	t.CreatedAt, t.UpdatedAt = now, now
	if err := a.DB.CreateThread(t); err != nil {
		return repo.ThreadRow{}, fmt.Errorf("agent: create: %w", err)
	}
	return t, nil
}

func (a *Agent) ReadThread(id string) (repo.ThreadView, error) {
	v, err := a.DB.Thread(id)
	if err != nil {
		return repo.ThreadView{}, fmt.Errorf("agent: thread %s: %w", id, err)
	}
	return v, nil
}

const pageBytes = 4 << 20

func (a *Agent) ReadThreadPage(id string, before, limit int) (repo.ThreadView, bool, error) {
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
	n := 512 + len(it.Text) + len(it.Arguments) + len(it.Error)
	if it.Result != nil {
		n += len(it.Result.Output)
	}
	return n
}

func (a *Agent) ListThreads(f repo.ThreadFilter) ([]repo.ThreadSummary, error) {
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

func (a *Agent) threadStatus(id string) repo.ThreadStatus {
	v, err := a.ReadThread(id)
	if err != nil {
		return ""
	}
	status := v.Status()
	if status != repo.ThreadActive {
		return status
	}
	for _, call := range v.AgentCalls() {
		if child, err := a.ReadThread(call.Await.ThreadID); err == nil {
			if cs := child.Status(); cs == repo.ThreadWaitingOnApproval || cs == repo.ThreadWaitingOnUserInput {
				return cs
			}
		}
	}
	return status
}
