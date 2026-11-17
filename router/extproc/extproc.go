package extproc

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/0xfakeSpike/ollupi/router"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
)

const ShardHeader = "x-ollupi-shard"

type Callout struct {
	extprocv3.UnimplementedExternalProcessorServer
	locator *router.Locator
}

func New(locator *router.Locator) *Callout {
	return &Callout{locator: locator}
}

func (c *Callout) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var res extprocv3.ProcessingResponse
		switch r := req.Request.(type) {
		case *extprocv3.ProcessingRequest_RequestHeaders:
			res.Response = &extprocv3.ProcessingResponse_RequestHeaders{RequestHeaders: &extprocv3.HeadersResponse{Response: c.place(stream.Context(), r.RequestHeaders.GetHeaders())}}
		case *extprocv3.ProcessingRequest_ResponseHeaders:
			res.Response = &extprocv3.ProcessingResponse_ResponseHeaders{ResponseHeaders: &extprocv3.HeadersResponse{}}
		case *extprocv3.ProcessingRequest_RequestBody:
			res.Response = &extprocv3.ProcessingResponse_RequestBody{RequestBody: &extprocv3.BodyResponse{}}
		case *extprocv3.ProcessingRequest_ResponseBody:
			res.Response = &extprocv3.ProcessingResponse_ResponseBody{ResponseBody: &extprocv3.BodyResponse{}}
		case *extprocv3.ProcessingRequest_RequestTrailers:
			res.Response = &extprocv3.ProcessingResponse_RequestTrailers{RequestTrailers: &extprocv3.TrailersResponse{}}
		case *extprocv3.ProcessingRequest_ResponseTrailers:
			res.Response = &extprocv3.ProcessingResponse_ResponseTrailers{ResponseTrailers: &extprocv3.TrailersResponse{}}
		default:
			continue
		}
		if err := stream.Send(&res); err != nil {
			return err
		}
	}
}

func (c *Callout) place(ctx context.Context, headers *corev3.HeaderMap) *extprocv3.CommonResponse {
	header := http.Header{}
	for _, h := range headers.GetHeaders() {
		value := h.GetValue()
		if raw := h.GetRawValue(); len(raw) > 0 {
			value = string(raw)
		}
		header.Add(h.GetKey(), value)
	}
	shard, _, err := c.locator.Locate(ctx, header)
	if err != nil {
		return &extprocv3.CommonResponse{HeaderMutation: &extprocv3.HeaderMutation{RemoveHeaders: []string{ShardHeader}}}
	}
	return &extprocv3.CommonResponse{HeaderMutation: &extprocv3.HeaderMutation{SetHeaders: []*corev3.HeaderValueOption{{
		Header:       &corev3.HeaderValue{Key: ShardHeader, RawValue: []byte(shard)},
		AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
	}}}}
}
