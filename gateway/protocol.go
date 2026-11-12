package gateway

//go:generate go test -run ^TestProtocolSchema$ . -update

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"

	"github.com/0xfakeSpike/ollupi/pkg/jsonrpc"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/pkg/wshub"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/mcp"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
	"github.com/coder/websocket"
)

const (
	errNotInitialized    = -32002
	errNotFound          = -32004
	errNoActiveTurn      = -32010
	errTurnMismatch      = -32011
	errNoSuchRequest     = -32012
	errSubagentThread    = -32013
	errWorkspaceArchived = -32014
	errThreadArchived    = -32015
	errThreadBusy        = -32016
)

const (
	methodInitialize  = "initialize"
	methodInitialized = "initialized"
)

const (
	notifyThreadStarted       = "thread/started"
	notifyThreadChanged       = "thread/changed"
	notifyTurnQueued          = "turn/queued"
	notifyTurnStarted         = "turn/started"
	notifyItemStarted         = "item/started"
	notifyItemUpdated         = "item/updated"
	notifyItemCompleted       = "item/completed"
	notifyAgentMessageDelta   = "item/agentMessage/delta"
	notifyReasoningDelta      = "item/reasoning/textDelta"
	notifyTurnCompleted       = "turn/completed"
	notifyThreadStatusChanged = "thread/status/changed"
	notifyWorkspaceChanged    = "workspace/changed"
	notifyEnvironmentChanged  = "environment/changed"
	notifyCronChanged         = "cron/changed"
	notifySettingsChanged     = "settings/changed"
	notifyError               = "error"
)

const (
	requestApproval  = "item/toolCall/requestApproval"
	requestUserInput = "item/tool/requestUserInput"
)

// Reconnect is what a client does after its connection ends, or its handshake is refused, in a given way.
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

// Ending is one way a connection ends, or its handshake is refused, and what the client does then. Once a connection
// is back, the client reads again what it shows with thread/read, since it heard nothing meanwhile.
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
	// closes are the WebSocket close codes a connection ends with; handshakes the HTTP statuses a handshake is
	// refused with.
	closes     map[int]Ending
	handshakes map[int]Ending
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
	},
	notifications: map[string]reflect.Type{
		notifyThreadStarted:       reflect.TypeFor[ThreadResult](),
		notifyThreadChanged:       reflect.TypeFor[ThreadChangedNotification](),
		notifyTurnQueued:          reflect.TypeFor[TurnNotification](),
		notifyTurnStarted:         reflect.TypeFor[TurnNotification](),
		notifyItemStarted:         reflect.TypeFor[ItemNotification](),
		notifyItemUpdated:         reflect.TypeFor[ItemNotification](),
		notifyItemCompleted:       reflect.TypeFor[ItemNotification](),
		notifyAgentMessageDelta:   reflect.TypeFor[DeltaNotification](),
		notifyReasoningDelta:      reflect.TypeFor[DeltaNotification](),
		notifyTurnCompleted:       reflect.TypeFor[TurnNotification](),
		notifyThreadStatusChanged: reflect.TypeFor[ThreadStatusNotification](),
		notifyWorkspaceChanged:    reflect.TypeFor[WorkspaceResult](),
		notifyEnvironmentChanged:  reflect.TypeFor[EnvironmentChangedNotification](),
		notifyCronChanged:         reflect.TypeFor[CronChangedNotification](),
		notifySettingsChanged:     reflect.TypeFor[SettingsRow](),
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
		errThreadArchived:          "threadArchived",
		errThreadBusy:              "threadBusy",
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
	// EnvironmentID is where the conversation runs: one of the user's environments. Empty is the one on this machine.
	EnvironmentID string `json:"environmentId,omitempty"`
	Title         string `json:"title,omitempty"`
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
	ThreadView
	HasMore bool `json:"hasMore"`
}

type ThreadResult struct {
	Thread ThreadRow `json:"thread"`
}

type ThreadRenameParams struct {
	ThreadID string `json:"threadId"`
	Title    string `json:"title"`
}

// ThreadMarkReadParams names the thread the user has read: it is no longer unread until another of its turns ends.
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

type ThreadChangedNotification struct {
	ThreadID string     `json:"threadId"`
	Thread   *ThreadRow `json:"thread"`
}

type ThreadListResult struct {
	Data    []ThreadSummary `json:"data"`
	HasMore bool            `json:"hasMore"`
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

// UserInput is one part of a message: "text", or a "file" on this machine, an image or a text file, which the
// engine copies when the message is sent. Name defaults to the file's name.
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
	Attachment repo.Attachment `json:"attachment"`
	Data       string          `json:"data"`
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
	Turn TurnRow `json:"turn"`
}

type TurnNotification struct {
	ThreadID string    `json:"threadId"`
	Thread   ThreadRow `json:"thread"`
	Turn     TurnRow   `json:"turn"`
}

type DeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type ItemNotification struct {
	ThreadID string  `json:"threadId"`
	TurnID   string  `json:"turnId"`
	Item     ItemRow `json:"item"`
}

type ErrorNotification struct {
	ThreadID string `json:"threadId,omitempty"`
	TurnID   string `json:"turnId,omitempty"`
	ItemID   string `json:"itemId,omitempty"`
	Message  string `json:"message"`
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
	repo.PermissionSettings
}

type WorkspaceResult struct {
	Workspace WorkspaceRow `json:"workspace"`
}

type EnvironmentListResult struct {
	Data []EnvironmentRow `json:"data"`
}

// EnvironmentAddParams names another place a conversation can run. Kind defaults to "machine": this machine is every
// user's environment already.
type EnvironmentAddParams struct {
	Name string               `json:"name"`
	Kind repo.EnvironmentKind `json:"kind,omitempty"`
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

// EnvironmentChangedNotification reports an environment added or renamed; Environment is nil once it is gone.
type EnvironmentChangedNotification struct {
	EnvironmentID string          `json:"environmentId"`
	Environment   *EnvironmentRow `json:"environment"`
}

type CronListParams struct {
	ThreadID string `json:"threadId,omitempty"`
}

type CronListResult struct {
	Data []CronJobView `json:"data"`
}

type CronChangedNotification struct {
	ID       string       `json:"id"`
	ThreadID string       `json:"threadId"`
	Job      *CronJobView `json:"job"`
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

type MemoryListParams struct {
	Scope       repo.MemoryScope `json:"scope"`
	WorkspaceID string           `json:"workspaceId,omitempty"`
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

type SettingsUpdateParams struct {
	Redaction *bool `json:"redaction"`
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
	Content            string          `json:"content,omitempty"`
	SuggestedRules     []string        `json:"suggestedRules,omitempty"`
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
	// Skipped declines the questions without answers (send answers as {}): the call ends declined and the agent goes
	// on without them.
	Skipped bool `json:"skipped,omitempty"`
}
