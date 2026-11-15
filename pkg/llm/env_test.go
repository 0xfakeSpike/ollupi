package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// headersOf answers every request 400, which no client retries, and keeps the headers of the last.
func headersOf(t *testing.T) (*httptest.Server, func() http.Header) {
	t.Helper()
	var mu sync.Mutex
	var last http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		last = r.Header.Clone()
		mu.Unlock()
		http.Error(w, `{"error":{"message":"no"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	return srv, func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

func TestAConnectionSendsNoCredentialsOfTheProcess(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "process-token")
	t.Setenv("OPENAI_API_KEY", "process-key")
	t.Setenv("OPENAI_ORG_ID", "process-org")
	t.Setenv("OPENAI_PROJECT_ID", "process-project")
	t.Setenv("GEMINI_API_KEY", "process-key")
	t.Setenv("GOOGLE_API_KEY", "process-key")
	limits := map[string]Limits{"m": {Context: 32000, Output: 4000}}
	req := ChatRequest{Model: Model{Name: "m"}, Messages: []ChatMessage{{Role: RoleUser, Content: "hi"}}, ToolChoice: ToolChoiceAuto}

	srv, headers := headersOf(t)
	_, err := NewAnthropic(srv.URL, "", 1, limits).Chat(context.Background(), req, nil)
	if headers() == nil {
		t.Fatalf("Anthropic sent no request: %v", err)
	}
	for _, name := range []string{"X-Api-Key", "Authorization"} {
		if v := headers().Get(name); strings.Contains(v, "process") {
			t.Errorf("Anthropic sent %s: %s", name, v)
		}
	}
	srv, headers = headersOf(t)
	_, err = NewOpenAI(srv.URL, "", 1, limits).Chat(context.Background(), req, nil)
	if headers() == nil {
		t.Fatalf("OpenAI sent no request: %v", err)
	}
	for _, name := range []string{"Authorization", "OpenAI-Organization", "OpenAI-Project"} {
		if v := headers().Get(name); v != "" {
			t.Errorf("OpenAI sent %s: %s", name, v)
		}
	}
	if _, err := NewGemini(srv.URL, "", 1, limits); err == nil {
		t.Error("a Gemini connection without a key takes the process's")
	}
}
