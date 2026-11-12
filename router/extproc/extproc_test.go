package extproc

import (
	"context"
	"net"
	"testing"

	"github.com/0xfakeSpike/ollupi/router"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type shards map[string]string

func (s shards) Assign(_ context.Context, user string) (string, error) { return s[user], nil }
func (shards) Address(context.Context, string) (string, error)         { return "", nil }

// callout runs the callout on a gRPC server in memory and opens one stream to it, as a load balancer would.
func callout(t *testing.T) extprocv3.ExternalProcessor_ProcessClient {
	t.Helper()
	locator, err := router.NewLocator(func(token string) (string, bool) {
		user, ok := map[string]string{"token-a": "a"}[token]
		return user, ok
	}, shards{"a": "17"})
	if err != nil {
		t.Fatal(err)
	}
	ln := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	extprocv3.RegisterExternalProcessorServer(srv, New(locator))
	go srv.Serve(ln)
	t.Cleanup(srv.Stop)
	cc, err := grpc.NewClient("passthrough:///callout", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return ln.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	stream, err := extprocv3.NewExternalProcessorClient(cc).Process(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

// handshake sends a handshake's headers, as raw values the way the load balancer sends them, and returns what the
// callout does to them.
func handshake(t *testing.T, stream extprocv3.ExternalProcessor_ProcessClient, headers map[string]string) *extprocv3.HeaderMutation {
	t.Helper()
	var hm corev3.HeaderMap
	for k, v := range headers {
		hm.Headers = append(hm.Headers, &corev3.HeaderValue{Key: k, RawValue: []byte(v)})
	}
	if err := stream.Send(&extprocv3.ProcessingRequest{Request: &extprocv3.ProcessingRequest_RequestHeaders{
		RequestHeaders: &extprocv3.HttpHeaders{Headers: &hm, EndOfStream: true}}}); err != nil {
		t.Fatal(err)
	}
	res, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	return res.GetRequestHeaders().GetResponse().GetHeaderMutation()
}

func TestTheCalloutPlacesAHandshakeOnItsUsersShard(t *testing.T) {
	stream := callout(t)
	for name, headers := range map[string]map[string]string{
		"bearer":      {"authorization": "Bearer token-a", ShardHeader: "9"},
		"subprotocol": {"sec-websocket-protocol": "ollupi.token, token-a"},
	} {
		set := handshake(t, stream, headers).GetSetHeaders()
		if len(set) != 1 || set[0].GetHeader().GetKey() != ShardHeader || string(set[0].GetHeader().GetRawValue()) != "17" ||
			set[0].GetAppendAction() != corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD {
			t.Errorf("%s: set %v", name, set)
		}
	}
}

// A handshake the callout cannot place goes on without a shard of the client's choosing, to the router.
func TestAHandshakeOfNoOneLosesTheShardItNamed(t *testing.T) {
	m := handshake(t, callout(t), map[string]string{"authorization": "Bearer nobody", ShardHeader: "17"})
	if len(m.GetSetHeaders()) != 0 || len(m.GetRemoveHeaders()) != 1 || m.GetRemoveHeaders()[0] != ShardHeader {
		t.Errorf("did %v", m)
	}
}
