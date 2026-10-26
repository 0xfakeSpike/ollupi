package gateway

import (
	"encoding/json"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/repo"
)

func (c *conn) cronRequest(method string, params json.RawMessage) (any, func(), *jsonrpc.Error) {
	jobs := c.s.jobs
	if jobs == nil {
		return nil, nil, jsonrpc.Errorf(jsonrpc.CodeServerError, "scheduled tasks are unavailable")
	}
	var p cronJobParams
	if rpcErr := jsonrpc.DecodeParams(params, &p); rpcErr != nil {
		return nil, nil, rpcErr
	}
	if strings.TrimSpace(p.ThreadID) == "" {
		return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "threadId is required")
	}
	if method != methodCronList && strings.TrimSpace(p.Name) == "" {
		return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "name is required")
	}
	var result any
	var err error
	switch method {
	case methodCronList:
		var rows []repo.CronJobRow
		rows, err = jobs.List(p.ThreadID)
		if rows == nil {
			rows = []repo.CronJobRow{}
		}
		result = struct {
			Data []repo.CronJobRow `json:"data"`
		}{rows}
	case methodCronCreate:
		result, err = jobs.Create(p.ThreadID, p.Name, p.Spec, p.Prompt)
	case methodCronPause:
		if p.Paused == nil {
			return nil, nil, jsonrpc.Errorf(jsonrpc.CodeInvalidParams, "paused is required")
		}
		result, err = jobs.SetPaused(p.ThreadID, p.Name, *p.Paused)
	case methodCronDelete:
		err = jobs.Delete(p.ThreadID, p.Name)
		result = struct{}{}
	}
	if err != nil {
		return nil, nil, serverError(err)
	}
	return result, nil, nil
}
