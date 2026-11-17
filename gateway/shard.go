package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// A shard asks Placements before it serves a user it has not served yet, so that a connection that reaches the wrong
// shard cannot start a second home for its user. Placing users is the router's job, before a connection arrives.
type Placements interface {
	// ShardOf returns "" when user is placed nowhere yet.
	ShardOf(ctx context.Context, user string) (string, error)
}

// All shards of a deployment report by the same measure, since the router compares them. Users stay on the shard they
// are first placed on, so a measure of how busy a shard is over days places them better than one of this minute.
type LoadMeasure string

const (
	LoadActiveUsers LoadMeasure = "activeUsers"
	LoadTurns       LoadMeasure = "turns"
	LoadConnections LoadMeasure = "connections"
)

type LoadReporter interface {
	ReportLoad(ctx context.Context, shard string, load int64) error
}

type Load struct {
	Reporter LoadReporter
	Measure  LoadMeasure
	Every    time.Duration
	Window   time.Duration
}

const (
	defaultLoadEvery  = time.Minute
	defaultLoadWindow = 7 * 24 * time.Hour
)

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

// When a report fails the next one tries again: the router places by the last load it heard meanwhile.
func (s *Server) reportLoad(ctx context.Context) {
	tick := time.NewTicker(s.load.Every)
	defer tick.Stop()
	for {
		if load, err := s.measureLoad(); err == nil {
			reportCtx, cancel := context.WithTimeout(ctx, s.load.Every)
			s.load.Reporter.ReportLoad(reportCtx, s.shard, load)
			cancel()
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
		return s.service.TurnsUnderWay()
	case LoadConnections:
		return int64(s.hub.Count()), nil
	default:
		return s.service.ActiveUsers(time.Now().Add(-s.load.Window))
	}
}

// mine refuses a user not on this shard for now: the router placed it by what it knew, and a client that comes back
// reaches the right shard once that is put right.
func (s *Server) mine(w http.ResponseWriter, r *http.Request, user string) bool {
	if s.placements == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(r.Context(), lookupTimeout)
	defer cancel()
	shard, err := s.placements.ShardOf(ctx, user)
	switch {
	case err != nil:
	case shard != s.shard:
	default:
		return true
	}
	s.hub.Refuse(w, "this user is not on this shard now; try again")
	return false
}
