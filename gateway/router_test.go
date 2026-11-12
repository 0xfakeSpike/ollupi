package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/coder/websocket"
)

// shardServer is one shard's gateway, on its own database.
type shardServer struct {
	db     *repo.DB
	server *Server
	http   *httptest.Server
}

func startShard(t *testing.T, id string, home string, catalog *repo.Catalog, authenticate func(string) (string, bool)) *shardServer {
	t.Helper()
	db, err := repo.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := NewServer(agent.New(agent.Config{DB: db}), Options{DB: db, Authenticate: OnShard(id, catalog, authenticate)})
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(s)
	t.Cleanup(h.Close)
	return &shardServer{db: db, server: s, http: h}
}

func hasUser(t *testing.T, db *repo.DB, user string) bool {
	t.Helper()
	users, err := db.Users()
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		if u == user {
			return true
		}
	}
	return false
}

func TestTheRouterSendsEachUserToTheirShard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	catalog, err := repo.OpenCatalog(filepath.Join(t.TempDir(), "catalog.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	authenticate := func(token string) (string, bool) {
		user, ok := map[string]string{"token-a": "a", "token-b": "b"}[token]
		return user, ok
	}
	homes := map[string]string{"one": t.TempDir(), "two": t.TempDir()}
	one := startShard(t, "one", homes["one"], catalog, authenticate)
	two := startShard(t, "two", homes["two"], catalog, authenticate)
	for id, s := range map[string]*shardServer{"one": one, "two": two} {
		if err := catalog.Publish(id, s.http.URL, true); err != nil {
			t.Fatal(err)
		}
	}
	router, err := NewRouter(authenticate, catalog)
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(router)
	defer front.Close()

	get := func(url, token string) int {
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
	if status := get(front.URL, "nobody"); status != http.StatusUnauthorized {
		t.Errorf("an unknown token: %d", status)
	}

	a, b := userClient(ctx, t, front.URL, "token-a"), userClient(ctx, t, front.URL, "token-b")
	shardA, err := catalog.ShardOf("a")
	if err != nil {
		t.Fatal(err)
	}
	shardB, err := catalog.ShardOf("b")
	if err != nil {
		t.Fatal(err)
	}
	if shardA == shardB {
		t.Fatalf("both users on %s", shardA)
	}
	homeOf := map[string]*shardServer{"one": one, "two": two}
	if !hasUser(t, homeOf[shardA].db, "a") || hasUser(t, homeOf[shardB].db, "a") {
		t.Error("a is not on their shard alone")
	}
	homeOf[shardB].server.Deliver(agent.Event{Kind: agent.EventThreadDeleted, Thread: repo.ThreadRow{ID: "of-b", UserID: "b"}})
	if got := nextThread(ctx, t, b); got != "of-b" {
		t.Errorf("b received %q through the router", got)
	}
	homeOf[shardA].server.Deliver(agent.Event{Kind: agent.EventThreadDeleted, Thread: repo.ThreadRow{ID: "of-a", UserID: "a"}})
	if got := nextThread(ctx, t, a); got != "of-a" {
		t.Errorf("a received %q through the router", got)
	}

	// A connection straight to the wrong shard starts no second home for the user.
	if status := get(homeOf[shardB].http.URL, "token-a"); status != http.StatusUnauthorized {
		t.Errorf("a on b's shard: %d", status)
	}
	if hasUser(t, homeOf[shardB].db, "a") {
		t.Error("the wrong shard took a in")
	}

	// a's shard changes hands: the old process goes, clients fail for now, and the new one publishes itself.
	old := homeOf[shardA]
	old.server.each("a", func(c *conn) { c.ws.CloseNow() }) // A process that dies takes its sockets with it; this one lives on in the test.
	old.http.Close()
	if _, _, err := a.Read(ctx); err == nil {
		t.Error("a's connection outlived its shard")
	}
	if status := get(front.URL, "token-a"); status != http.StatusServiceUnavailable {
		t.Errorf("while the shard changes hands: %d", status)
	}
	standby := startShard(t, shardA, homes[shardA], catalog, authenticate)
	if err := catalog.Publish(shardA, standby.http.URL, true); err != nil {
		t.Fatal(err)
	}
	back := userClient(ctx, t, front.URL, "token-a")
	defer back.Close(websocket.StatusNormalClosure, "")
	standby.server.Deliver(agent.Event{Kind: agent.EventThreadDeleted, Thread: repo.ThreadRow{ID: "after", UserID: "a"}})
	if got := nextThread(ctx, t, back); got != "after" {
		t.Errorf("a received %q from the new holder", got)
	}
}

type oneShard struct{ address string }

func (d oneShard) ShardOf(string) (string, error) { return "s", nil }
func (d oneShard) Address(string) (string, error) { return d.address, nil }

// A shard that takes a connection and never answers does not hold the client: the router answers in time.
func TestTheRouterDoesNotWaitOnAShardThatHangs(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer hung.Close()
	defer close(release)
	rt, err := NewRouter(func(string) (string, bool) { return "a", true }, oneShard{hung.URL})
	if err != nil {
		t.Fatal(err)
	}
	rt.transport.ResponseHeaderTimeout = 100 * time.Millisecond
	front := httptest.NewServer(rt)
	defer front.Close()
	req, err := http.NewRequest(http.MethodGet, front.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer token")
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("the router answered %s", res.Status)
	}
}
