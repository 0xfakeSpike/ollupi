package cron

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/user"
)

type Submit func(tx store.Tx, thread, prompt, job string) error

func Run(ctx context.Context, st store.Store, cal Calendar, submit Submit) error {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if err := sendDue(ctx, st, cal, submit, time.Now().UTC()); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func sendDue(ctx context.Context, st store.Store, cal Calendar, submit Submit, now time.Time) error {
	var due []user.CronJob
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

func send(tx store.Tx, cal Calendar, submit Submit, due user.CronJob, now time.Time, sending bool) error {
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
	waiting := slices.ContainsFunc(v.Behind(), func(t conversation.Turn) bool { return t.CronJob == job.ID })
	sent := sending && !waiting
	if sent {
		if err := submit(tx, job.Thread, job.Prompt, job.ID); err != nil {
			return errSubmit{err}
		}
	}
	next, done, err := cal.Ran(job, now, sent)
	if err != nil {
		return err
	}
	if done {
		return tx.DeleteCronJob(job.ID)
	}
	return tx.PutCronJob(next)
}
