package role

import (
	"fmt"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/contextmanager"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/redact"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/google/uuid"
)

// runState is what a role keeps for a run, alike for every role: the deployment's agent and its permission settings,
// the turn and its conversation, the permissions its Permit makes, the redactor that keeps the user's secrets from the
// model, and the usage of the run's model calls.
type runState struct {
	// agent and prompt are the deployment's agent, by its name and the operator's prompt for it; skillsDir is where
	// each role's skills are installed.
	agent, prompt, skillsDir string
	// own is the role's own permission layer, if it has one: what it keeps of the tools, below the user's settings.
	own []permission.Layer
	// core are the built-in tools the role is sent whatever the window holds; none: every built-in tool.
	core []string
	// settings are the deployment's default permission settings, which serve users with different tools when shared;
	// tools, when set, are the only tools a turn may use.
	settings permission.Settings
	shared   bool
	tools    []string
	// memory keeps the users' memory, which the role's runs take in; without it they take in none.
	memory *memory.Manager
	// window is the context window of the turn's model, which the prompt, the tools and the memory are fitted to.
	window *contextmanager.Manager

	turn      db.TurnRow
	thread    db.ThreadRow
	workspace db.WorkspaceRow
	// history are the conversation's items before the turn's, items the turn's own.
	history, items []db.ItemRow

	checker  *permission.Checker
	redactor *redact.Redactor
	usage    db.Usage
}

func (s *runState) start(turn db.TurnRow, conversation []db.ItemRow) {
	s.turn, s.history, s.items = turn, nil, nil
	for _, it := range conversation {
		if it.TurnID == turn.ID {
			s.items = append(s.items, it)
		} else {
			s.history = append(s.history, it)
		}
	}
}

func (s *runState) place(thread db.ThreadRow, workspace db.WorkspaceRow) {
	s.thread, s.workspace = thread, workspace
}

func (s *runState) Turn() db.TurnRow { return s.turn }

func (s *runState) Thread() db.ThreadRow { return s.thread }

func (s *runState) Workspace() db.WorkspaceRow { return s.workspace }

func (s *runState) Items() []db.ItemRow { return s.items }

func (s *runState) Conversation() []db.ItemRow { return slices.Concat(s.history, s.items) }

func (s *runState) Keep(stored db.ItemRow) {
	for i := range s.items {
		if s.items[i].ID == stored.ID {
			s.items[i] = stored
			return
		}
	}
	s.items = append(s.items, stored)
}

func (s *runState) Decide(call permission.Call) permission.Decision {
	return s.checker.Decide(call, s.Conversation())
}

// Load reads the turn from store, its conversation and the conversation's items up to it, and makes the role's
// permissions in it with the tools of registry.
func (s *runState) Load(store *db.DB, registry *tools.Registry, turnID string) error {
	turn, err := store.Turn(turnID)
	if err != nil {
		return fmt.Errorf("role: turn %s: %w", turnID, err)
	}
	thread, err := store.ThreadRow(turn.ThreadID)
	if err != nil {
		return fmt.Errorf("role: thread %s: %w", turn.ThreadID, err)
	}
	items, err := store.RunItems(thread.ID, turn.Seq)
	if err != nil {
		return fmt.Errorf("role: the items of thread %s: %w", thread.ID, err)
	}
	s.start(turn, items)
	return s.permitIn(store, registry, thread)
}

// Reload reads the turn's items again, and makes the role's permissions in its conversation as it is now.
func (s *runState) Reload(store *db.DB, registry *tools.Registry) error {
	items, err := store.TurnItems(s.turn.ID)
	if err != nil {
		return fmt.Errorf("role: reload the turn: %w", err)
	}
	thread, err := store.ThreadRow(s.thread.ID)
	if err != nil {
		return fmt.Errorf("role: thread %s: %w", s.thread.ID, err)
	}
	s.items = items
	return s.permitIn(store, registry, thread)
}

// permitIn makes the role's permissions in the conversation t, with its workspace's, and the conversation's
// settings, or for a subagent its parent's, and places the role there.
func (s *runState) permitIn(store *db.DB, registry *tools.Registry, t db.ThreadRow) error {
	var w db.WorkspaceRow
	if t.WorkspaceID != "" {
		var err error
		if w, err = store.Workspace(string(t.WorkspaceID)); err != nil {
			return fmt.Errorf("role: workspace %s: %w", t.WorkspaceID, err)
		}
	}
	conversation := permission.ThreadLayer(t.Settings)
	if t.ParentThreadID != "" {
		parent, err := store.ThreadRow(string(t.ParentThreadID))
		if err != nil {
			return fmt.Errorf("role: parent thread %s: %w", t.ParentThreadID, err)
		}
		conversation = permission.ParentLayer(parent.Settings)
	}
	if err := s.Permit(w, registry, conversation); err != nil {
		return err
	}
	s.place(t, w)
	return nil
}

func (s *runState) Permit(workspace db.WorkspaceRow, registry *tools.Registry, conversation ...permission.Layer) error {
	layers := slices.Concat(s.own, permission.Base(s.settings, s.shared, workspace), conversation)
	var allowlist []string
	if s.tools != nil {
		allowlist = []string{}
		for _, d := range registry.Descriptors() {
			if slices.Contains(s.tools, d.Name) {
				allowlist = append(allowlist, d.Name)
			}
		}
	}
	checker, err := permission.New(layers, allowlist, registry)
	if err != nil {
		return err
	}
	s.checker = checker
	return nil
}

// Recall is the memory snapshot the run takes in now, as an item to record: none when the role takes in no memory,
// the user has none, or the conversation took one in since its latest compaction.
func (s *runState) Recall() (*db.ItemRow, error) {
	if s.memory == nil || hasSnapshot(s.Conversation()) {
		return nil, nil
	}
	snapshot, err := s.memory.Snapshot(s.thread.UserID, string(s.thread.WorkspaceID), s.window.MemoryBudget())
	if err != nil || snapshot == nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &db.ItemRow{
		ID: uuid.NewString(), TurnID: s.turn.ID, Type: db.ItemMemoryContext, Status: db.ItemCompleted,
		Memory: snapshot.Entries, Text: snapshot.Text, StartedAt: now, CompletedAt: &now,
	}, nil
}

func (s *runState) Window() *contextmanager.Manager { return s.window }

func (s *runState) Offer() (offered, deferred []tools.ToolDescriptor) {
	return offer(s.checker.Tools(), s.window, s.Conversation(), s.core)
}

func (s *runState) Tool(name string) (tools.ToolDescriptor, bool) {
	offered, _ := s.Offer()
	i := slices.IndexFunc(offered, func(d tools.ToolDescriptor) bool { return d.Name == name })
	if i < 0 {
		return tools.ToolDescriptor{}, false
	}
	return offered[i], true
}

// hasSnapshot says whether the items took in a memory snapshot since their latest compaction.
func hasSnapshot(items []db.ItemRow) bool {
	for i := len(items) - 1; i >= 0; i-- {
		switch it := items[i]; {
		case it.Type == db.ItemMemoryContext && it.Status == db.ItemCompleted:
			return true
		case it.Type == db.ItemContextCompaction && it.Status == db.ItemCompleted:
			return false
		}
	}
	return false
}

func (s *runState) Redact(key string) { s.redactor = redact.New(key) }

func (s *runState) Mask(text string) string { return s.redactor.Mask(text) }

func (s *runState) Restore(text string) string { return s.redactor.Restore(text) }

func (s *runState) RestoreJSON(text string) string { return s.redactor.RestoreJSON(text) }

func (s *runState) Stream() *redact.Stream { return s.redactor.Stream() }

func (s *runState) AddUsage(u db.Usage) { s.usage.Add(u) }

func (s *runState) Usage() db.Usage { return s.usage }
