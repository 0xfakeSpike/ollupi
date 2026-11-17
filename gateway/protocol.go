package gateway

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/ask"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/cron"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/wshub"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/0xfakeSpike/ollupi/tool/mcp"
	"github.com/0xfakeSpike/ollupi/user"
	"github.com/coder/websocket"
)

//go:generate go test -run ^TestProtocolSchema$ . -update

const (
	errNotInitialized    = -32002
	errNotFound          = -32004
	errNoActiveTurn      = -32010
	errTurnMismatch      = -32011
	errNoSuchRequest     = -32012
	errSubagentThread    = -32013
	errWorkspaceArchived = -32014
	errThreadBusy        = -32016
	errSearchTimedOut    = -32017
	errRefused           = -32018
)

const (
	methodInitialize  = "initialize"
	methodInitialized = "initialized"
)

// A change reaches a client as it was committed: its seq orders it after the page a client read, which names the seq it
// was read at, so a client drops the notifications of a conversation up to that seq.
const (
	notifyThreadChanged      = "thread/changed"
	notifyTurnChanged        = "turn/changed"
	notifyItemChanged        = "item/changed"
	notifyAgentMessageDelta  = "item/agentMessage/delta"
	notifyReasoningDelta     = "item/reasoning/textDelta"
	notifyWorkspaceChanged   = "workspace/changed"
	notifyEnvironmentChanged = "environment/changed"
	notifyCronChanged        = "cron/changed"
	notifySettingsChanged    = "settings/changed"
	notifyMCPServerChanged   = "mcpServer/changed"
	notifyMemoryChanged      = "memory/changed"
	notifyError              = "error"
)

const (
	requestApproval  = "item/toolCall/requestApproval"
	requestUserInput = "item/tool/requestUserInput"
)

type Reconnect string

const (
	// ReconnectNever: not on its own, but when the user acts, such as signing in again or bringing the client back
	// to the front.
	ReconnectNever Reconnect = "never"
	// ReconnectSoon: after a random wait of up to 10 seconds, so that the clients of a whole shard do not come back at
	// once.
	ReconnectSoon Reconnect = "soon"
	// ReconnectBackoff: after a wait that starts at 1 second and doubles with each failed try up to 60 seconds, a
	// random point under each bound.
	ReconnectBackoff Reconnect = "backoff"
	// ReconnectRetryAfter: after the seconds the response's Retry-After header gives, or as ReconnectBackoff when it
	// gives none, as when a load balancer refuses for a shard that is changing hands.
	ReconnectRetryAfter Reconnect = "retryAfter"
)

// Once a connection is back, the client reads again what it shows with thread/read: it heard nothing meanwhile.
type Ending struct {
	Name      string    `json:"name"`
	Reconnect Reconnect `json:"reconnect"`
}

var protocol = struct {
	methods             map[string]method
	notifications       map[string]reflect.Type
	serverRequests      map[string]serverRequest
	clientNotifications map[string]reflect.Type
	errors              map[int]string
	closes              map[int]Ending
	handshakes          map[int]Ending
	limits              Limits
}{
	methods: map[string]method{
		methodInitialize:              handle((*conn).initialize),
		"thread/start":                handle((*conn).threadStart),
		"thread/resume":               handle(threadPage(true)),
		"thread/read":                 handle(threadPage(false)),
		"thread/list":                 handle((*conn).threadList),
		"thread/updatePermissions":    handle((*conn).threadPermissions),
		"thread/rename":               handle((*conn).threadRename),
		"thread/archive":              handle((*conn).threadArchive),
		"thread/markRead":             handle((*conn).threadMarkRead),
		"thread/delete":               handle((*conn).threadDelete),
		"turn/start":                  handle((*conn).turnStart),
		"turn/steer":                  handle((*conn).turnSteer),
		"turn/interrupt":              handle((*conn).turnInterrupt),
		"attachment/read":             handle((*conn).attachmentRead),
		"model/list":                  handle((*conn).modelList),
		"tool/list":                   handle((*conn).toolList),
		"mcpServer/list":              handle((*conn).mcpServerList),
		"mcpServer/set":               handle((*conn).mcpServerSet),
		"mcpServer/delete":            handle((*conn).mcpServerDelete),
		"workspace/list":              handle((*conn).workspaceList),
		"workspace/add":               handle((*conn).workspaceAdd),
		"workspace/rename":            handle((*conn).workspaceRename),
		"workspace/move":              handle((*conn).workspaceMove),
		"workspace/archive":           handle((*conn).workspaceArchive),
		"workspace/updatePermissions": handle((*conn).workspacePermissions),
		"environment/list":            handle((*conn).environmentList),
		"environment/add":             handle((*conn).environmentAdd),
		"environment/rename":          handle((*conn).environmentRename),
		"environment/delete":          handle((*conn).environmentDelete),
		"cron/list":                   handle((*conn).cronList),
		"cron/create":                 handle((*conn).cronCreate),
		"cron/pause":                  handle((*conn).cronPause),
		"cron/delete":                 handle((*conn).cronDelete),
		"memory/list":                 handle((*conn).memoryList),
		"memory/status":               handle((*conn).memoryStatus),
		"memory/update":               handle((*conn).memoryUpdate),
		"memory/move":                 handle((*conn).memoryMove),
		"memory/forget":               handle((*conn).memoryForget),
		"search":                      handle((*conn).search),
		"settings/read":               handle((*conn).settingsRead),
		"settings/update":             handle((*conn).settingsUpdate),
		"settings/updatePermissions":  handle((*conn).settingsPermissions),
	},
	notifications: map[string]reflect.Type{
		notifyThreadChanged:      reflect.TypeFor[ThreadChangedNotification](),
		notifyTurnChanged:        reflect.TypeFor[TurnChangedNotification](),
		notifyItemChanged:        reflect.TypeFor[ItemChangedNotification](),
		notifyAgentMessageDelta:  reflect.TypeFor[DeltaNotification](),
		notifyReasoningDelta:     reflect.TypeFor[DeltaNotification](),
		notifyWorkspaceChanged:   reflect.TypeFor[WorkspaceResult](),
		notifyEnvironmentChanged: reflect.TypeFor[EnvironmentChangedNotification](),
		notifyCronChanged:        reflect.TypeFor[CronChangedNotification](),
		notifySettingsChanged:    reflect.TypeFor[SettingsRow](),
		notifyMCPServerChanged:   reflect.TypeFor[MCPServerChangedNotification](),
		notifyMemoryChanged:      reflect.TypeFor[MemoryResult](),
		notifyError:              reflect.TypeFor[ErrorNotification](),
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
		errThreadBusy:              "threadBusy",
		errSearchTimedOut:          "searchTimedOut",
		errRefused:                 "refused",
	},
	closes: map[int]Ending{
		// The server is stopping, or the connection reached its age.
		int(websocket.StatusGoingAway): {"goingAway", ReconnectSoon},
		// No close frame came: the network dropped, or the server gave up on a peer that stopped answering pings.
		int(websocket.StatusAbnormalClosure): {"abnormal", ReconnectBackoff},
		// The client read too slowly and the server's queue for it filled.
		int(websocket.StatusPolicyViolation): {"policyViolation", ReconnectBackoff},
		int(websocket.StatusMessageTooBig):   {"messageTooBig", ReconnectBackoff},
		int(websocket.StatusInternalError):   {"internalError", ReconnectBackoff},
		// The user opened more connections than they may hold, and this was the oldest. Reconnecting on its own
		// would take the place of another of theirs, which would do the same.
		int(wshub.StatusReplaced): {"replaced", ReconnectNever},
	},
	handshakes: map[int]Ending{
		http.StatusUnauthorized:        {"unauthorized", ReconnectNever},
		http.StatusForbidden:           {"forbidden", ReconnectNever},
		http.StatusInternalServerError: {"internalError", ReconnectBackoff},
		// Too many connections are starting, or the user's shard cannot be reached now.
		http.StatusServiceUnavailable: {"unavailable", ReconnectRetryAfter},
	},
	limits: Limits{SearchRunes: app.MaxSearchRunes, SearchTerms: app.MaxSearchTerms},
}

type Limits struct {
	SearchRunes int `json:"searchRunes"`
	SearchTerms int `json:"searchTerms"`
}

type method struct {
	params, result reflect.Type
	call           func(c *conn, ctx context.Context, params json.RawMessage) (any, func(), *jsonrpc.Error)
}

type serverRequest struct {
	params, result reflect.Type
}

func handle[P, R any](h func(*conn, context.Context, P) (R, func(), error)) method {
	return method{
		params: reflect.TypeFor[P](),
		result: reflect.TypeFor[R](),
		call: func(c *conn, ctx context.Context, raw json.RawMessage) (any, func(), *jsonrpc.Error) {
			var p P
			if err := jsonrpc.DecodeParams(raw, &p); err != nil {
				return nil, nil, err
			}
			result, after, err := h(c, ctx, p)
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
	ServerInfo     ServerInfo `json:"serverInfo"`
	ProtocolSHA256 string     `json:"protocolSha256"`
}

type ThreadStartParams struct {
	WorkspaceID string `json:"workspaceId,omitempty"`
	// Empty is the environment on this machine.
	EnvironmentID string `json:"environmentId,omitempty"`
	Title         string `json:"title,omitempty"`
}

type ThreadPermissionsParams struct {
	ThreadID string `json:"threadId"`
	policy.Settings
}

type ThreadPageParams struct {
	ThreadID  string `json:"threadId"`
	BeforeSeq int64  `json:"beforeSeq,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

type ThreadPageResult struct {
	ThreadView
	HasMore bool `json:"hasMore"`
}

type ThreadResult struct {
	Thread ThreadRow `json:"thread"`
}

type ThreadListParams struct {
	// WorkspaceID, when given, keeps the conversations in that workspace, or those in none when it is "".
	WorkspaceID *string `json:"workspaceId,omitempty"`
	Archived    bool    `json:"archived,omitempty"`
	// After continues a list from the last conversation of the page before.
	After *ThreadCursor `json:"after,omitempty"`
	Limit int           `json:"limit,omitempty"`
}

type ThreadCursor struct {
	UpdatedAt time.Time `json:"updatedAt"`
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

type ThreadRenameParams struct {
	ThreadID string `json:"threadId"`
	Title    string `json:"title"`
}

// The thread is unread again once another of its turns ends.
type ThreadMarkReadParams struct {
	ThreadID string `json:"threadId"`
}

type ThreadArchiveParams struct {
	ThreadID string `json:"threadId"`
	Archived *bool  `json:"archived"`
}

type ThreadDeleteParams struct {
	ThreadID string `json:"threadId"`
}

// Thread is null once the conversation is deleted.
type ThreadChangedNotification struct {
	ThreadID string     `json:"threadId"`
	Thread   *ThreadRow `json:"thread"`
	Seq      int64      `json:"seq"`
}

type TurnChangedNotification struct {
	ThreadID string  `json:"threadId"`
	Turn     TurnRow `json:"turn"`
	Seq      int64   `json:"seq"`
}

type ItemChangedNotification struct {
	ThreadID string  `json:"threadId"`
	Item     ItemRow `json:"item"`
	Seq      int64   `json:"seq"`
}

// A delta is text a model writes now, which the item it belongs to holds whole once the response is kept.
type DeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type ErrorNotification struct {
	ThreadID string `json:"threadId,omitempty"`
	TurnID   string `json:"turnId,omitempty"`
	ItemID   string `json:"itemId,omitempty"`
	Message  string `json:"message"`
}

type ThreadListResult struct {
	Data    []ThreadRow `json:"data"`
	HasMore bool        `json:"hasMore"`
}

type ModelListResult struct {
	Data    []llm.Model `json:"data"`
	Default string      `json:"default"`
}

type ToolDescriptor struct {
	Provider    string           `json:"provider"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Annotations tool.Annotations `json:"annotations"`
	InputSchema map[string]any   `json:"input_schema"`
}

type ToolListResult struct {
	Data []ToolDescriptor `json:"data"`
}

type MCPServerListResult struct {
	Data []MCPServer `json:"data"`
}

type MCPServer struct {
	MCPServerConfig
	// Status is left out for a server that has not been started yet.
	Status *mcp.ServerStatus `json:"status,omitempty"`
}

type MCPServerDeleteParams struct {
	Name string `json:"name"`
}

// Server is left out once the server is deleted.
type MCPServerChangedNotification struct {
	Name   string           `json:"name"`
	Server *MCPServerConfig `json:"server"`
}

// A "file" is copied when the message is sent; Name defaults to the file's name.
type UserInput struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Path     string `json:"path,omitempty"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

type AttachmentReadParams struct {
	ThreadID     string `json:"threadId"`
	AttachmentID string `json:"attachmentId"`
}

type AttachmentReadResult struct {
	Attachment conversation.Attachment `json:"attachment"`
	Data       string                  `json:"data"`
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

// Turn's status says whether it runs now or waits behind the turn in progress.
type TurnResult struct {
	Turn TurnRow `json:"turn"`
}

type WorkspaceListResult struct {
	Data []WorkspaceRow `json:"data"`
}

type WorkspaceAddParams struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

type WorkspaceRenameParams struct {
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
}

type WorkspaceMoveParams struct {
	WorkspaceID string `json:"workspaceId"`
	Path        string `json:"path"`
}

type WorkspaceArchiveParams struct {
	WorkspaceID string `json:"workspaceId"`
	Archived    *bool  `json:"archived"`
}

type WorkspacePermissionsParams struct {
	WorkspaceID string `json:"workspaceId"`
	policy.Settings
}

type WorkspaceResult struct {
	Workspace WorkspaceRow `json:"workspace"`
}

type EnvironmentListResult struct {
	Data []EnvironmentRow `json:"data"`
}

// Kind defaults to "machine": this machine is every user's environment already.
type EnvironmentAddParams struct {
	Name string               `json:"name"`
	Kind user.EnvironmentKind `json:"kind,omitempty"`
}

type EnvironmentRenameParams struct {
	EnvironmentID string `json:"environmentId"`
	Name          string `json:"name"`
}

type EnvironmentDeleteParams struct {
	EnvironmentID string `json:"environmentId"`
}

type EnvironmentResult struct {
	Environment EnvironmentRow `json:"environment"`
}

// Environment is nil once it is gone.
type EnvironmentChangedNotification struct {
	EnvironmentID string          `json:"environmentId"`
	Environment   *EnvironmentRow `json:"environment"`
}

type CronListParams struct {
	ThreadID string `json:"threadId,omitempty"`
}

type CronListResult struct {
	Data []CronJobRow `json:"data"`
}

// Job is nil once the task is gone.
type CronChangedNotification struct {
	ID       string      `json:"id"`
	ThreadID string      `json:"threadId"`
	Job      *CronJobRow `json:"job"`
}

type CronCreateParams struct {
	ThreadID string `json:"threadId"`
	Name     string `json:"name"`
	Spec     string `json:"spec"`
	Prompt   string `json:"prompt"`
	Times    *int   `json:"times,omitempty"`
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

type CronResult struct {
	Job CronJobRow `json:"job"`
}

type MemoryListParams struct {
	Scope       memory.Scope `json:"scope"`
	WorkspaceID string       `json:"workspaceId,omitempty"`
}

type MemoryListResult struct {
	Data []MemoryRow `json:"data"`
}

type MemoryUpdateParams struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type MemoryMoveParams struct {
	ID string `json:"id"`
}

type MemoryForgetParams struct {
	ID string `json:"id"`
}

type MemoryResult struct {
	Memory MemoryRow `json:"memory"`
}

type SearchParams struct {
	Query       string `json:"query"`
	WorkspaceID string `json:"workspaceId,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type SearchResult struct {
	Found
	Memories []MemoryRow `json:"memories"`
}

// A setting left out stays as it is.
type SettingsUpdateParams struct {
	Redaction *bool `json:"redaction,omitempty"`
	Memory    *bool `json:"memory,omitempty"`
}

type SettingsPermissionsParams struct {
	policy.Settings
}

type ApprovalRequestParams struct {
	ThreadID           string                  `json:"threadId"`
	TurnID             string                  `json:"turnId"`
	ItemID             string                  `json:"itemId"`
	ApprovalID         string                  `json:"approvalId"`
	Server             string                  `json:"server"`
	Tool               string                  `json:"tool"`
	Arguments          json.RawMessage         `json:"arguments"`
	PreparedExecution  json.RawMessage         `json:"preparedExecution,omitempty"`
	Reason             string                  `json:"reason,omitempty"`
	Content            string                  `json:"content,omitempty"`
	SuggestedRules     []string                `json:"suggestedRules,omitempty"`
	AvailableDecisions []conversation.Decision `json:"availableDecisions"`
}

type ApprovalResponse struct {
	Decision conversation.Decision `json:"decision"`
}

type UserInputRequestParams struct {
	ThreadID  string         `json:"threadId"`
	TurnID    string         `json:"turnId"`
	ItemID    string         `json:"itemId"`
	Questions []ask.Question `json:"questions"`
}

type UserInputResponse struct {
	Answers ask.Answers `json:"answers"`
	// Skipped declines the questions without answers (send answers as {}): the call ends declined and the agent goes
	// on without them.
	Skipped bool `json:"skipped,omitempty"`
}

var errorCodes = []struct {
	err  error
	code int
}{
	{store.ErrNotFound, errNotFound},
	{app.ErrNoActiveTurn, errNoActiveTurn},
	{app.ErrTurnMismatch, errTurnMismatch},
	{app.ErrNoSuchRequest, errNoSuchRequest},
	{user.ErrSubagentThread, errSubagentThread},
	{user.ErrWorkspaceArchived, errWorkspaceArchived},
	{app.ErrThreadBusy, errThreadBusy},
	{app.ErrWorkspaceBusy, errThreadBusy},
	{app.ErrQueueFull, errThreadBusy},
	{conversation.ErrRefused, errRefused},
	{app.ErrEnvironmentInUse, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidMessage, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidAnswer, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidDecision, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidPath, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidQuery, jsonrpc.CodeInvalidParams},
	{app.ErrSearchTimedOut, errSearchTimedOut},
	{user.ErrInvalidName, jsonrpc.CodeInvalidParams},
	{user.ErrInvalidMCPServer, jsonrpc.CodeInvalidParams},
	{policy.ErrInvalidSettings, jsonrpc.CodeInvalidParams},
	{memory.ErrInvalidChange, jsonrpc.CodeInvalidParams},
	{kit.ErrUnknownTool, jsonrpc.CodeInvalidParams},
	{kit.ErrInvalidArguments, jsonrpc.CodeInvalidParams},
	{cron.ErrInvalidJob, jsonrpc.CodeInvalidParams},
}

func serverError(err error) *jsonrpc.Error {
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	for _, e := range errorCodes {
		if errors.Is(err, e.err) {
			return jsonrpc.Errorf(e.code, "%v", err)
		}
	}
	return jsonrpc.Errorf(jsonrpc.CodeServerError, "the request could not be completed")
}
