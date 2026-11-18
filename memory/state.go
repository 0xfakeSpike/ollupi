package memory

import (
	"time"
)

type Cursor struct {
	Seq    int64  `json:"seq"`
	Thread string `json:"thread,omitempty"`
	Turn   string `json:"turn,omitempty"`
}

type FailureCode string

const (
	ModelRequest    FailureCode = "model_request"
	InvalidResponse FailureCode = "invalid_response"
	BadEvidence     FailureCode = "evidence"
	Storage         FailureCode = "storage"
)

var FailureCodes = []FailureCode{ModelRequest, InvalidResponse, BadEvidence, Storage}

type Failure struct {
	Code  FailureCode `json:"code"`
	Error string      `json:"error"`
}

type Skip struct {
	At     time.Time `json:"at"`
	Thread string    `json:"threadId"`
	Turn   string    `json:"turnId"`
	Failure
}

type State struct {
	Cursor      Cursor     `json:"-"`
	Attempts    int        `json:"attempts,omitempty"`
	NextAttempt *time.Time `json:"nextAttempt,omitempty"`
	Skipped     int64      `json:"skipped"`
	LastSuccess *time.Time `json:"lastSuccess,omitempty"`
	LastFailure *Failure   `json:"lastFailure,omitempty"`
	LastSkip    *Skip      `json:"lastSkip,omitempty"`
}

const MaxAttempts = 10

func (s State) Due(now time.Time) bool { return s.NextAttempt == nil || !now.Before(*s.NextAttempt) }

func (s State) Read(c Cursor, now time.Time) State {
	s.Cursor, s.Attempts, s.NextAttempt, s.LastSuccess, s.LastFailure = c, 0, nil, &now, nil
	return s
}

func (s State) Failed(c Cursor, f Failure, now time.Time) State {
	f.Error = clip(f.Error, maxReasonRunes)
	if s.Attempts+1 >= MaxAttempts {
		s.Cursor, s.Attempts, s.NextAttempt, s.LastFailure = c, 0, nil, nil
		s.Skipped++
		s.LastSkip = &Skip{At: now, Thread: c.Thread, Turn: c.Turn, Failure: f}
		return s
	}
	next := now.Add(time.Duration(min(60, 1<<min(s.Attempts, 6))) * time.Minute)
	s.Attempts, s.NextAttempt, s.LastFailure = s.Attempts+1, &next, &f
	return s
}
