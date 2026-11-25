package store

import "sync"

type Feed struct {
	mu     sync.RWMutex
	byUser map[string]map[*Subscription]struct{}
	all    map[*Subscription]struct{}

	queue   chan batch
	stop    chan struct{}
	stopped chan struct{}
	closing sync.Once
}

type batch struct {
	events []Event
	told   func()
}

type Subscription struct {
	feed *Feed
	user string
	all  bool
	fn   func(Event)
}

const queued = 1024

func NewFeed() *Feed {
	f := &Feed{byUser: map[string]map[*Subscription]struct{}{}, all: map[*Subscription]struct{}{},
		queue: make(chan batch, queued), stop: make(chan struct{}), stopped: make(chan struct{})}
	go f.tell()
	return f
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

func (f *Feed) Publish(events []Event, told func()) {
	select {
	case f.queue <- batch{events: events, told: told}:
	case <-f.stop:
	}
}

func (f *Feed) Close() {
	f.closing.Do(func() { close(f.stop) })
	<-f.stopped
}

func (f *Feed) tell() {
	defer close(f.stopped)
	for {
		select {
		case b := <-f.queue:
			f.deliver(b)
		case <-f.stop:
			for {
				select {
				case b := <-f.queue:
					f.deliver(b)
				default:
					return
				}
			}
		}
	}
}

func (f *Feed) deliver(b batch) {
	f.mu.RLock()
	for _, e := range b.events {
		if e.User == "" {
			// The deployment's: every user's subscribers hear it.
			for _, subs := range f.byUser {
				for s := range subs {
					s.fn(e)
				}
			}
		}
		for s := range f.byUser[e.User] {
			s.fn(e)
		}
		for s := range f.all {
			s.fn(e)
		}
	}
	f.mu.RUnlock()
	if b.told != nil {
		b.told()
	}
}
