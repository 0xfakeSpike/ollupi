package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent/orchestrator"
	"github.com/0xfakeSpike/ollupi/repo"
)

// placements is where users are, counting each time it is asked; "down" fails.
type placements struct {
	mu    sync.Mutex
	on    map[string]string
	asked int
}

func (p *placements) ShardOf(_ context.Context, user string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked++
	if user == "down" {
		return "", errors.New("placements down")
	}
	return p.on[user], nil
}

// A shard serves only its own users: one placed elsewhere, or nowhere, or whose shard cannot be found now, is told to
// come back later, when the router has sent them right, and leaves nothing behind.
func TestAShardServesOnlyItsOwnUsers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	where := &placements{on: map[string]string{"a": "one", "b": "two"}}
	s, err := NewServer(orchestrator.New(orchestrator.Config{DB: db}), Options{
		DB: db, Authenticate: func(token string) (string, bool) { return token, token != "" }, Shard: "one", Placements: where,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(s)
	defer h.Close()

	for _, user := range []string{"b", "nowhere", "down"} {
		req, err := http.NewRequest(http.MethodGet, h.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+user)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusServiceUnavailable || res.Header.Get("Retry-After") == "" {
			t.Errorf("%s: %s, Retry-After %q", user, res.Status, res.Header.Get("Retry-After"))
		}
	}
	userClient(ctx, t, h.URL, "a")
	userClient(ctx, t, h.URL, "a")
	users, err := db.Users()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(users, "a") || slices.ContainsFunc(users, func(u string) bool { return u == "b" || u == "nowhere" || u == "down" }) {
		t.Errorf("the shard's users: %v", users)
	}
	if where.asked != 4 {
		t.Errorf("asked where users are %d times; a, once found here, need not be asked again", where.asked)
	}
}

func TestAShardNeedsBothItsNameAndPlacements(t *testing.T) {
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, opts := range []Options{{DB: db, Token: "t", Shard: "one"}, {DB: db, Token: "t", Placements: &placements{}}} {
		if _, err := NewServer(nil, opts); err == nil {
			t.Errorf("a gateway with only one of Shard and Placements: %+v", opts)
		}
	}
}

func TestAShardsHealthCheckNeedsNoToken(t *testing.T) {
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := NewServer(nil, Options{DB: db, Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://10.0.0.7"+HealthPath, nil))
	if w.Code != http.StatusOK {
		t.Errorf("the shard answered its health check %d", w.Code)
	}
}

// reports keeps the loads a shard reports.
type reports struct {
	mu   sync.Mutex
	last map[string]int64
}

func (r *reports) ReportLoad(_ context.Context, shard string, load int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last[shard] = load
	return nil
}

func (r *reports) of(shard string) (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	load, ok := r.last[shard]
	return load, ok
}

// A shard reports its load while it serves, by the measure it is given.
func TestAShardReportsItsLoadWhileItServes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	got := &reports{last: map[string]int64{}}
	s, err := NewServer(orchestrator.New(orchestrator.Config{DB: db}), Options{
		DB: db, Authenticate: func(token string) (string, bool) { return token, token != "" },
		Shard: "one", Placements: &placements{on: map[string]string{"a": "one", "b": "one"}},
		Load: Load{Reporter: got, Measure: LoadConnections, Every: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serving, stop := context.WithCancel(ctx)
	served := make(chan error, 1)
	go func() { served <- s.Serve(serving, ln) }()
	url := "http://" + ln.Addr().String()

	waitFor := func(want int64) {
		t.Helper()
		for {
			if load, ok := got.of("one"); ok && load == want {
				return
			}
			select {
			case <-ctx.Done():
				load, _ := got.of("one")
				t.Fatalf("the shard reported %d connections, want %d", load, want)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	waitFor(0)
	userClient(ctx, t, url, "a")
	userClient(ctx, t, url, "b")
	waitFor(2)
	stop()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

func TestALoadNeedsAShardAndAKnownMeasure(t *testing.T) {
	db, err := repo.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	on := &placements{}
	for name, opts := range map[string]Options{
		"no shard":        {DB: db, Token: "t", Load: Load{Reporter: &reports{}}},
		"unknown measure": {DB: db, Token: "t", Shard: "one", Placements: on, Load: Load{Reporter: &reports{}, Measure: "cpu"}},
		"negative":        {DB: db, Token: "t", Shard: "one", Placements: on, Load: Load{Reporter: &reports{}, Every: -time.Second}},
	} {
		if _, err := NewServer(nil, opts); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	s, err := NewServer(nil, Options{DB: db, Token: "t", Shard: "one", Placements: on, Load: Load{Reporter: &reports{}}})
	if err != nil {
		t.Fatal(err)
	}
	if s.load.Measure != LoadActiveUsers || s.load.Every != time.Minute || s.load.Window != 7*24*time.Hour {
		t.Errorf("the defaults: %+v", s.load)
	}
}
