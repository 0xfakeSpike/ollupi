package gateway

import (
	"context"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

func (c *conn) cronList(ctx context.Context, p CronListParams) (CronListResult, func(), error) {
	c.rpc.HoldUntilReplied()
	rows, err := c.s.service.CronJobs(ctx, c.user, p.ThreadID)
	if err != nil {
		return CronListResult{}, nil, err
	}
	return CronListResult{Data: each(rows, cronJobView)}, nil, nil
}

func (c *conn) cronCreate(ctx context.Context, p CronCreateParams) (CronJobRow, func(), error) {
	row, err := c.s.service.CreateCronJob(c.user, p.ThreadID, p.Name, p.Spec, p.Prompt, p.Times)
	return cronJobRow(row), nil, err
}

func (c *conn) cronPause(ctx context.Context, p CronPauseParams) (CronJobRow, func(), error) {
	if p.Paused == nil {
		return CronJobRow{}, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "paused is required")
	}
	row, err := c.s.service.PauseCronJob(c.user, p.ThreadID, p.Name, *p.Paused)
	return cronJobRow(row), nil, err
}

func (c *conn) cronDelete(ctx context.Context, p CronDeleteParams) (Empty, func(), error) {
	return Empty{}, nil, c.s.service.DeleteCronJob(c.user, p.ThreadID, p.Name)
}
