package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
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

func backgroundTurn(ctx context.Context, t *testing.T, cfg agent.Config) (*agent.Orchestrator, *repo.DB, repo.ThreadRow, repo.ItemRow) {
	t.Helper()
	ag, db, th, call, _ := backgroundTurnOf(ctx, t, cfg)
	return ag, db, th, call
}

func backgroundTurnOf(ctx context.Context, t *testing.T, cfg agent.Config) (*agent.Orchestrator, *repo.DB, repo.ThreadRow, repo.ItemRow, *callingModel) {
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

// waitingModel starts a download in the background, waits for it with the id it was told, and answers after that.
type waitingModel struct {
	mu   sync.Mutex
	reqs [][]byte
}

func (m *waitingModel) last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return string(m.reqs[len(m.reqs)-1])
}

func (m *waitingModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.reqs = append(m.reqs, body)
	n := len(m.reqs)
	m.mu.Unlock()
	call := func(name, args string) map[string]any {
		return map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%d", n), "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}
	}
	delta, finish := map[string]any{"content": "下载好了。"}, "stop"
	switch n {
	case 1:
		delta, finish = call("files__download", `{"url":"https://example.com/big.iso","run_in_background":true}`), "tool_calls"
	case 2:
		id := regexp.MustCompile(`\\"background\\": *\\"([0-9a-f-]+)\\"`).FindStringSubmatch(string(body))
		if id == nil {
			delta = map[string]any{"content": "no id"}
			break
		}
		delta, finish = call(agent.ToolWaitForBackground, `{"background":["`+id[1]+`"]}`), "tool_calls"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(v any) { raw, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", raw) }
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "demo", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	send(chunk(delta, nil))
	send(chunk(map[string]any{}, finish))
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestATurnWaitsForItsBackgroundCallWithoutASlot(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	slow := slowTools{release: make(chan struct{}), stopped: make(chan struct{})}
	fake := &waitingModel{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ag, db := started(ctx, t, agent.Config{Providers: []tools.Provider{slow}}, srv.URL)
	th, err := ag.StartThread(repo.DefaultUser, "", "", "download", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := ag.StartTurn(ctx, repo.DefaultUser, th.ID, agent.Input{Text: "下载这个文件，下好了告诉我"})
	if err != nil {
		t.Fatal(err)
	}
	reach := func(want repo.TurnStatus) {
		t.Helper()
		for {
			row, err := db.Turn(turn.ID)
			if err == nil && row.Status == want {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("the turn never reached %s: %s", want, row.Status)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	reach(repo.TurnWaitingOnBackground)
	close(slow.release)
	reach(repo.TurnCompleted)
	if v, err := db.Thread(th.ID); err != nil || len(v.Turns) != 1 {
		t.Errorf("the result started a turn of its own instead of coming back to the one that waited: %v", err)
	}
	last := fake.last()
	if !strings.Contains(last, "of files__download ended: completed]") || !strings.Contains(last, `\"ended\"`) {
		t.Errorf("the turn went on without the result: %s", last)
	}
}

// delegatingModel has the parent hand the download to a subagent, which starts it in the background and reports at
// once, and again with what it knows when it is next asked.
type delegatingModel struct {
	mu            sync.Mutex
	parent, child int
	childLast     string
	parentLast    string
}

func (m *delegatingModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	isChild := strings.Contains(string(body), "# You are a subagent")
	var n int
	if isChild {
		m.child++
		n, m.childLast = m.child, string(body)
	} else {
		m.parent++
		n, m.parentLast = m.parent, string(body)
	}
	m.mu.Unlock()
	call := func(name, args string) map[string]any {
		return map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%v_%d", isChild, n), "type": "function", "function": map[string]any{"name": name, "arguments": args}}}}
	}
	delta, finish := map[string]any{"content": "都好了。"}, "stop"
	switch {
	case !isChild && n == 1:
		delta, finish = call("Agent", `{"description":"Download it","prompt":"Download https://example.com/big.iso and report where it is."}`), "tool_calls"
	case isChild && n == 1:
		delta, finish = call("files__download", `{"url":"https://example.com/big.iso","run_in_background":true}`), "tool_calls"
	case isChild && strings.Contains(string(body), "ended: completed]"):
		delta = map[string]any{"content": "big.iso is saved."}
	case isChild:
		delta = map[string]any{"content": "The download has started."}
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(v any) { raw, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", raw) }
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "demo", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	send(chunk(delta, nil))
	send(chunk(map[string]any{}, finish))
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestASubagentReportsOnlyOnceItsBackgroundCallsHaveEnded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	slow := slowTools{release: make(chan struct{}), stopped: make(chan struct{})}
	fake := &delegatingModel{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	ag, db := started(ctx, t, agent.Config{Providers: []tools.Provider{slow}}, srv.URL)
	th, err := ag.StartThread(repo.DefaultUser, "", "", "delegate", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := ag.StartTurn(ctx, repo.DefaultUser, th.ID, agent.Input{Text: "让子 agent 去下载"})
	if err != nil {
		t.Fatal(err)
	}
	child := func() repo.TurnRow {
		v, err := db.Thread(th.ID)
		if err != nil {
			return repo.TurnRow{}
		}
		for _, it := range v.Items {
			if it.Tool == "Agent" && it.Await != nil && it.Await.Handle != "" {
				if cv, err := db.Thread(it.Await.Handle); err == nil && len(cv.Turns) > 0 {
					return cv.Turns[len(cv.Turns)-1]
				}
			}
		}
		return repo.TurnRow{}
	}
	for child().Status != repo.TurnWaitingOnBackground {
		select {
		case <-ctx.Done():
			t.Fatalf("the subagent never waited for its download: %s", child().Status)
		case <-time.After(20 * time.Millisecond):
		}
	}
	close(slow.release)
	for {
		row, err := db.Turn(turn.ID)
		if err == nil && row.Status == repo.TurnCompleted {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("the parent never got the report: %s", row.Status)
		case <-time.After(20 * time.Millisecond):
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !strings.Contains(fake.parentLast, "big.iso is saved.") || strings.Contains(fake.parentLast, "The download has started.") {
		t.Errorf("the parent was reported to before the download ended: %s", fake.parentLast)
	}
}

func TestABackgroundCallCutOffByARestartComesBackAsInterrupted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fake := &callingModel{tool: "files__download", args: `{"url":"https://example.com/big.iso","run_in_background":true}`}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := func(slow slowTools) (*agent.Orchestrator, context.CancelFunc) {
		ag := agent.New(agent.Config{
			Name: "Ollupi", DB: db, Models: []llm.Model{{Name: "demo", Connection: "fake"}},
			Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
			Connections: map[string]llm.Client{"fake": llm.NewOpenAI(srv.URL, "", 4, map[string]llm.Limits{"demo": {Context: 32000, Output: 4000}})},
			Providers:   []tools.Provider{slow},
		})
		actx, stop := context.WithCancel(ctx)
		if err := ag.Start(actx); err != nil {
			stop()
			t.Fatal(err)
		}
		return ag, stop
	}
	first, stop := start(slowTools{release: make(chan struct{}), stopped: make(chan struct{})})
	th, err := first.StartThread(repo.DefaultUser, "", "", "download", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.StartTurn(ctx, repo.DefaultUser, th.ID, agent.Input{Text: "下载这个文件"}); err != nil {
		t.Fatal(err)
	}
	var id string
	for id == "" {
		if v, err := db.Thread(th.ID); err == nil && len(v.Turns) == 1 && v.Turns[0].Status == repo.TurnCompleted {
			for _, it := range v.Items {
				if it.Tool == "files__download" {
					id = backgroundID(t, it)
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("the download never started in the background")
		case <-time.After(20 * time.Millisecond):
		}
	}
	stop()
	first.Wait()
	if call, err := db.BackgroundCall(id); err != nil || call.Status != repo.BackgroundInterrupted || call.DeliveredAt != nil {
		t.Fatalf("the call after its process stopped: %+v %v", call, err)
	}

	second, stopSecond := start(slowTools{release: make(chan struct{}), stopped: make(chan struct{})})
	defer second.Wait()
	defer stopSecond()
	for {
		v, err := db.Thread(th.ID)
		if err == nil && len(v.Turns) == 2 && v.Turns[1].Status == repo.TurnCompleted {
			if first := itemsOf(v, v.Turns[1].ID); len(first) == 0 || first[0].BackgroundID != id || first[0].Text != string(repo.BackgroundInterrupted) {
				t.Errorf("the turn after the restart begins with %+v", first)
			}
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("the interrupted call never came back to the conversation")
		case <-time.After(20 * time.Millisecond):
		}
	}
}
