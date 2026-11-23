package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/ask"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/sandbox"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/store/sqlite"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/google/uuid"
)

type models struct{}

func (models) Models(string) []llm.Model {
	return []llm.Model{{Name: "big", Connection: "main", Images: true}, {Name: "small", Connection: "text"}}
}
func (models) Limits(string, llm.Model) (llm.Limits, error) {
	return llm.Limits{Context: 200_000, Output: 8_000}, nil
}

type set struct{}

func (set) Specs() []tool.Spec {
	return []tool.Spec{{Name: agent.RunCommandTool, Provider: agent.WorkspaceProvider, Content: true},
		{Name: agent.StartConversationTool, Provider: agent.HubProvider}}
}
func (set) Unavailable() []string                                      { return nil }
func (set) Content(context.Context, tool.Call) (policy.Content, error) { return policy.Content{}, nil }
func (set) Prepare(context.Context, tool.Call) (tool.Plan, error)      { return tool.Plan{}, nil }
func (set) Invoke(context.Context, tool.Call) tool.Result              { return tool.Result{} }

type plainSet struct{ set }

func (plainSet) Specs() []tool.Spec {
	return []tool.Spec{{Name: agent.RunCommandTool, Provider: agent.WorkspaceProvider, Content: true}}
}

func tools(context.Context, string) (tool.Set, error) { return set{}, nil }
func here(string) (sandbox.Box, error)                { return sandbox.Here() }

type blobs struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (b *blobs) Put(_ context.Context, key string, data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.files[key] = data
	return nil
}
func (b *blobs) Get(_ context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.files[key], nil
}
func (b *blobs) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.files, key)
	return nil
}
func (b *blobs) DeletePrefix(_ context.Context, prefix string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k := range b.files {
		if strings.HasPrefix(k, prefix) {
			delete(b.files, k)
		}
	}
	return nil
}

type fixture struct {
	t     *testing.T
	ctx   context.Context
	st    store.Store
	a     *App
	blobs *blobs
}

func setup(t *testing.T) *fixture {
	t.Helper()
	st, err := sqlite.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b := &blobs{files: map[string][]byte{}}
	f := &fixture{t: t, ctx: context.Background(), st: st, blobs: b,
		a: New(st, models{}, tools, noStatuses, here, b, cron.NewCalendar(time.UTC), policy.Settings{ApprovalPolicy: string(policy.OnRequest)})}
	if err := f.a.EnsureUser(f.ctx, "u"); err != nil {
		t.Fatal(err)
	}
	return f
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

func sent(t *testing.T) func(conversation.Turn, bool, error) conversation.Turn {
	return func(turn conversation.Turn, _ bool, err error) conversation.Turn {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return turn
	}
}

func (f *fixture) view(id string) conversation.View {
	f.t.Helper()
	var v conversation.View
	if err := f.st.Read(f.ctx, func(r store.Reader) (err error) { v, err = r.View(id); return err }); err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *fixture) waitOnApproval(id string, turn conversation.Turn) conversation.Item {
	f.t.Helper()
	item := uuid.NewString()
	err := f.st.Update(f.ctx, func(tx store.Tx) error {
		_, err := tx.Append(id,
			conversation.Entry{Turn: turn.ID, Body: conversation.ModelResponded{Response: "r"}},
			conversation.Entry{Turn: turn.ID, Item: item, Body: conversation.ToolCall{Tool: agent.RunCommandTool, Response: "r"}},
			conversation.Entry{Turn: turn.ID, Item: item, Body: conversation.ApprovalAsked{Approval: "a1", Content: "make", Suggested: []string{"run_command(make)"}}})
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
	it, _ := f.view(id).ItemOf(item)
	return it
}

func TestMessagesQueueTurnsAndSteeringAnswersWhatTheTurnAsked(t *testing.T) {
	f := setup(t)
	th := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", "", "", "chat"))
	if th.Environment == "" {
		t.Fatal("the conversation is in no environment")
	}
	first := sent(t)(f.a.Send(f.ctx, "u", th.ID, Input{Text: "build it"}))
	if first.Model.Connection != "main" || first.Role != string(agent.FullMain) {
		t.Errorf("the turn %+v", first)
	}
	second := sent(t)(f.a.Send(f.ctx, "u", th.ID, Input{Text: "then test it", Connection: "text"}))
	if v := f.view(th.ID); len(v.Queued()) != 1 || v.Queued()[0].ID != second.ID || second.Model.Name != "small" {
		t.Fatalf("the second turn waits behind: %+v", v.Queued())
	}
	f.waitOnApproval(th.ID, first)
	if _, err := f.a.Steer(f.ctx, "u", th.ID, second.ID, Input{Text: "no"}); !errors.Is(err, ErrTurnMismatch) {
		t.Errorf("steering a turn not in progress: %v", err)
	}
	must[conversation.Turn](t)(f.a.Steer(f.ctx, "u", th.ID, first.ID, Input{Text: "use make instead"}))
	v := f.view(th.ID)
	if call := v.ItemsOf(first.ID)[1]; call.Status != conversation.Declined || v.Status() != conversation.ThreadActive {
		t.Errorf("after steering: %s, %s", call.Status, v.Status())
	}
	if err := f.a.Interrupt(f.ctx, "u", th.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if active, _ := f.view(th.ID).Active(); active.ID != second.ID {
		t.Errorf("after the interrupt, the turn in progress is %s", active.ID)
	}
	if _, _, err := f.a.Send(f.ctx, "v", th.ID, Input{Text: "hi"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's conversation: %v", err)
	}
	if err := f.a.Interrupt(f.ctx, "u", th.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	must[store.Thread](t)(f.a.ArchiveThread(f.ctx, "u", th.ID, true))
	if _, _, err := f.a.Send(f.ctx, "u", th.ID, Input{Text: "more"}); !errors.Is(err, ErrThreadArchived) {
		t.Errorf("a message to an archived conversation: %v", err)
	}
}

func TestAcceptingAlwaysKeepsTheRuleWithTheConversation(t *testing.T) {
	f := setup(t)
	th := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", "", "", ""))
	turn := sent(t)(f.a.Send(f.ctx, "u", th.ID, Input{Text: "build"}))
	f.waitOnApproval(th.ID, turn)
	if err := f.a.Decide(f.ctx, "u", th.ID, "a2", conversation.Accept); !errors.Is(err, ErrNoSuchRequest) {
		t.Errorf("an approval not asked for: %v", err)
	}
	if err := f.a.Decide(f.ctx, "u", th.ID, "a1", conversation.AcceptAlways); err != nil {
		t.Fatal(err)
	}
	var stored store.Thread
	f.st.Read(f.ctx, func(r store.Reader) (err error) { stored, err = r.Thread(th.ID); return err })
	if got := stored.Settings.Rules.Allow; len(got) != 1 || got[0] != "run_command(make)" {
		t.Errorf("the rules kept: %+v", stored.Settings)
	}
	if v := f.view(th.ID); v.Waiting() != conversation.WaitNone {
		t.Errorf("still waiting on %s", v.Waiting())
	}
}

func TestAcceptingAnEscalatedCallAlwaysKeepsItsRuleAsAnEscalateRule(t *testing.T) {
	f := setup(t)
	th := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", "", "", ""))
	turn := sent(t)(f.a.Send(f.ctx, "u", th.ID, Input{Text: "check the vm"}))
	err := f.st.Update(f.ctx, func(tx store.Tx) error {
		_, err := tx.Append(th.ID,
			conversation.Entry{Turn: turn.ID, Body: conversation.ModelResponded{Response: "r"}},
			conversation.Entry{Turn: turn.ID, Item: "c1", Body: conversation.ToolCall{Tool: agent.RunCommandTool, Response: "r"}},
			conversation.Entry{Turn: turn.ID, Item: "c1", Body: conversation.ApprovalAsked{Approval: "a1", Content: "ssh vm uptime", Suggested: []string{"run_command(ssh:*)"}, Escalated: true}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.Decide(f.ctx, "u", th.ID, "a1", conversation.AcceptAlways); err != nil {
		t.Fatal(err)
	}
	var stored store.Thread
	f.st.Read(f.ctx, func(r store.Reader) (err error) { stored, err = r.Thread(th.ID); return err })
	if rules := stored.Settings.Rules; !slices.Equal(rules.Escalate, []string{"run_command(ssh:*)"}) || len(rules.Allow) != 0 {
		t.Errorf("the rules kept: %+v", rules)
	}
}

func TestASubagentTakesMessagesInItsRoleAndKeepsRulesInItsWorkspace(t *testing.T) {
	f := setup(t)
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "home"), 0o755)
	os.Mkdir(filepath.Join(dir, "docs"), 0o755)
	home := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", filepath.Join(dir, "home"), ""))
	docs := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", filepath.Join(dir, "docs"), ""))
	parent := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", home.ID, "", ""))
	now := time.Now().UTC()
	err := f.st.Update(f.ctx, func(tx store.Tx) error {
		child := store.Thread{User: "u", Thread: account.Thread{ID: "child", Workspace: docs.ID, Environment: parent.Environment,
			Parent: parent.ID, ParentItem: "launch", Role: string(agent.GeneralSub), Created: now, Updated: now}}
		if err := tx.CreateThread(child); err != nil {
			return err
		}
		_, err := tx.Append("child",
			conversation.Entry{Turn: "t1", Body: conversation.TurnQueued{Model: llm.Model{Name: "big", Connection: "main"}, Role: string(agent.GeneralSub)}},
			conversation.Entry{Turn: "t1", Item: "m1", Body: conversation.UserMessage{Text: "write the page"}})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := f.view("child").Active()
	f.waitOnApproval("child", first)
	if err := f.a.Decide(f.ctx, "u", "child", "a1", conversation.AcceptAlways); err != nil {
		t.Fatal(err)
	}
	ws := must[[]account.Workspace](t)(f.a.Workspaces(f.ctx, "u"))
	for _, w := range ws {
		allowed := w.Settings.Rules.Allow
		if kept := len(allowed) == 1 && allowed[0] == "run_command(make)"; kept != (w.ID == docs.ID) {
			t.Errorf("workspace %s keeps %+v", w.Name, w.Settings)
		}
	}
	next := sent(t)(f.a.Send(f.ctx, "u", "child", Input{Text: "and the index"}))
	if next.Role != string(agent.GeneralSub) {
		t.Errorf("a message to the subagent runs as %s", next.Role)
	}
	must[conversation.Turn](t)(f.a.Steer(f.ctx, "u", "child", first.ID, Input{Text: "keep it short"}))
}

func (f *fixture) launch(parent store.Thread, id string, role agent.Name, task string) {
	f.t.Helper()
	now := time.Now().UTC()
	err := f.st.Update(f.ctx, func(tx store.Tx) error {
		child := store.Thread{User: "u", Thread: account.Thread{ID: id, Title: task, Environment: parent.Environment,
			Parent: parent.ID, ParentItem: "launch-" + id, Role: string(role), Created: now, Updated: now}}
		if err := tx.CreateThread(child); err != nil {
			return err
		}
		_, err := tx.Append(id,
			conversation.Entry{Turn: id + "-t1", Body: conversation.TurnQueued{Model: llm.Model{Name: "big", Connection: "main"}, Role: string(role)}},
			conversation.Entry{Turn: id + "-t1", Item: id + "-m1", Body: conversation.UserMessage{Text: task}})
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func TestAnswersMustAnswerEveryQuestionAsked(t *testing.T) {
	f := setup(t)
	th := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", "", "", ""))
	turn := sent(t)(f.a.Send(f.ctx, "u", th.ID, Input{Text: "plan a trip"}))
	request, _ := json.Marshal(ask.Request{Questions: []ask.Question{{ID: "where", Header: "Where", Question: "Where to?"}}})
	f.st.Update(f.ctx, func(tx store.Tx) error {
		_, err := tx.Append(th.ID,
			conversation.Entry{Turn: turn.ID, Body: conversation.ModelResponded{Response: "r"}},
			conversation.Entry{Turn: turn.ID, Item: "q", Body: conversation.ToolCall{Tool: ask.Tool, Response: "r"}},
			conversation.Entry{Turn: turn.ID, Item: "q", Body: conversation.ToolStarted{}},
			conversation.Entry{Turn: turn.ID, Item: "q", Body: conversation.InputAsked{Request: request}})
		return err
	})
	if err := f.a.Answer(f.ctx, "u", th.ID, "q", ask.Answers{"when": {Answers: []string{"May"}}}); !errors.Is(err, ErrInvalidAnswer) {
		t.Errorf("an answer to a question not asked: %v", err)
	}
	if err := f.a.Answer(f.ctx, "u", th.ID, "q", ask.Answers{"where": {Answers: []string{"Kyoto"}}}); err != nil {
		t.Fatal(err)
	}
	if it, _ := f.view(th.ID).ItemOf("q"); it.Status != conversation.Done || !strings.Contains(string(it.Result.Output), "Kyoto") {
		t.Errorf("the answered call %+v", it)
	}
}

func TestAttachmentsAreCopiedAndGoWithTheirConversation(t *testing.T) {
	f := setup(t)
	path := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(path, []byte("remember the milk"), 0o600)
	th := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", "", "", ""))
	if _, _, err := f.a.Send(f.ctx, "u", th.ID, Input{Attachments: []AttachmentInput{{Path: "relative.txt"}}}); !errors.Is(err, ErrInvalidMessage) {
		t.Errorf("a relative path: %v", err)
	}
	turn := sent(t)(f.a.Send(f.ctx, "u", th.ID, Input{Text: "read this", Attachments: []AttachmentInput{{Path: path}}}))
	att := f.view(th.ID).ItemsOf(turn.ID)[0].Attachments[0]
	got, data, err := f.a.ReadAttachment(f.ctx, "u", th.ID, att.ID)
	if err != nil || got.Name != "notes.txt" || string(data) != "remember the milk" {
		t.Fatalf("the attachment %+v %q %v", got, data, err)
	}
	if err := f.a.DeleteThread(f.ctx, "u", th.ID); !errors.Is(err, ErrThreadBusy) {
		t.Errorf("deleting a busy conversation: %v", err)
	}
	f.a.Interrupt(f.ctx, "u", th.ID, turn.ID)
	if err := f.a.DeleteThread(f.ctx, "u", th.ID); err != nil || len(f.blobs.files) != 0 {
		t.Errorf("deleted: %v, %d files left", err, len(f.blobs.files))
	}
}

func TestAWorkspaceMovesOnlyWhenItsConversationsAreIdle(t *testing.T) {
	f := setup(t)
	dir := t.TempDir()
	root, to := filepath.Join(dir, "project"), filepath.Join(dir, "moved")
	os.Mkdir(root, 0o755)
	os.Mkdir(to, 0o755)
	w := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", root, ""))
	if again := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", root+"/", "other")); again.ID != w.ID || w.Name != "project" {
		t.Errorf("the same folder twice: %+v %+v", w, again)
	}
	th := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", w.ID, "", ""))
	turn := sent(t)(f.a.Send(f.ctx, "u", th.ID, Input{Text: "work"}))
	if _, err := f.a.MoveWorkspace(f.ctx, "u", w.ID, to); !errors.Is(err, ErrWorkspaceBusy) {
		t.Errorf("moving a busy workspace: %v", err)
	}
	f.a.Interrupt(f.ctx, "u", th.ID, turn.ID)
	moved := must[account.Workspace](t)(f.a.MoveWorkspace(f.ctx, "u", w.ID, to))
	if _, err := os.Stat(root); !os.IsNotExist(err) || !strings.HasSuffix(moved.Root, "moved") {
		t.Errorf("after the move: %+v, %v", moved, err)
	}
	must[account.Workspace](t)(f.a.ArchiveWorkspace(f.ctx, "u", w.ID, true))
	if _, _, err := f.a.Send(f.ctx, "u", th.ID, Input{Text: "more"}); !errors.Is(err, account.ErrWorkspaceArchived) {
		t.Errorf("a message in an archived workspace: %v", err)
	}
}

func TestAWorkspaceKeepsItsInstructionsAndProviders(t *testing.T) {
	f := setup(t)
	w := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", t.TempDir(), "plans"))
	got := must[account.Workspace](t)(f.a.UpdateWorkspaceInstructions(f.ctx, "u", w.ID, "  Plan, do not build.  "))
	if got.Instructions != "Plan, do not build." {
		t.Errorf("the instructions %q", got.Instructions)
	}
	got = must[account.Workspace](t)(f.a.UpdateWorkspaceProviders(f.ctx, "u", w.ID, account.Providers{Disabled: []string{agent.WorkspaceProvider}}))
	if !slices.Equal(got.Providers.Disabled, []string{agent.WorkspaceProvider}) {
		t.Errorf("the providers %+v", got.Providers)
	}
	if _, err := f.a.UpdateWorkspaceProviders(f.ctx, "u", w.ID, account.Providers{Enabled: []string{"nowhere"}}); !errors.Is(err, policy.ErrInvalidSettings) {
		t.Errorf("an unknown provider: %v", err)
	}
	stored := must[[]account.Workspace](t)(f.a.Workspaces(f.ctx, "u"))
	if stored[0].Instructions != "Plan, do not build." || len(stored[0].Providers.Disabled) != 1 {
		t.Errorf("stored %+v", stored[0])
	}
}

func TestAUserHasOneHubWhoseConversationsRunAsTheHub(t *testing.T) {
	f := setup(t)
	dir := t.TempDir()
	for _, name := range []string{"home", "work", "docs"} {
		os.Mkdir(filepath.Join(dir, name), 0o755)
	}
	home := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", filepath.Join(dir, "home"), ""))
	work := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", filepath.Join(dir, "work"), ""))
	docs := must[account.Workspace](t)(f.a.AddWorkspace(f.ctx, "u", filepath.Join(dir, "docs"), ""))
	if home.Kind != account.ProjectWorkspace {
		t.Errorf("a new workspace is %q", home.Kind)
	}
	noHub := New(f.st, models{}, func(context.Context, string) (tool.Set, error) { return plainSet{}, nil }, noStatuses, here, f.blobs,
		cron.NewCalendar(time.UTC), policy.Settings{})
	if _, err := noHub.SetWorkspaceKind(f.ctx, "u", home.ID, account.HubWorkspace); !errors.Is(err, ErrNoHub) {
		t.Errorf("a hub where the deployment offers none: %v", err)
	}
	if _, err := f.a.SetWorkspaceKind(f.ctx, "u", home.ID, "inbox"); !errors.Is(err, account.ErrInvalidName) {
		t.Errorf("an unknown kind: %v", err)
	}
	must[account.Workspace](t)(f.a.SetWorkspaceKind(f.ctx, "u", home.ID, account.HubWorkspace))
	if _, err := f.a.SetWorkspaceKind(f.ctx, "u", work.ID, account.HubWorkspace); !errors.Is(err, ErrSecondHub) {
		t.Errorf("a second hub: %v", err)
	}
	must[account.Workspace](t)(f.a.SetWorkspaceKind(f.ctx, "u", home.ID, account.HubWorkspace))
	plan := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", home.ID, "", ""))
	notes := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", docs.ID, "", ""))
	if turn := sent(t)(f.a.Send(f.ctx, "u", plan.ID, Input{Text: "plan the week"})); turn.Role != string(agent.HubMain) {
		t.Errorf("a turn in the hub runs as %s", turn.Role)
	}
	if turn := sent(t)(f.a.Send(f.ctx, "u", notes.ID, Input{Text: "write notes"})); turn.Role != string(agent.FullMain) {
		t.Errorf("a turn in a project runs as %s", turn.Role)
	}
	must[account.Workspace](t)(f.a.SetWorkspaceKind(f.ctx, "u", home.ID, account.ProjectWorkspace))
	must[account.Workspace](t)(f.a.SetWorkspaceKind(f.ctx, "u", work.ID, account.HubWorkspace))
	if turn := sent(t)(f.a.Send(f.ctx, "u", plan.ID, Input{Text: "and next week"})); turn.Role != string(agent.FullMain) {
		t.Errorf("after the hub moved, a turn in the old one runs as %s", turn.Role)
	}
}

func TestCronJobsAreNamedOncePerConversation(t *testing.T) {
	f := setup(t)
	th := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", "", "", ""))
	must[store.CronJobView](t)(f.a.CreateCronJob(f.ctx, "u", th.ID, cron.Draft{Name: "news", Spec: "@daily", Prompt: "read the news"}))
	if _, err := f.a.CreateCronJob(f.ctx, "u", th.ID, cron.Draft{Name: "news", Spec: "@hourly", Prompt: "again"}); !errors.Is(err, cron.ErrInvalidJob) {
		t.Errorf("a name twice: %v", err)
	}
	if j := must[store.CronJobView](t)(f.a.PauseCronJob(f.ctx, "u", th.ID, "news", true)); !j.Paused {
		t.Error("not paused")
	}
	if err := f.a.DeleteCronJob(f.ctx, "u", th.ID, "news"); err != nil {
		t.Fatal(err)
	}
	if jobs := must[[]store.CronJobView](t)(f.a.CronJobs(f.ctx, "u", th.ID)); len(jobs) != 0 {
		t.Errorf("left %+v", jobs)
	}
}

func TestTheUserCorrectsTheirNotes(t *testing.T) {
	f := setup(t)
	now := time.Now().UTC()
	note := memory.Memory{ID: "n", User: "u", Place: memory.Place{Scope: memory.General}, Topic: "editor", Kind: memory.Preference, Text: "Uses vim.",
		Status: memory.Active, Sources: []memory.Source{}, Created: now, Updated: now}
	f.st.Update(f.ctx, func(tx store.Tx) error { return tx.PutMemory(note) })
	if m := must[memory.Memory](t)(f.a.EditMemory(f.ctx, "u", "n", "Uses helix.")); !m.UserEdited || m.Text != "Uses helix." {
		t.Errorf("edited %+v", m)
	}
	if m := must[memory.Memory](t)(f.a.MoveMemory(f.ctx, "u", "n")); m.Scope != memory.Global {
		t.Errorf("moved %+v", m)
	}
	for _, workspace := range []string{"", "w"} {
		_, found, err := f.a.Search(f.ctx, "u", "helix", workspace, 10)
		if err != nil || len(found) != 1 {
			t.Errorf("in %q found %+v %v", workspace, found, err)
		}
	}
	if err := f.a.ForgetMemory(f.ctx, "u", "n"); err != nil {
		t.Fatal(err)
	}
	if s := must[MemoryStatus](t)(f.a.MemoryStatus(f.ctx, "u")); s.Active != 0 {
		t.Errorf("status %+v", s)
	}
	if _, err := f.a.Memories(f.ctx, "u", memory.Place{Scope: memory.Workspace}); !errors.Is(err, memory.ErrInvalidChange) {
		t.Errorf("a workspace place with no workspace: %v", err)
	}
}

func noStatuses(string) ([]account.MCPStatus, error) { return nil, nil }

func TestAMessageKeepsTheHubConversationThatSentIt(t *testing.T) {
	f := setup(t)
	plan := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", "", "", "plan"))
	notes := must[store.Thread](t)(f.a.StartThread(f.ctx, "u", "", "", "notes"))
	if _, _, err := f.a.Send(f.ctx, "u", notes.ID, Input{Text: "write notes", From: "nowhere"}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("sent from a conversation that is not there: %v", err)
	}
	turn := sent(t)(f.a.Send(f.ctx, "u", notes.ID, Input{Text: " write notes ", From: plan.ID}))
	must[conversation.Turn](t)(f.a.Steer(f.ctx, "u", notes.ID, turn.ID, Input{Text: "and the changelog"}))
	items := f.view(notes.ID).Items
	if it := items[0]; it.Text != "write notes" || it.From == nil || it.From.Thread != plan.ID || it.From.Title != "plan" {
		t.Errorf("the hub's message %+v from %+v", it, it.From)
	}
	if it := items[1]; it.From != nil {
		t.Errorf("the user's message came from %+v", it.From)
	}
}
