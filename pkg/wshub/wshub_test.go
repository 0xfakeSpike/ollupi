package wshub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var quick = Options{PerKey: 2, Rate: 1000, Burst: 100, RetryWithin: 3 * time.Second, PingEvery: 20 * time.Millisecond, PongWithin: 50 * time.Millisecond}

// start serves each connection under key "k", reading it until it fails, and says when each Serve returned.
func start(t *testing.T, h *Hub[string]) (string, <-chan struct{}) {
	t.Helper()
	ended := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.Admit(w) {
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		h.Serve(r.Context(), "k", ws, r.URL.Query().Get("name"), func(ctx context.Context) error {
			for {
				if _, _, err := ws.Read(ctx); err != nil {
					return err
				}
			}
		})
		ended <- struct{}{}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), ended
}

func dial(ctx context.Context, t *testing.T, url, name string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(ctx, url+"?name="+name, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	return ws
}

func held(h *Hub[string]) []string {
	var names []string
	h.Each("k", func(name string) { names = append(names, name) })
	return names
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
	}
}

func TestOptionsMustBound(t *testing.T) {
	bad := quick
	bad.PerKey = 0
	if _, err := New[string](bad); err == nil {
		t.Error("a hub that holds no connection per key")
	}
	bad = quick
	bad.RetryWithin = time.Millisecond
	if _, err := New[string](bad); err == nil {
		t.Error("a Retry-After under a second")
	}
}

func TestConnectionsBeyondTheRateAreToldWhenToComeBack(t *testing.T) {
	opts := quick
	opts.Rate, opts.Burst = 0.001, 1
	h, err := New[string](opts)
	if err != nil {
		t.Fatal(err)
	}
	if w := httptest.NewRecorder(); !h.Admit(w) {
		t.Fatalf("the first connection was refused: %d", w.Code)
	}
	w := httptest.NewRecorder()
	if h.Admit(w) {
		t.Fatal("a connection beyond the burst was admitted")
	}
	after, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if w.Code != http.StatusServiceUnavailable || err != nil || after < 1 || after > 3 {
		t.Errorf("refused with %d and Retry-After %q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestANewerConnectionTakesTheOldestsPlace(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	h, err := New[string](quick)
	if err != nil {
		t.Fatal(err)
	}
	url, _ := start(t, h)
	first := dial(ctx, t, url, "first")
	go first.Read(ctx)
	eventually(t, "the first connection was not held", func() bool { return len(held(h)) == 1 })
	for _, name := range []string{"second", "third"} {
		ws := dial(ctx, t, url, name)
		go func() {
			for {
				if _, _, err := ws.Read(ctx); err != nil {
					return
				}
			}
		}()
	}
	eventually(t, "the oldest connection was not given up", func() bool {
		names := held(h)
		return len(names) == 2 && names[0] == "second" && names[1] == "third"
	})
}

func TestAPeerThatStopsAnsweringPingsIsDropped(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	h, err := New[string](quick)
	if err != nil {
		t.Fatal(err)
	}
	url, ended := start(t, h)

	// A client answers pings only while it reads.
	answering := dial(ctx, t, url, "answering")
	go func() {
		for {
			if _, _, err := answering.Read(ctx); err != nil {
				return
			}
		}
	}()
	dial(ctx, t, url, "silent")
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal("the silent peer's connection was kept")
	}
	time.Sleep(10 * quick.PingEvery)
	if names := held(h); len(names) != 1 || names[0] != "answering" {
		t.Errorf("held after the silent peer missed its pongs: %v", names)
	}
}
