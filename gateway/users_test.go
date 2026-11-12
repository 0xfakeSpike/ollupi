package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools/cron"
	"github.com/coder/websocket"
)

// The gateway reads some views straight from the database; those keep to the connection's user too.
func TestAConnectionReadsOnlyItsUsersRows(t *testing.T) {
	ctx := context.Background()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, user := range []string{"a", "b"} {
		if err := db.EnsureUser(user); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	if err := db.CreateThread(repo.ThreadRow{ID: "t", UserID: "a", Title: "mine", CreatedAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	jobs := cron.New(db, time.UTC, func(string, string) {})
	s, err := NewServer(nil, Options{DB: db, Token: "secret", Jobs: jobs})
	if err != nil {
		t.Fatal(err)
	}
	c := &conn{s: s, user: "b"}

	if _, _, err := threadPage(false)(c, ctx, ThreadPageParams{ThreadID: "t"}); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("read the thread: %v", err)
	}
	if _, _, err := c.cronCreate(ctx, CronCreateParams{ThreadID: "t", Name: "x", Spec: "@daily", Prompt: "go"}); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("schedule a task: %v", err)
	}
	paused := true
	if _, _, err := c.cronPause(ctx, CronPauseParams{ThreadID: "t", Name: "x", Paused: &paused}); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("pause a task: %v", err)
	}
	if _, _, err := c.cronDelete(ctx, CronDeleteParams{ThreadID: "t", Name: "x"}); !errors.Is(err, repo.ErrNotFound) {
		t.Errorf("delete a task: %v", err)
	}
	owner := &conn{s: s, user: "a"}
	if _, _, err := owner.cronCreate(ctx, CronCreateParams{ThreadID: "t", Name: "x", Spec: "@daily", Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	list, after, err := c.cronList(ctx, CronListParams{})
	if err != nil {
		t.Fatal(err)
	}
	after()
	if len(list.Data) != 0 {
		t.Errorf("b lists tasks: %+v", list.Data)
	}
}

// userClient is a connection that has said hello and reads what the gateway sends it.
func userClient(ctx context.Context, t *testing.T, url, token string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http"), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close(websocket.StatusNormalClosure, "") })
	hello := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"test","version":"1"}}}`
	if err := ws.Write(ctx, websocket.MessageText, []byte(hello)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.Read(ctx); err != nil {
		t.Fatal(err)
	}
	return ws
}

// nextThread reads the next notification and says which thread it is about.
func nextThread(ctx context.Context, t *testing.T, ws *websocket.Conn) string {
	t.Helper()
	_, data, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Params struct {
			ThreadID string `json:"threadId"`
		} `json:"params"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m.Params.ThreadID
}

func TestEachUserHasTheirOwnConnectionAndEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tokens := map[string]string{"token-a": "a", "token-b": "b"}
	s, err := NewServer(agent.New(agent.Config{DB: db}), Options{DB: db, Authenticate: func(token string) (string, bool) {
		user, ok := tokens[token]
		return user, ok
	}})
	if err != nil {
		t.Fatal(err)
	}
	gw := httptest.NewServer(s)
	defer gw.Close()

	req, err := http.NewRequest(http.MethodGet, gw.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer nobody")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("an unknown token: %s", res.Status)
	}
	res, err = http.Get(gw.URL + "?token=token-a")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("a token in the URL: %s", res.Status)
	}
	browser, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(gw.URL, "http"), &websocket.DialOptions{Subprotocols: []string{tokenProtocol, "token-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := browser.Subprotocol(); got != tokenProtocol {
		t.Errorf("the gateway answered the subprotocol %q", got)
	}
	browser.Close(websocket.StatusNormalClosure, "")

	a, b := userClient(ctx, t, gw.URL, "token-a"), userClient(ctx, t, gw.URL, "token-b")
	if users, err := db.Users(); err != nil || len(users) != 3 {
		t.Errorf("users after connecting: %v, %v", users, err)
	}
	changed := func(user, thread string) agent.Event {
		return agent.Event{Kind: agent.EventThreadDeleted, Thread: repo.ThreadRow{ID: thread, UserID: user}}
	}
	s.Deliver(changed("b", "of-b"))
	s.Deliver(agent.Event{Kind: agent.EventError, Thread: repo.ThreadRow{ID: "of-nobody"}, Error: "internal"})
	s.Deliver(changed("a", "of-a"))
	if got := nextThread(ctx, t, a); got != "of-a" {
		t.Errorf("a received an event of %q", got)
	}
	if got := nextThread(ctx, t, b); got != "of-b" {
		t.Errorf("b received an event of %q", got)
	}

	newer := userClient(ctx, t, gw.URL, "token-a")
	if _, _, err := a.Read(ctx); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Errorf("a's older connection: %v", err)
	}
	s.Deliver(changed("b", "still-b"))
	if got := nextThread(ctx, t, b); got != "still-b" {
		t.Errorf("a's new connection disturbed b: %q", got)
	}
	s.Deliver(changed("a", "newer-a"))
	if got := nextThread(ctx, t, newer); got != "newer-a" {
		t.Errorf("a's newer connection received %q", got)
	}
}

func TestOnlyLocalAndAllowedHostsAreServed(t *testing.T) {
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := NewServer(nil, Options{DB: db, Token: "secret", AllowedHosts: []string{"app.example"}})
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]int{"app.example:443": 0, "localhost:1": 0, "other.example": http.StatusForbidden} {
		r := httptest.NewRequest(http.MethodGet, "http://"+host, nil)
		r.Header.Set("Authorization", "Bearer secret")
		user, status, _ := s.authorize(r)
		if status != want || want == 0 && user != repo.DefaultUser {
			t.Errorf("%s: user %q, status %d", host, user, status)
		}
	}
}
