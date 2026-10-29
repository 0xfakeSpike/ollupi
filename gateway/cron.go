package gateway

import (
	"errors"
	"log"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools/cron"
)

func (c *conn) cronJobs(threadID, name string, named bool) (*cron.Jobs, error) {
	if c.s.jobs == nil {
		return nil, jsonrpc.Errorf(jsonrpc.CodeServerError, "scheduled tasks are unavailable")
	}
	if named && strings.TrimSpace(threadID) == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "threadId is required")
	}
	if named && strings.TrimSpace(name) == "" {
		return nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "name is required")
	}
	return c.s.jobs, nil
}

func (c *conn) cronList(p CronListParams) (CronListResult, func(), error) {
	jobs, err := c.cronJobs(p.ThreadID, "", false)
	if err != nil {
		return CronListResult{}, nil, err
	}
	c.s.deliver.Lock()
	rows, err := jobs.List(p.ThreadID)
	if err != nil {
		c.s.deliver.Unlock()
		return CronListResult{}, nil, err
	}
	return CronListResult{Data: nonNil(rows)}, c.s.deliver.Unlock, nil
}

func (s *Server) CronChanged(id, threadID string) {
	s.deliver.Lock()
	defer s.deliver.Unlock()
	c := s.current()
	if c == nil || s.jobs == nil {
		return
	}
	n := CronChangedNotification{ID: id, ThreadID: threadID}
	job, err := s.jobs.Job(id)
	switch {
	case err == nil:
		n.Job = &job
	case !errors.Is(err, repo.ErrNotFound):
		log.Printf("gateway: cron task %s changed but cannot be read: %v", id, err)
		return
	}
	c.rpc.Notify(notifyCronChanged, n)
}

func (c *conn) cronCreate(p CronCreateParams) (repo.CronJobRow, func(), error) {
	jobs, err := c.cronJobs(p.ThreadID, p.Name, true)
	if err != nil {
		return repo.CronJobRow{}, nil, err
	}
	row, err := jobs.Create(p.ThreadID, p.Name, p.Spec, p.Prompt)
	return row, nil, err
}

func (c *conn) cronPause(p CronPauseParams) (repo.CronJobRow, func(), error) {
	jobs, err := c.cronJobs(p.ThreadID, p.Name, true)
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
	jobs, err := c.cronJobs(p.ThreadID, p.Name, true)
	if err != nil {
		return Empty{}, nil, err
	}
	_, err = jobs.Delete(p.ThreadID, p.Name)
	return Empty{}, nil, err
}

func (s *Server) threadJobs(threadID string) []repo.CronJobView {
	if s.jobs == nil {
		return nil
	}
	all, err := s.jobs.List("")
	if err != nil {
		log.Printf("gateway: the tasks of thread %s cannot be listed: %v", threadID, err)
		return nil
	}
	var out []repo.CronJobView
	for _, j := range all {
		if j.ThreadID == threadID || j.ParentThreadID == threadID {
			out = append(out, j)
		}
	}
	return out
}

func (s *Server) cronJobsChanged(jobs []repo.CronJobView) {
	for _, j := range jobs {
		s.CronChanged(j.ID, j.ThreadID)
	}
}
