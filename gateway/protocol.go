package gateway

//go:generate go test -run ^TestProtocolSchema$ . -update

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"reflect"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/mcp"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
)

const (
	errNotInitialized    = -32002
	errNotFound          = -32004
	errNoActiveTurn      = -32010
	errTurnMismatch      = -32011
	errNoSuchRequest     = -32012
	errSubagentThread    = -32013
	errWorkspaceArchived = -32014
)

const (
	methodInitialize  = "initialize"
	methodInitialized = "initialized"
)

const (
	notifyThreadStarted       = "thread/started"
	notifyTurnQueued          = "turn/queued"
	notifyTurnStarted         = "turn/started"
	notifyItemStarted         = "item/started"
	notifyItemCompleted       = "item/completed"
	notifyAgentMessageDelta   = "item/agentMessage/delta"
	notifyReasoningDelta      = "item/reasoning/textDelta"
	notifyTurnCompleted       = "turn/completed"
	notifyThreadStatusChanged = "thread/status/changed"
	notifyWorkspaceChanged    = "workspace/changed"
	notifyError               = "error"
)

const (
	requestApproval  = "item/toolCall/requestApproval"
	requestUserInput = "item/tool/requestUserInput"
)

// protocol declares every message of the gateway. Call dispatches client
// methods from it, and protocol.schema.json is generated from it, so the
// contract the UI builds against is the one the gateway serves.
var protocol = struct {
	methods             map[string]method
	notifications       map[string]reflect.Type
	serverRequests      map[string]serverRequest
	clientNotifications map[string]reflect.Type
	errors              map[int]string
}{
	methods: map[string]method{
		methodInitialize:              handle((*conn).initialize),
		"thread/start":                handle((*conn).threadStart),
		"thread/resume":               handle(threadPage(true)),
		"thread/read":                 handle(threadPage(false)),
		"thread/list":                 handle((*conn).threadList),
		"thread/updatePermissions":    handle((*conn).threadPermissions),
		"turn/start":                  handle((*conn).turnStart),
		"turn/steer":                  handle((*conn).turnSteer),
		"turn/interrupt":              handle((*conn).turnInterrupt),
		"model/list":                  handle((*conn).modelList),
		"tool/list":                   handle((*conn).toolList),
		"mcpServer/list":              handle((*conn).mcpServerList),
		"workspace/list":              handle((*conn).workspaceList),
		"workspace/add":               handle((*conn).workspaceAdd),
		"workspace/rename":            handle((*conn).workspaceRename),
		"workspace/archive":           handle((*conn).workspaceArchive),
		"workspace/updatePermissions": handle((*conn).workspacePermissions),
		"cron/list":                   handle((*conn).cronList),
		"cron/create":                 handle((*conn).cronCreate),
		"cron/pause":                  handle((*conn).cronPause),
		"cron/delete":                 handle((*conn).cronDelete),
		"memory/list":                 handle((*conn).memoryList),
		"memory/status":               handle((*conn).memoryStatus),
		"memory/update":               handle((*conn).memoryUpdate),
		"memory/forget":               handle((*conn).memoryForget),
	},
	notifications: map[string]reflect.Type{
		notifyThreadStarted:       reflect.TypeFor[ThreadResult](),
		notifyTurnQueued:          reflect.TypeFor[TurnNotification](),
		notifyTurnStarted:         reflect.TypeFor[TurnNotification](),
		notifyItemStarted:         reflect.TypeFor[ItemNotification](),
		notifyItemCompleted:       reflect.TypeFor[ItemNotification](),
		notifyAgentMessageDelta:   reflect.TypeFor[DeltaNotification](),
		notifyReasoningDelta:      reflect.TypeFor[DeltaNotification](),
		notifyTurnCompleted:       reflect.TypeFor[TurnNotification](),
		notifyThreadStatusChanged: reflect.TypeFor[ThreadStatusNotification](),
		notifyWorkspaceChanged:    reflect.TypeFor[WorkspaceResult](),
		notifyError:               reflect.TypeFor[ErrorNotification](),
	},
	serverRequests: map[string]serverRequest{
		requestApproval:  {reflect.TypeFor[ApprovalRequestParams](), reflect.TypeFor[ApprovalResponse]()},
		requestUserInput: {reflect.TypeFor[UserInputRequestParams](), reflect.TypeFor[UserInputResponse]()},
	},
	clientNotifications: map[string]reflect.Type{
		methodInitialized: reflect.TypeFor[Empty](),
	},
	errors: map[int]string{
		jsonrpc.CodeParseError:     "parseError",
		jsonrpc.CodeInvalidRequest: "invalidRequest",
		jsonrpc.CodeMethodNotFound: "methodNotFound",
		jsonrpc.CodeInvalidParams:  "invalidParams",
		jsonrpc.CodeInternalError:  "internalError",
		jsonrpc.CodeServerError:    "serverError",
		errNotInitialized:          "notInitialized",
		errNotFound:                "notFound",
		errNoActiveTurn:            "noActiveTurn",
		errTurnMismatch:            "turnMismatch",
		errNoSuchRequest:           "noSuchRequest",
		errSubagentThread:          "subagentThread",
		errWorkspaceArchived:       "workspaceArchived",
	},
}

type method struct {
	params, result reflect.Type
	call           func(c *conn, params json.RawMessage) (any, func(), *jsonrpc.Error)
}

type serverRequest struct {
	params, result reflect.Type
}

func handle[P, R any](h func(*conn, P) (R, func(), error)) method {
	return method{
		params: reflect.TypeFor[P](),
		result: reflect.TypeFor[R](),
		call: func(c *conn, raw json.RawMessage) (any, func(), *jsonrpc.Error) {
			var p P
			if err := jsonrpc.DecodeParams(raw, &p); err != nil {
				return nil, nil, err
			}
			result, after, err := h(c, p)
			if err != nil {
				return nil, nil, serverError(err)
			}
			return result, after, nil
		},
	}
}

//go:embed protocol.schema.json
var protocolSchema []byte

var protocolSHA256 = func() string {
	sum := sha256.Sum256(protocolSchema)
	return hex.EncodeToString(sum[:])
}()

// Empty is the params or result of a message that carries nothing.
type Empty struct{}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type InitializeParams struct {
	ClientInfo ClientInfo `json:"clientInfo"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type InitializeResult struct {
	ServerInfo ServerInfo `json:"serverInfo"`
	// ProtocolSHA256 is the sha256 of the protocol.schema.json this server
	// was built with; a client built from another contract can tell.
	ProtocolSHA256 string `json:"protocolSha256"`
}

type ThreadStartParams struct {
	WorkspaceID string `json:"workspaceId,omitempty"`
	repo.PermissionSettings
}

type ThreadPermissionsParams struct {
	ThreadID string `json:"threadId"`
	repo.PermissionSettings
}

type ThreadPageParams struct {
	ThreadID  string `json:"threadId"`
	BeforeSeq int    `json:"beforeSeq,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type ThreadPageResult struct {
	repo.ThreadView
	HasMore bool `json:"hasMore"`
}

type ThreadResult struct {
	Thread repo.ThreadRow `json:"thread"`
}

type ThreadListResult struct {
	Data []repo.ThreadSummary `json:"data"`
}

type ThreadStatusNotification struct {
	ThreadID string            `json:"threadId"`
	Status   repo.ThreadStatus `json:"status"`
}

type ModelListResult struct {
	Data    []llm.Model `json:"data"`
	Default string      `json:"default"`
}

type ToolListResult struct {
	Data []tools.ToolDescriptor `json:"data"`
}

type MCPServerListResult struct {
	Data []mcp.ServerStatus `json:"data"`
}

type UserInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type TurnStartParams struct {
	ThreadID   string      `json:"threadId"`
	Input      []UserInput `json:"input"`
	Connection string      `json:"connection,omitempty"`
}

type TurnSteerParams struct {
	ThreadID       string      `json:"threadId"`
	Input          []UserInput `json:"input"`
	ExpectedTurnID string      `json:"expectedTurnId"`
}

type TurnSteerResult struct {
	TurnID string `json:"turnId"`
}

type TurnInterruptParams struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
}

type TurnResult struct {
	Turn repo.TurnRow `json:"turn"`
}

type TurnNotification struct {
	ThreadID string         `json:"threadId"`
	Thread   repo.ThreadRow `json:"thread"`
	Turn     repo.TurnRow   `json:"turn"`
}

type DeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type ItemNotification struct {
	ThreadID string       `json:"threadId"`
	TurnID   string       `json:"turnId"`
	Item     repo.ItemRow `json:"item"`
}

type ErrorNotification struct {
	ThreadID string `json:"threadId,omitempty"`
	TurnID   string `json:"turnId,omitempty"`
	ItemID   string `json:"itemId,omitempty"`
	Message  string `json:"message"`
}

type WorkspaceListResult struct {
	Data []repo.WorkspaceRow `json:"data"`
}

type WorkspaceAddParams struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

type WorkspaceRenameParams struct {
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
}

type WorkspaceArchiveParams struct {
	WorkspaceID string `json:"workspaceId"`
	Archived    *bool  `json:"archived"`
}

type WorkspacePermissionsParams struct {
	WorkspaceID string `json:"workspaceId"`
	repo.PermissionSettings
}

type WorkspaceResult struct {
	Workspace repo.WorkspaceRow `json:"workspace"`
}

type CronListParams struct {
	ThreadID string `json:"threadId"`
}

type CronListResult struct {
	Data []repo.CronJobRow `json:"data"`
}

type CronCreateParams struct {
	ThreadID string `json:"threadId"`
	Name     string `json:"name"`
	Spec     string `json:"spec"`
	Prompt   string `json:"prompt"`
}

type CronPauseParams struct {
	ThreadID string `json:"threadId"`
	Name     string `json:"name"`
	Paused   *bool  `json:"paused"`
}

type CronDeleteParams struct {
	ThreadID string `json:"threadId"`
	Name     string `json:"name"`
}

type MemoryListParams struct {
	WorkspaceID   string `json:"workspaceId,omitempty"`
	IncludeGlobal bool   `json:"includeGlobal,omitempty"`
}

type MemoryListResult struct {
	Data []repo.MemoryRow `json:"data"`
}

type MemoryUpdateParams struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type MemoryForgetParams struct {
	ID string `json:"id"`
}

type MemoryResult struct {
	Memory repo.MemoryRow `json:"memory"`
}

type ApprovalRequestParams struct {
	PreparedExecution  *tools.Prepared `json:"preparedExecution,omitempty"`
	ThreadID           string          `json:"threadId"`
	TurnID             string          `json:"turnId"`
	ItemID             string          `json:"itemId"`
	ApprovalID         string          `json:"approvalId"`
	Server             string          `json:"server"`
	Tool               string          `json:"tool"`
	Arguments          json.RawMessage `json:"arguments"`
	Reason             string          `json:"reason,omitempty"`
	AvailableDecisions []repo.Decision `json:"availableDecisions"`
}

type ApprovalResponse struct {
	Decision repo.Decision `json:"decision"`
}

type UserInputRequestParams struct {
	ThreadID  string               `json:"threadId"`
	TurnID    string               `json:"turnId"`
	ItemID    string               `json:"itemId"`
	Questions []userinput.Question `json:"questions"`
}

type UserInputResponse struct {
	Answers userinput.Answers `json:"answers"`
}
