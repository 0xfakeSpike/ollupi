package llm

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestATryEndsWhenTheProviderGoesQuiet(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/headers" {
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {}\n\n"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: idleTransport{base: http.DefaultTransport, idle: 200 * time.Millisecond}}

	started := time.Now()
	if _, err := client.Get(srv.URL + "/headers"); !errors.Is(err, errIdle) {
		t.Errorf("no headers: %v, want %v", err, errIdle)
	}
	res, err := client.Get(srv.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if !errors.Is(err, errIdle) || string(body) != "data: {}\n\n" {
		t.Errorf("a stream that stalls: read %q, %v; want the event, then %v", body, err, errIdle)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("giving up took %s", elapsed)
	}
}

func TestAStreamThatKeepsSendingOutlastsTheIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for range 10 {
			w.Write([]byte("data: {}\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: idleTransport{base: http.DefaultTransport, idle: 200 * time.Millisecond}}
	res, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if body, err := io.ReadAll(res.Body); err != nil || len(body) != 100 {
		t.Errorf("read %d bytes, %v; want the whole stream", len(body), err)
	}
}
