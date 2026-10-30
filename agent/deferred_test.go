package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/cron"
)

type githubTools struct{}

func (githubTools) Name() (string, string) { return "github", "GitHub issues and pull requests." }
func (githubTools) ToolList(context.Context) ([]*tools.Spec, error) {
	long := strings.Repeat("Detailed usage notes for this GitHub operation. ", 60)
	schema := map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}, "additionalProperties": false}
	return []*tools.Spec{
		{Name: "github__create_issue", Description: "Create an issue. " + long, InputSchema: schema},
		{Name: "github__list_pulls", Description: "List pull requests. " + long, InputSchema: schema, Annotations: &tools.Annotations{ReadOnlyHint: true}},
	}, nil
}
func (githubTools) Invoke(context.Context, string, map[string]any) (tools.Result, error) {
	return tools.Result{Output: map[string]any{"number": 42, "url": "https://github.com/acme/app/issues/42"}}, nil
}

type recordingModel struct {
	mu   sync.Mutex
	reqs [][]byte
}

func (m *recordingModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.reqs = append(m.reqs, body)
	n := len(m.reqs)
	m.mu.Unlock()
	var chunks []map[string]any
	call := func(name, args string) {
		chunks = append(chunks, map[string]any{"content": "我先加载 GitHub 工具。"}, map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_%d", n), "type": "function", "function": map[string]any{"name": name, "arguments": args}}}})
	}
	finish := "tool_calls"
	switch n {
	case 1:
		call(tools.ToolLoadTools, `{"names":["github__create_issue"]}`)
	case 2:
		call("github__create_issue", `{"title":"登录页在 Safari 上白屏"}`)
	case 3:
		chunks, finish = []map[string]any{{"content": "已创建 issue #42：https://github.com/acme/app/issues/42"}}, "stop"
	default:
		chunks, finish = []map[string]any{{"content": "明天上午 10 点前处理。"}}, "stop"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	send := func(v any) { raw, _ := json.Marshal(v); fmt.Fprintf(w, "data: %s\n\n", raw) }
	choice := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "demo", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	}
	for _, c := range chunks {
		send(choice(c, nil))
	}
	send(choice(map[string]any{}, finish))
	send(map[string]any{"id": "x", "object": "chat.completion.chunk", "created": 1, "model": "demo", "choices": []any{}, "usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}})
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// lockedBuffer collects log lines written from several goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestLoadedToolIsSentWithTheNextRequest(t *testing.T) {
	logs := &lockedBuffer{}
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fake := &recordingModel{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	if err := db.SaveMemory(repo.MemoryRow{ID: "mem_1", Scope: repo.MemoryGlobal, Topic: "回复语言", Kind: "preference", Text: "用户希望用中文回复，结论先行。", Status: repo.MemoryActive, Sources: []repo.MemorySource{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mem, err := memory.New(db, nil, memory.Options{Use: true})
	if err != nil {
		t.Fatal(err)
	}
	jobs := cron.New(db, time.Local, func(string, string) {})
	ag := agent.New(agent.Config{
		Name: "Ollupi", Models: []llm.Model{{Name: "demo", Connection: "fake"}},
		Settings:    permission.Settings{ApprovalPolicy: string(permission.Never)},
		Connections: map[string]llm.Client{"fake": llm.NewOpenAI(srv.URL, "", 4, map[string]llm.Limits{"demo": {Context: 32000, Output: 4000}})},
		Providers:   []tools.Provider{githubTools{}, jobs}, Memory: mem, DB: db,
	})
	actx, stop := context.WithCancel(ctx)
	if err := ag.Start(actx); err != nil {
		t.Fatal(err)
	}
	defer ag.Wait()
	defer stop()
	w, err := ag.AddWorkspace(t.TempDir(), "app")
	if err != nil {
		t.Fatal(err)
	}
	th, err := ag.StartThread(w.ID, "", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	wait := func(n int) {
		for {
			fake.mu.Lock()
			got := len(fake.reqs)
			fake.mu.Unlock()
			if got >= n {
				time.Sleep(300 * time.Millisecond)
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("waited for %d requests, got %d", n, got)
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
	if _, err := ag.StartTurn(th.ID, agent.Input{Text: "帮我在 GitHub 上提个 issue：登录页在 Safari 上白屏"}); err != nil {
		t.Fatal(err)
	}
	wait(3)
	type request struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	var reqs []request
	for _, b := range fake.reqs {
		var r request
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		reqs = append(reqs, r)
	}
	sent := func(r request) []string {
		var names []string
		for _, tool := range r.Tools {
			names = append(names, tool.Function.Name)
		}
		return names
	}
	if names := sent(reqs[0]); slices.Contains(names, "github__create_issue") || !slices.Contains(names, tools.ToolLoadTools) ||
		!strings.Contains(reqs[0].Messages[0].Content, "# DEFERRED TOOLS") || !strings.Contains(reqs[0].Messages[0].Content, "- github: github__create_issue, github__list_pulls") {
		t.Errorf("first request sends %q with system prompt:\n%s", names, reqs[0].Messages[0].Content)
	}
	if names := sent(reqs[1]); len(names) == 0 || names[len(names)-1] != "github__create_issue" || slices.Contains(names, "github__list_pulls") {
		t.Errorf("the request after load_tools sends %q, want github__create_issue last and github__list_pulls left out", names)
	}
	if last := reqs[2].Messages[len(reqs[2].Messages)-1]; last.Role != "tool" || !strings.Contains(last.Content, "issues/42") {
		t.Errorf("the loaded tool's call returned %+v", last)
	}
	// The log follows the turn by ids and numbers, and keeps the message, the arguments and the output out.
	got := logs.String()
	for _, want := range []string{"msg=\"turn started\"", "msg=\"model call\"", "msg=\"tool call\"", "tool=github__create_issue"} {
		if !strings.Contains(got, want) {
			t.Errorf("the log lacks %s:\n%s", want, got)
		}
	}
	for _, secret := range []string{"Safari", "issues/42", "中文"} {
		if strings.Contains(got, secret) {
			t.Errorf("the log quotes %q:\n%s", secret, got)
		}
	}
}
