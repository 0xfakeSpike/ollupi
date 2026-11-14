package orchestrator

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/runtime"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

// A turnRun carries out a run of one turn: runtime.Next decides each step from the run's state, and the turnRun does
// it, records what came of it and keeps the state for the next step.
type turnRun struct {
	o        *Orchestrator
	t        repo.ThreadRow
	caller   tools.Caller
	registry *tools.Registry
	client   llm.Client
	settings repo.SettingsRow
	state    runtime.State
}

// run runs the turn until it ends with the final answer, which it reports, or waits.
func (tr *turnRun) run(ctx context.Context) (bool, error) {
	ctx = tools.WithCaller(ctx, tr.caller)
	if slices.ContainsFunc(tr.state.Items, func(it repo.ItemRow) bool { return it.WaitsOnUser() || it.Awaits() }) {
		return false, fmt.Errorf("orchestrator: turn %s still waits for an answer", tr.state.Turn.ID)
	}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		step, err := runtime.Next(tr.state)
		if err != nil {
			return false, err
		}
		switch st := step.(type) {
		case runtime.Wait:
			return false, nil
		case runtime.Done:
			return true, nil
		case runtime.LoadMemory:
			err = tr.loadMemory(st)
		case runtime.Inspect:
			tr.state.Inspections[st.Call.ID], err = tr.inspect(ctx, st)
		case runtime.Calls:
			err = tr.runCalls(ctx, st)
		case runtime.Stop:
			_, err = tr.record(st.Notice)
		case runtime.Compact:
			err = tr.compact(ctx, st)
		case runtime.CallModel:
			if err = tr.respond(ctx, st); err != nil {
				if st.Stopped != "" {
					err = fmt.Errorf("%w (tool use stopped: %s)", err, st.Stopped)
				}
				break
			}
			tr.state.Inspections, tr.state.Prompted = map[string]runtime.Inspection{}, st.Prompted
			err = tr.reload()
		}
		if err != nil {
			return false, err
		}
	}
}

// record stores an item of the turn, tells those who watch the thread, and keeps it in the run's state.
func (tr *turnRun) record(it repo.ItemRow) (repo.ItemRow, error) {
	stored, isNew, err := tr.o.cfg.DB.RecordItem(tr.t.ID, it)
	if err != nil {
		return repo.ItemRow{}, fmt.Errorf("orchestrator: record %s item: %w", it.Type, err)
	}
	tr.o.emitItem(tr.t, tr.state.Turn, stored, isNew)
	items := tr.state.Items
	if i := slices.IndexFunc(items, func(row repo.ItemRow) bool { return row.ID == stored.ID }); i >= 0 {
		items[i] = stored
	} else {
		tr.state.Items = append(items, stored)
	}
	return stored, nil
}

// reload reads the turn's items again, with the permissions of its thread and the settings of its user as they are
// now.
func (tr *turnRun) reload() error {
	items, err := tr.o.cfg.DB.TurnItems(tr.state.Turn.ID)
	if err != nil {
		return fmt.Errorf("orchestrator: reload the turn: %w", err)
	}
	current, err := tr.o.cfg.DB.ThreadRow(tr.t.ID)
	if err != nil {
		return fmt.Errorf("orchestrator: thread %s: %w", tr.t.ID, err)
	}
	checker, _, err := tr.o.permissions(current, tr.state.Role)
	if err != nil {
		return err
	}
	settings, err := tr.o.cfg.DB.Settings(tr.t.UserID)
	if err != nil {
		return err
	}
	tr.state.Items, tr.state.Permissions, tr.settings = items, checker, settings
	return nil
}

// loadMemory records the user's memory snapshot; a deployment that keeps no memory leaves the run without.
func (tr *turnRun) loadMemory(st runtime.LoadMemory) error {
	if tr.o.cfg.Memory == nil {
		tr.state.MemoryOff = true
		return nil
	}
	snapshot, err := tr.o.cfg.Memory.Snapshot(tr.t.UserID, string(tr.t.WorkspaceID), st.Budget)
	if err != nil {
		return fmt.Errorf("orchestrator: load memory: %w", err)
	}
	if snapshot == nil {
		tr.state.MemoryOff = true
		return nil
	}
	now := time.Now().UTC()
	_, err = tr.record(repo.ItemRow{
		ID: uuid.NewString(), TurnID: tr.state.Turn.ID, Type: repo.ItemMemoryContext, Status: repo.ItemCompleted,
		Memory: snapshot.Entries, Text: snapshot.Text, StartedAt: now, CompletedAt: &now,
	})
	return err
}
