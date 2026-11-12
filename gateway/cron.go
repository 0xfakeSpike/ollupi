package gateway

import (
	"context"
	"errors"
	"log/slog"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools/cron"
)

func (c *conn) cronJobs() (*cron.Jobs, error) {
	if c.s.jobs == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeServerError, "scheduled tasks are unavailable")
	}
	return c.s.jobs, nil
}

func (c *conn) cronList(ctx context.Context, p CronListParams) (CronListResult, func(), error) {
	c.deliver.Lock()
	rows, err := c.s.db.WithContext(ctx).CronJobViews(c.user, p.ThreadID)
	if err != nil {
		c.deliver.Unlock()
		return CronListResult{}, nil, err
	}
	return CronListResult{Data: each(rows, cronJobView)}, c.deliver.Unlock, nil
}

func (s *Server) CronChanged(id, threadID string) {
	t, err := s.db.ThreadRow(threadID)
	if err != nil {
		slog.Error("thread of a changed cron task not readable", "job", id, "thread", threadID, "err", err)
		return
	}
	if c := s.current(t.UserID); c != nil {
		c.deliver.Lock()
		defer c.deliver.Unlock()
		c.cronChanged(id, threadID)
	}
}

func (c *conn) cronChanged(id, threadID string) {
	n := CronChangedNotification{ID: id, ThreadID: threadID}
	job, err := c.s.db.CronJobView(id)
	switch {
	case err == nil:
		view := cronJobView(job)
		n.Job = &view
	case !errors.Is(err, repo.ErrNotFound):
		slog.Error("changed cron task not readable", "job", id, "err", err)
		return
	}
	c.rpc.Notify(notifyCronChanged, n)
}

func (c *conn) cronCreate(ctx context.Context, p CronCreateParams) (CronJobRow, func(), error) {
	jobs, err := c.cronJobs()
	if err != nil {
		return CronJobRow{}, nil, err
	}
	if _, err := c.s.db.WithContext(ctx).ThreadOf(c.user, p.ThreadID); err != nil {
		return CronJobRow{}, nil, err
	}
	row, err := jobs.Create(p.ThreadID, p.Name, p.Spec, p.Prompt, p.Times)
	return cronJobRow(row), nil, err
}

func (c *conn) cronPause(ctx context.Context, p CronPauseParams) (CronJobRow, func(), error) {
	jobs, err := c.cronJobs()
	if err != nil {
		return CronJobRow{}, nil, err
	}
	if p.Paused == nil {
		return CronJobRow{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "paused is required")
	}
	if _, err := c.s.db.WithContext(ctx).ThreadOf(c.user, p.ThreadID); err != nil {
		return CronJobRow{}, nil, err
	}
	row, err := jobs.SetPaused(p.ThreadID, p.Name, *p.Paused)
	return cronJobRow(row), nil, err
}

func (c *conn) cronDelete(ctx context.Context, p CronDeleteParams) (Empty, func(), error) {
	jobs, err := c.cronJobs()
	if err != nil {
		return Empty{}, nil, err
	}
	if _, err := c.s.db.WithContext(ctx).ThreadOf(c.user, p.ThreadID); err != nil {
		return Empty{}, nil, err
	}
	_, err = jobs.Delete(p.ThreadID, p.Name)
	return Empty{}, nil, err
}
