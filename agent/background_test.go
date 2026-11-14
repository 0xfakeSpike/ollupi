package agent_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
)

// slowTools runs a download that ends when released, or stops when its call is cancelled.
type slowTools struct {
	release chan struct{}
	stopped chan struct{}
}

func (slowTools) Name() (string, string) { return "files", "Downloads files." }
func (slowTools) ToolList(context.Context) ([]*tools.Spec, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"url": map[string]any{"type": "string"}}, "additionalProperties": false}
	return []*tools.Spec{{Name: "files__download", Description: "Download a file.", InputSchema: schema, Background: true}}, nil
}
func (s slowTools) Invoke(ctx context.Context, _ string, args map[string]any) (tools.Result, error) {
	select {
	case <-s.release:
		return tools.Result{Output: map[string]any{"saved": args["url"]}}, nil
	case <-ctx.Done():
		close(s.stopped)
		return tools.Result{}, ctx.Err()
	}
}

func backgroundTurn(ctx context.Context, t *testing.T, cfg agent.Config) (*agent.Agent, *repo.DB, repo.ThreadRow, repo.ItemRow) {
	t.Helper()
	ag, db, th, call, _ := backgroundTurnOf(ctx, t, cfg)
	return ag, db, th, call
}

func backgroundTurnOf(ctx context.Context, t *testing.T, cfg agent.Config) (*agent.Agent, *repo.DB, repo.ThreadRow, repo.ItemRow, *callingModel) {
	t.Helper()
	fake := &callingModel{tool: "files__download", args: `{"url":"https://example.com/big.iso","run_in_background":true}`}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	ag, db := started(ctx, t, cfg, srv.URL)
	th, err := ag.StartThread(repo.DefaultUser, "", "", "download", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := ag.StartTurn(ctx, repo.DefaultUser, th.ID, agent.Input{Text: "下载这个文件"})
	if err != nil {
		t.Fatal(err)
	}
	for {
		row, err := db.Turn(turn.ID)
		if err == nil && row.Status == repo.TurnCompleted {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the turn never ended while its call ran in the background: %s", row.Status)
		case <-time.After(20 * time.Millisecond):
		}
	}
	v, err := db.Thread(th.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range v.Items {
		if it.Tool == "files__download" {
			return ag, db, th, it, fake
		}
	}
	t.Fatal("the turn made no download call")
	return nil, nil, repo.ThreadRow{}, repo.ItemRow{}, nil
}

func backgroundID(t *testing.T, call repo.ItemRow) string {
	t.Helper()
	var out struct{ Background, Status string }
	if call.Result == nil || json.Unmarshal(call.Result.Output, &out) != nil || out.Background == "" || out.Status != "running" {
		t.Fatalf("the call did not start in the background: %+v %s", call.Result, call.Error)
	}
	return out.Background
}

func until(ctx context.Context, t *testing.T, db *repo.DB, id string, want repo.BackgroundStatus) repo.BackgroundCallRow {
	t.Helper()
	for {
		call, err := db.BackgroundCall(id)
		if err == nil && call.Status == want {
			return call
		}
		select {
		case <-ctx.Done():
			t.Fatalf("background call %s never became %s: %s", id, want, call.Status)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestABackgroundCallLetsItsTurnEndAndComesBackLater(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	slow := slowTools{release: make(chan struct{}), stopped: make(chan struct{})}
	_, db, th, call, fake := backgroundTurnOf(ctx, t, agent.Config{Providers: []tools.Provider{slow}})
	id := backgroundID(t, call)
	if row, err := db.BackgroundCall(id); err != nil || row.Status != repo.BackgroundRunning || row.Arguments != `{"url":"https://example.com/big.iso"}` {
		t.Fatalf("the call while its turn has ended: %+v %v", row, err)
	}
	close(slow.release)
	if ended := until(ctx, t, db, id, repo.BackgroundCompleted); ended.Result == nil || !strings.Contains(string(ended.Result.Output), "big.iso") {
		t.Errorf("the ended call: %+v", ended)
	}
	// The idle conversation takes the result in a turn it starts, and the model reads it as data.
	for {
		v, err := db.Thread(th.ID)
		if err == nil && len(v.Turns) == 2 && v.Turns[1].Status == repo.TurnCompleted {
			if first := itemsOf(v, v.Turns[1].ID); len(first) == 0 || first[0].Type != repo.ItemBackgroundResult || first[0].BackgroundID != id {
				t.Errorf("the turn the result started begins with %+v", first)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("the result never came back to the conversation")
		case <-time.After(20 * time.Millisecond):
		}
	}
	reqs := fake.requests()
	last := string(reqs[len(reqs)-1])
	if !strings.Contains(last, "[background call "+id+" of files__download ended: completed]") || !strings.Contains(last, "big.iso") {
		t.Errorf("the model was not sent the result: %s", last)
	}
	var sent struct {
		Messages []struct{ Role, Content string }
	}
	if json.Unmarshal(reqs[len(reqs)-1], &sent) == nil {
		for _, m := range sent.Messages {
			if strings.Contains(m.Content, "[background call") && strings.Contains(m.Content, "[sent ") {
				t.Errorf("the result was sent as the user's own words: %q", m.Content)
			}
		}
	}
}

func itemsOf(v repo.ThreadView, turnID string) []repo.ItemRow {
	var out []repo.ItemRow
	for _, it := range v.Items {
		if it.TurnID == turnID {
			out = append(out, it)
		}
	}
	return out
}

func TestArchivingAConversationCancelsItsBackgroundCalls(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	slow := slowTools{release: make(chan struct{}), stopped: make(chan struct{})}
	ag, db, th, call := backgroundTurn(ctx, t, agent.Config{Providers: []tools.Provider{slow}})
	id := backgroundID(t, call)
	if _, err := ag.ArchiveThread(repo.DefaultUser, th.ID, true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-slow.stopped:
	case <-ctx.Done():
		t.Fatal("the tool of a cancelled call kept running")
	}
	if row, err := db.BackgroundCall(id); err != nil || row.Status != repo.BackgroundCancelled || row.Result != nil {
		t.Errorf("the call of an archived conversation: %+v %v", row, err)
	}
}
