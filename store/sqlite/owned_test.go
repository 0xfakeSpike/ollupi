package sqlite

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
)

func update(t *testing.T, s *Store, fn func(store.Tx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

func read[T any](t *testing.T, s *Store, fn func(store.Reader) (T, error)) T {
	t.Helper()
	var out T
	if err := s.Read(context.Background(), func(r store.Reader) error {
		var err error
		out, err = fn(r)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAUserIsReadyWithSettingsAndThisMachine(t *testing.T) {
	s := open(t, t.TempDir())
	update(t, s, func(tx store.Tx) error {
		return tx.EnsureUser("u", account.NewEnvironment("This computer", "local", nil))
	})
	update(t, s, func(tx store.Tx) error {
		return tx.EnsureUser("u", account.NewEnvironment("This computer", "local", nil))
	})
	settings := read(t, s, func(r store.Reader) (account.Settings, error) { return r.Settings("u") })
	if !settings.Redaction || !settings.Memory || len(settings.RedactionKey) < 16 {
		t.Errorf("settings %+v", settings)
	}
	envs := read(t, s, func(r store.Reader) ([]account.Environment, error) { return r.Environments("u") })
	if len(envs) != 1 || envs[0].Provider != "local" || envs[0].Name != "This computer" {
		t.Fatalf("environments %+v", envs)
	}
	settings.Memory, settings.Permissions = false, policy.Settings{Rules: policy.Rules{Allow: []string{"read_file"}}}
	var heard []store.Event
	s.Feed().Subscribe("u", func(e store.Event) { heard = append(heard, e) })
	update(t, s, func(tx store.Tx) error { return tx.SetSettings("u", settings) })
	if got := read(t, s, func(r store.Reader) (account.Settings, error) { return r.Settings("u") }); got.Memory || got.Permissions.Rules.Allow[0] != "read_file" {
		t.Errorf("after setting: %+v", got)
	}
	if len(heard) != 1 || heard[0].Owned == nil || heard[0].Owned.Settings == nil || heard[0].Seq == 0 {
		t.Errorf("heard %+v", heard)
	}
}

func TestWhatAUserOwnsIsTheirsAlone(t *testing.T) {
	s := open(t, t.TempDir())
	now := time.Now().UTC()
	update(t, s, func(tx store.Tx) error {
		for _, u := range []string{"a", "b"} {
			if err := tx.EnsureUser(u, account.Environment{ID: "here-" + u, Name: "This computer", Provider: "local"}); err != nil {
				return err
			}
		}
		if err := tx.PutWorkspace("a", account.Workspace{ID: "w1", Name: "app", Environment: "here-a", Root: "/app", Created: now, Updated: now}); err != nil {
			return err
		}
		if err := tx.PutEnvironment("a", account.Environment{ID: "e1", Name: "Cloud", Provider: "cloud", Config: []byte(`{"template":"base"}`), Created: now.Add(time.Hour), Updated: now.Add(time.Hour)}); err != nil {
			return err
		}
		return tx.PutMCPServer("a", account.MCPServer{Name: "github", URL: "https://x/mcp", Headers: map[string]string{"Authorization": "t"}})
	})
	err := s.Update(context.Background(), func(tx store.Tx) error {
		return tx.PutWorkspace("a", account.Workspace{ID: "w2", Name: "again", Environment: "here-a", Root: "/app", Created: now, Updated: now})
	})
	if err == nil {
		t.Error("two workspaces share a folder")
	}
	update(t, s, func(tx store.Tx) error {
		return tx.PutWorkspace("a", account.Workspace{ID: "w3", Name: "there", Environment: "e1", Root: "/app", Created: now, Updated: now})
	})
	err = s.Update(context.Background(), func(tx store.Tx) error {
		return tx.PutEnvironment("a", account.Environment{ID: "e2", Name: "cloud", Provider: "cloud", Created: now, Updated: now})
	})
	if err == nil {
		t.Error("two environments share a name, but for case")
	}
	if _, err := s.readWorkspace("b", "w1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("another user's workspace: %v", err)
	}
	if w, err := s.readWorkspace("a", "w1"); err != nil || w.Root != "/app" {
		t.Errorf("the workspace %+v, %v", w, err)
	}
	envs := read(t, s, func(r store.Reader) ([]account.Environment, error) { return r.Environments("a") })
	if len(envs) != 2 || envs[0].ID != "here-a" || envs[1].ID != "e1" || string(envs[1].Config) != `{"template":"base"}` || len(envs[1].State) != 2 {
		t.Errorf("environments %+v", envs)
	}
	update(t, s, func(tx store.Tx) error { return tx.KeepEnvironmentState("a", "e1", []byte(`{"sandbox":"sb-1"}`)) })
	if e := read(t, s, func(r store.Reader) (account.Environment, error) { return r.Environment("a", "e1") }); string(e.State) != `{"sandbox":"sb-1"}` {
		t.Errorf("the kept state %s", e.State)
	}
	servers := read(t, s, func(r store.Reader) ([]account.MCPServer, error) { return r.MCPServers("a") })
	if len(servers) != 1 || servers[0].Headers["Authorization"] != "t" || servers[0].Args == nil {
		t.Errorf("servers %+v", servers)
	}
	if others := read(t, s, func(r store.Reader) ([]account.MCPServer, error) { return r.MCPServers("b") }); len(others) != 0 {
		t.Errorf("b sees %+v", others)
	}
	update(t, s, func(tx store.Tx) error { return tx.DeleteMCPServer("a", "github") })
	if err := s.Update(context.Background(), func(tx store.Tx) error { return tx.DeleteMCPServer("a", "github") }); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("deleting it again: %v", err)
	}
}

func (s *Store) readWorkspace(owner, id string) (account.Workspace, error) {
	var w account.Workspace
	err := s.Read(context.Background(), func(r store.Reader) error {
		var err error
		w, err = r.Workspace(owner, id)
		return err
	})
	return w, err
}

func TestThreadsListTheirCronJobsAndGoWithThem(t *testing.T) {
	s := open(t, t.TempDir())
	start(t, s, "u", "first")
	time.Sleep(2 * time.Millisecond)
	start(t, s, "u", "second")
	now := time.Now().UTC()
	update(t, s, func(tx store.Tx) error {
		if err := tx.CreateThread(store.Thread{User: "u", Thread: account.Thread{ID: "child", Parent: "first", Created: now, Updated: now}}); err != nil {
			return err
		}
		if err := tx.PutCronJob(account.CronJob{ID: "j1", Thread: "first", Name: "standup", Spec: "@daily", Prompt: "write the notes", NextRun: now.Add(-time.Minute), Created: now}); err != nil {
			return err
		}
		return tx.PutCronJob(account.CronJob{ID: "j2", Thread: "second", Name: "later", Spec: "@daily", Prompt: "x", NextRun: now.Add(time.Hour), Created: now})
	})
	list, _ := read2(t, s, func(r store.Reader) ([]store.Thread, bool, error) {
		return r.Threads("u", store.ThreadFilter{Limit: 1})
	})
	if len(list) != 1 || list[0].ID != "second" {
		t.Fatalf("the first page %+v", list)
	}
	after := &store.ThreadCursor{Updated: list[0].Updated, Created: list[0].Created, ID: list[0].ID}
	rest, more := read2(t, s, func(r store.Reader) ([]store.Thread, bool, error) {
		return r.Threads("u", store.ThreadFilter{Limit: 5, After: after})
	})
	if len(rest) != 1 || rest[0].ID != "first" || more {
		t.Errorf("the next page %+v, more %v", rest, more)
	}
	if kids := read(t, s, func(r store.Reader) ([]store.Thread, error) { return r.Children("first") }); len(kids) != 1 || kids[0].ID != "child" {
		t.Errorf("children %+v", kids)
	}
	if due := read(t, s, func(r store.Reader) ([]account.CronJob, error) { return r.Due(now) }); len(due) != 1 || due[0].ID != "j1" {
		t.Errorf("due %+v", due)
	}
	var heard []store.Event
	s.Feed().Subscribe("u", func(e store.Event) { heard = append(heard, e) })
	update(t, s, func(tx store.Tx) error { return tx.DeleteThread("first") })
	if _, err := s.readThread("child"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a subagent's conversation outlived its parent: %v", err)
	}
	if jobs := read(t, s, func(r store.Reader) ([]store.CronJobView, error) { return r.CronJobs("u", "") }); len(jobs) != 1 || jobs[0].ID != "j2" {
		t.Errorf("tasks after the delete %+v", jobs)
	}
	deleted := slices.ContainsFunc(heard, func(e store.Event) bool { return e.Change != nil && e.Change.Deleted && e.Thread == "first" })
	jobGone := slices.ContainsFunc(heard, func(e store.Event) bool { return e.Owned != nil && e.Owned.CronJobDeleted == "j1" })
	if !deleted || !jobGone {
		t.Errorf("heard %+v", heard)
	}
	if busy := read(t, s, func(r store.Reader) (int64, error) { return r.Busy() }); busy != 1 {
		t.Errorf("%d conversations busy", busy)
	}
}

func (s *Store) readThread(id string) (store.Thread, error) {
	var th store.Thread
	err := s.Read(context.Background(), func(r store.Reader) error {
		var err error
		th, err = r.Thread(id)
		return err
	})
	return th, err
}

func read2[T any](t *testing.T, s *Store, fn func(store.Reader) (T, bool, error)) (T, bool) {
	t.Helper()
	var out T
	var more bool
	if err := s.Read(context.Background(), func(r store.Reader) error {
		var err error
		out, more, err = fn(r)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out, more
}

func TestSearchFindsTitlesMessagesAndCronJobs(t *testing.T) {
	s := open(t, t.TempDir())
	start(t, s, "u", "th")
	now := time.Now().UTC()
	update(t, s, func(tx store.Tx) error {
		th, err := tx.Thread("th")
		if err != nil {
			return err
		}
		th.Title = "Quarterly report"
		if err := tx.UpdateThread(th); err != nil {
			return err
		}
		if _, err := tx.Append("th", conversation.Entry{Turn: "t1", Body: conversation.ModelResponded{Response: "r1"}},
			conversation.Entry{Turn: "t1", Item: "a1", Body: conversation.AgentMessage{Text: "The budget for the QUARTERLY offsite is ready.", Response: "r1"}}); err != nil {
			return err
		}
		return tx.PutCronJob(account.CronJob{ID: "j", Thread: "th", Name: "quarterly", Spec: "@monthly", Prompt: "remind me", NextRun: now, Created: now})
	})
	found := read(t, s, func(r store.Reader) (store.Found, error) { return r.Search("u", []string{"quarterly"}, "", 10) })
	if len(found.Threads) != 1 || len(found.Messages) != 1 || len(found.CronJobs) != 1 {
		t.Fatalf("found %+v", found)
	}
	if hit := found.Messages[0]; hit.Item != "a1" || hit.Kind != conversation.ItemAgentMessage || hit.Title != "Quarterly report" {
		t.Errorf("the hit %+v", hit)
	}
	if none := read(t, s, func(r store.Reader) (store.Found, error) {
		return r.Search("someone else", []string{"quarterly"}, "", 10)
	}); len(none.Messages) != 0 {
		t.Errorf("another user found %+v", none)
	}
}

func TestAUserKeepsTheModelsTheyChose(t *testing.T) {
	s := open(t, t.TempDir())
	update(t, s, func(tx store.Tx) error {
		return tx.EnsureUser("a", account.NewEnvironment("This computer", "local", nil))
	})
	settings := read(t, s, func(r store.Reader) (account.Settings, error) { return r.Settings("a") })
	if settings.Model != "" || settings.LearnModel != "" {
		t.Fatalf("a new user chose %+v", settings)
	}
	settings.Model, settings.LearnModel = "DeepSeek 官方/deepseek-chat", "DeepSeek 官方/deepseek-reasoner"
	update(t, s, func(tx store.Tx) error { return tx.SetSettings("a", settings) })
	if got := read(t, s, func(r store.Reader) (account.Settings, error) { return r.Settings("a") }); got.Model != settings.Model || got.LearnModel != settings.LearnModel {
		t.Fatalf("settings %+v", got)
	}
}
