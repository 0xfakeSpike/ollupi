package gateway

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
)

type Method func(params json.RawMessage) (any, error)

func (s *Server) Register(name string, m Method) error {
	_, method := protocol.methods[name]
	_, notification := protocol.clientNotifications[name]
	switch {
	case m == nil:
		return fmt.Errorf("gateway: method %q has no handler", name)
	case strings.TrimSpace(name) != name || name == "" || strings.HasPrefix(name, "rpc."):
		return fmt.Errorf("gateway: invalid method name %q", name)
	case method || notification:
		return fmt.Errorf("gateway: method %q is part of the protocol", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.methods[name]; dup {
		return fmt.Errorf("gateway: method %q is registered twice", name)
	}
	s.methods[name] = m
	return nil
}

func callMethod(m Method, params json.RawMessage) (any, func(), *jsonrpc.Error) {
	result, err := m(params)
	if err != nil {
		return nil, nil, serverError(err)
	}
	if result == nil {
		result = struct{}{}
	}
	return result, nil, nil
}
