package gateway

import (
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

func (c *conn) cronList(p CronListParams) (CronListResult, func(), error) {
	c.s.deliver.Lock()
	rows, err := c.s.db.CronJobViews(p.ThreadID)
	if err != nil {
		c.s.deliver.Unlock()
		return CronListResult{}, nil, err
	}
	return CronListResult{Data: nonNil(rows)}, c.s.deliver.Unlock, nil
}

func (s *Server) CronChanged(id, threadID string) {
	s.deliver.Lock()
	defer s.deliver.Unlock()
	if c := s.current(); c != nil {
		c.cronChanged(id, threadID)
	}
}

func (c *conn) cronChanged(id, threadID string) {
	n := CronChangedNotification{ID: id, ThreadID: threadID}
	job, err := c.s.db.CronJobView(id)
	switch {
	case err == nil:
		n.Job = &job
	case !errors.Is(err, repo.ErrNotFound):
		slog.Error("changed cron task not readable", "job", id, "err", err)
		return
	}
	c.rpc.Notify(notifyCronChanged, n)
}

func (c *conn) cronCreate(p CronCreateParams) (repo.CronJobRow, func(), error) {
	jobs, err := c.cronJobs()
	if err != nil {
		return repo.CronJobRow{}, nil, err
	}
	row, err := jobs.Create(p.ThreadID, p.Name, p.Spec, p.Prompt)
	return row, nil, err
}

func (c *conn) cronPause(p CronPauseParams) (repo.CronJobRow, func(), error) {
	jobs, err := c.cronJobs()
	if err != nil {
		return repo.CronJobRow{}, nil, err
	}
	if p.Paused == nil {
		return repo.CronJobRow{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "paused is required")
	}
	row, err := jobs.SetPaused(p.ThreadID, p.Name, *p.Paused)
	return row, nil, err
}

func (c *conn) cronDelete(p CronDeleteParams) (Empty, func(), error) {
	jobs, err := c.cronJobs()
	if err != nil {
		return Empty{}, nil, err
	}
	_, err = jobs.Delete(p.ThreadID, p.Name)
	return Empty{}, nil, err
}
