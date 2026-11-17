package router

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/event"
	"github.com/0xfakeSpike/ollupi/agent/service"
	"github.com/0xfakeSpike/ollupi/gateway"
	"github.com/0xfakeSpike/ollupi/repo/catalog"
	"github.com/0xfakeSpike/ollupi/repo/db"
	"github.com/coder/websocket"
)

// shard is one shard's process: its gateway on its own database, serving until stop.
type shard struct {
	db     *db.DB
	server *gateway.Server
	events *event.Emitter
	url    string
	stop   context.CancelFunc
}

func startShard(t *testing.T, id, home string, authenticate gateway.Authenticate, placements gateway.Placements) *shard {
	t.Helper()
	store, err := db.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	events := event.New(store)
	s, err := gateway.NewServer(service.New(service.Config{DB: store}), events, gateway.Options{
		DB: store, Authenticate: authenticate, Shard: id, Placements: placements,
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		s.Serve(ctx, ln)
	}()
	t.Cleanup(func() {
		stop()
		<-served
		store.Close()
	})
	return &shard{db: store, server: s, events: events, url: "http://" + ln.Addr().String(), stop: stop}
}

func hasUser(t *testing.T, store *db.DB, user string) bool {
	t.Helper()
	users, err := store.Users()
	if err != nil {
		t.Fatal(err)
	}
	return slices.Contains(users, user)
}

// connect opens a client's connection and initializes it.
func connect(ctx context.Context, t *testing.T, url, token string) *websocket.Conn {
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

func get(t *testing.T, url, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	return res.StatusCode
}

func TestTheRouterSendsEachUserToTheirShard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	catalog, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	authenticate := tokens(map[string]string{"token-a": "a", "token-b": "b"})
	homes := map[string]string{"one": t.TempDir(), "two": t.TempDir()}
	shards := map[string]*shard{}
	for id, home := range homes {
		shards[id] = startShard(t, id, home, authenticate, catalog)
		if err := catalog.Publish(t.Context(), id, shards[id].url, true); err != nil {
			t.Fatal(err)
		}
	}
	locator, err := NewLocator(authenticate, catalog)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(NewRouter(locator))
	defer front.Close()

	if status := get(t, front.URL, "nobody"); status != http.StatusUnauthorized {
		t.Errorf("an unknown token: %d", status)
	}

	a, b := connect(ctx, t, front.URL, "token-a"), connect(ctx, t, front.URL, "token-b")
	shardA, err := catalog.ShardOf(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	shardB, err := catalog.ShardOf(t.Context(), "b")
	if err != nil {
		t.Fatal(err)
	}
	if shardA == "" || shardA == shardB {
		t.Fatalf("a on %q, b on %q", shardA, shardB)
	}
	if !hasUser(t, shards[shardA].db, "a") || hasUser(t, shards[shardB].db, "a") {
		t.Error("a is not on their shard alone")
	}
	shards[shardB].events.Emit(event.Event{Kind: event.ThreadDeleted, Thread: db.ThreadRow{ID: "of-b", UserID: "b"}})
	if got := nextThread(ctx, t, b); got != "of-b" {
		t.Errorf("b received %q through the router", got)
	}
	shards[shardA].events.Emit(event.Event{Kind: event.ThreadDeleted, Thread: db.ThreadRow{ID: "of-a", UserID: "a"}})
	if got := nextThread(ctx, t, a); got != "of-a" {
		t.Errorf("a received %q through the router", got)
	}

	// a's shard changes hands: the old process stops, clients fail for now, and the new one publishes itself.
	shards[shardA].stop()
	if _, _, err := a.Read(ctx); err == nil {
		t.Error("a's connection outlived its shard")
	}
	if status := get(t, front.URL, "token-a"); status != http.StatusServiceUnavailable {
		t.Errorf("while the shard changes hands: %d", status)
	}
	standby := startShard(t, shardA, homes[shardA], authenticate, catalog)
	if err := catalog.Publish(t.Context(), shardA, standby.url, true); err != nil {
		t.Fatal(err)
	}
	back := connect(ctx, t, front.URL, "token-a")
	standby.events.Emit(event.Event{Kind: event.ThreadDeleted, Thread: db.ThreadRow{ID: "after", UserID: "a"}})
	if got := nextThread(ctx, t, back); got != "after" {
		t.Errorf("a received %q from the new holder", got)
	}
}

type oneShard struct{ address string }

func (d oneShard) Assign(context.Context, string) (string, error)  { return "s", nil }
func (d oneShard) Address(context.Context, string) (string, error) { return d.address, nil }

// A shard that takes a connection and never answers does not hold the client: the router answers in time.
func TestTheRouterDoesNotWaitOnAShardThatHangs(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer hung.Close()
	defer close(release)
	locator, err := NewLocator(func(context.Context, string) (string, error) { return "a", nil }, oneShard{hung.URL})
	if err != nil {
		t.Fatal(err)
	}
	rt := NewRouter(locator)
	rt.transport.ResponseHeaderTimeout = 100 * time.Millisecond
	front := httptest.NewServer(rt)
	defer front.Close()
	if status := get(t, front.URL, "token"); status != http.StatusServiceUnavailable {
		t.Errorf("the router answered %d", status)
	}
}

// failing finds each user once: the catalog it stands for went down after that.
type failing struct {
	oneShard
	asked map[string]bool
}

func (d failing) Assign(_ context.Context, user string) (string, error) {
	if d.asked[user] {
		return "", errors.New("catalog down")
	}
	d.asked[user] = true
	return "s", nil
}

// A user, once placed, stays on their shard, so the locator finds them again without the directory.
func TestTheLocatorRemembersWhereUsersAre(t *testing.T) {
	locator, err := NewLocator(func(_ context.Context, token string) (string, error) {
		if token == "" {
			return "", gateway.ErrUnknownToken
		}
		return token, nil
	}, failing{asked: map[string]bool{}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, shard, err := locator.Locate(t.Context(), http.Header{"Authorization": {"Bearer a"}}); shard != "s" || err != nil {
			t.Errorf("a on %q: %v", shard, err)
		}
	}
	if _, _, err := locator.Locate(t.Context(), http.Header{}); !errors.Is(err, gateway.ErrUnknownToken) {
		t.Errorf("no token: %v", err)
	}
}

func TestTheRoutersHealthCheckNeedsNoToken(t *testing.T) {
	locator, err := NewLocator(tokens(nil), oneShard{})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	NewRouter(locator).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://10.0.0.7"+gateway.HealthPath, nil))
	if w.Code != http.StatusOK {
		t.Errorf("the router answered its health check %d", w.Code)
	}
}

// tokens authenticates the tokens of users, one each.
func tokens(users map[string]string) gateway.Authenticate {
	return func(_ context.Context, token string) (string, error) {
		user, ok := users[token]
		if !ok {
			return "", gateway.ErrUnknownToken
		}
		return user, nil
	}
}
