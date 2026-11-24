package cron

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
)

type Submit func(tx store.Tx, thread, prompt, job string) error

const clockCheck = time.Minute

func Run(ctx context.Context, st store.Store, cal Calendar, submit Submit) error {
	changed := make(chan struct{}, 1)
	sub := st.Feed().SubscribeAll(func(e store.Event) {
		jobChanged := e.Owned != nil && (e.Owned.CronJob != nil || e.Owned.CronJobDeleted != "")
		if !jobChanged {
			return
		}
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	defer sub.Close()
	for {
		if err := sendDue(ctx, st, cal, submit, time.Now().UTC()); err != nil {
			return err
		}
		var next time.Time
		var scheduled bool
		if err := st.Read(ctx, func(r store.Reader) (err error) { next, scheduled, err = r.NextRun(); return err }); err != nil {
			return err
		}
		var clock <-chan time.Time
		if scheduled {
			wait := time.Until(next)
			if wait <= 0 || wait > clockCheck {
				wait = clockCheck
			}
			clock = time.After(wait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		case <-clock:
		}
	}
}

func sendDue(ctx context.Context, st store.Store, cal Calendar, submit Submit, now time.Time) error {
	var due []account.CronJob
	if err := st.Read(ctx, func(r store.Reader) (err error) { due, err = r.Due(now); return err }); err != nil {
		return err
	}
	for _, job := range due {
		err := st.Update(ctx, func(tx store.Tx) error { return send(tx, cal, submit, job, now, true) })
		var invalid errSubmit
		if errors.As(err, &invalid) {
			err = st.Update(ctx, func(tx store.Tx) error { return send(tx, cal, submit, job, now, false) })
		}
		if err != nil {
			return err
		}
	}
	return nil
}

type errSubmit struct{ error }

func send(tx store.Tx, cal Calendar, submit Submit, due account.CronJob, now time.Time, sending bool) error {
	job, err := tx.CronJob(due.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if job.Paused || !job.NextRun.Equal(due.NextRun) {
		return nil
	}
	v, err := tx.View(job.Thread)
	if err != nil {
		return err
	}
	waiting := slices.ContainsFunc(v.Queued(), func(t conversation.Turn) bool { return t.CronJob == job.ID })
	sent := sending && !waiting
	if sent {
		if err := submit(tx, job.Thread, job.Prompt, job.ID); err != nil {
			return errSubmit{err}
		}
	}
	next, done, err := cal.Advance(job, now, sent)
	if err != nil {
		return err
	}
	if done {
		return tx.DeleteCronJob(job.ID)
	}
	return tx.PutCronJob(next)
}
