package agent_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
)

// A message may take a quarter of the model's input: far more than a few thousand characters, and never more than
// the model could be sent.
func TestAMessageIsHeldToAShareOfTheModelsInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	fake := &holdingModel{release: make(chan struct{})}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	defer close(fake.release)
	ag, _ := started(ctx, t, agent.Config{}, srv.URL)
	thread, err := ag.StartThread(repo.DefaultUser, "", "", "t", permission.Settings{})
	if err != nil {
		t.Fatal(err)
	}

	// The model takes 28,000 tokens of input, so a message gets 7,000.
	tooLong := strings.Repeat("The build log goes on. ", 2000)
	if _, err := ag.StartTurn(repo.DefaultUser, thread.ID, agent.Input{Text: tooLong}); !errors.Is(err, agent.ErrInvalidMessage) || !strings.Contains(err.Error(), "attachment") {
		t.Errorf("a message over the model's share: %v", err)
	}
	long := strings.Repeat("The task says more. ", 500)
	if _, err := ag.StartTurn(repo.DefaultUser, thread.ID, agent.Input{Text: long}); err != nil {
		t.Errorf("a %d-character message: %v", len(long), err)
	}
}
