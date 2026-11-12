package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
)

// Placements says which shard each user is on, where users are spread over shards. A shard asks it before it serves a
// user it has not served yet, so that a connection that reaches the wrong shard cannot start a second home for its
// user. Placing users is not the shard's to do: the router does it before a connection arrives. Where the answer is
// kept, a database apart from the shards', Redis or a service of the router's, is the deployment's choice.
type Placements interface {
	// ShardOf says which shard user is on, or "" when they are placed nowhere yet.
	ShardOf(ctx context.Context, user string) (string, error)
}

// LoadMeasure is what a shard's load counts. All shards of a deployment report by the same one, for the router compares
// them. Users stay on the shard they are first placed on, so a measure of how busy a shard is over days places them
// better than one of this minute.
type LoadMeasure string

const (
	// LoadActiveUsers counts the users with a conversation that moved within Load.Window.
	LoadActiveUsers LoadMeasure = "activeUsers"
	// LoadTurns counts the turns the shard has to run now: queued, ready, running or waiting on a subagent.
	LoadTurns LoadMeasure = "turns"
	// LoadConnections counts the clients' connections the shard holds now.
	LoadConnections LoadMeasure = "connections"
)

// LoadReporter takes each shard's load, for the router to place new users by; router.Catalog is one.
type LoadReporter interface {
	ReportLoad(ctx context.Context, shard string, load int64) error
}

// Load is how a shard tells the router how busy it is. It reports while the gateway serves, once at the start and then
// every so often.
type Load struct {
	// Reporter takes the reports; without one the shard reports nothing.
	Reporter LoadReporter
	// Measure is what the load counts, LoadActiveUsers by default.
	Measure LoadMeasure
	// Every is how often the shard reports, a minute by default.
	Every time.Duration
	// Window is how far back LoadActiveUsers looks, seven days by default.
	Window time.Duration
}

const (
	defaultLoadEvery  = time.Minute
	defaultLoadWindow = 7 * 24 * time.Hour
)

// withDefaults is l with what it leaves out filled in, or an error for what it gets wrong.
func (l Load) withDefaults() (Load, error) {
	if l.Reporter == nil {
		return l, nil
	}
	if l.Measure == "" {
		l.Measure = LoadActiveUsers
	}
	if !slices.Contains([]LoadMeasure{LoadActiveUsers, LoadTurns, LoadConnections}, l.Measure) {
		return l, fmt.Errorf("gateway: unknown load measure %q", l.Measure)
	}
	if l.Every < 0 || l.Window < 0 {
		return l, errors.New("gateway: Load.Every and Load.Window cannot be negative")
	}
	if l.Every == 0 {
		l.Every = defaultLoadEvery
	}
	if l.Window == 0 {
		l.Window = defaultLoadWindow
	}
	return l, nil
}

// reportLoad reports the shard's load until ctx ends. A report that fails is logged and the next one tries again: the
// router places by the last load it heard meanwhile.
func (s *Server) reportLoad(ctx context.Context) {
	tick := time.NewTicker(s.load.Every)
	defer tick.Stop()
	for {
		load, err := s.measureLoad()
		if err == nil {
			reportCtx, cancel := context.WithTimeout(ctx, s.load.Every)
			err = s.load.Reporter.ReportLoad(reportCtx, s.shard, load)
			cancel()
		}
		if err != nil && ctx.Err() == nil {
			slog.Warn("shard load not reported", "shard", s.shard, "measure", s.load.Measure, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (s *Server) measureLoad() (int64, error) {
	switch s.load.Measure {
	case LoadTurns:
		return s.db.TurnsUnderWay()
	case LoadConnections:
		return int64(s.hub.Count()), nil
	default:
		return s.db.ActiveUsers(time.Now().Add(-s.load.Window))
	}
}
