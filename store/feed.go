package store

import "sync"

// Feed hands each event to the subscribers of its user, and to those of every user, in the goroutine that publishes
// it, so that a subscriber that queues what it hears keeps the order it was published in: a commit's events in the
// order of its commits. A subscriber must not block and must not write to the store.
type Feed struct {
	mu     sync.RWMutex
	byUser map[string]map[*Subscription]struct{}
	all    map[*Subscription]struct{}
}

type Subscription struct {
	feed *Feed
	user string
	all  bool
	fn   func(Event)
}

func NewFeed() *Feed {
	return &Feed{byUser: map[string]map[*Subscription]struct{}{}, all: map[*Subscription]struct{}{}}
}

func (f *Feed) Subscribe(user string, fn func(Event)) *Subscription {
	s := &Subscription{feed: f, user: user, fn: fn}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.byUser[user] == nil {
		f.byUser[user] = map[*Subscription]struct{}{}
	}
	f.byUser[user][s] = struct{}{}
	return s
}

func (f *Feed) SubscribeAll(fn func(Event)) *Subscription {
	s := &Subscription{feed: f, all: true, fn: fn}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.all[s] = struct{}{}
	return s
}

func (s *Subscription) Close() {
	f := s.feed
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.all {
		delete(f.all, s)
		return
	}
	delete(f.byUser[s.user], s)
	if len(f.byUser[s.user]) == 0 {
		delete(f.byUser, s.user)
	}
}

// Publish is the store's, after a commit, and a runner's, for a delta.
func (f *Feed) Publish(e Event) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for s := range f.byUser[e.User] {
		s.fn(e)
	}
	for s := range f.all {
		s.fn(e)
	}
}
