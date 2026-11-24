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

	"github.com/0xfakeSpike/ollupi/account"
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
	"github.com/coder/websocket"
)

//go:generate go test -run ^TestProtocolSchema$ . -update

const (
	errNotInitialized    = -32002
	errNotFound          = -32004
	errNoActiveTurn      = -32010
	errTurnMismatch      = -32011
	errNoSuchRequest     = -32012
	errWorkspaceArchived = -32014
	errThreadArchived    = -32015
	errThreadBusy        = -32016
	errSearchTimedOut    = -32017
	errRefused           = -32018
	errSecondHub         = -32020
	errNoHub             = -32021
)

const (
	methodInitialize  = "initialize"
	methodInitialized = "initialized"
)

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
	notifyTodoChanged        = "todo/changed"
	notifyError              = "error"
)

const (
	requestApproval  = "item/toolCall/requestApproval"
	requestUserInput = "item/tool/requestUserInput"
)

type Reconnect string

const (
	ReconnectNever      Reconnect = "never"
	ReconnectSoon       Reconnect = "soon"
	ReconnectBackoff    Reconnect = "backoff"
	ReconnectRetryAfter Reconnect = "retryAfter"
)

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
		methodInitialize:               handleThen((*conn).initialize),
		"thread/start":                 handle((*conn).threadStart),
		"thread/resume":                read(handleThen(threadPage(true))),
		"thread/read":                  read(handleThen(threadPage(false))),
		"thread/list":                  read(handle((*conn).threadList)),
		"thread/updatePermissions":     handle((*conn).threadPermissions),
		"thread/rename":                handle((*conn).threadRename),
		"thread/archive":               handle((*conn).threadArchive),
		"thread/markRead":              handle((*conn).threadMarkRead),
		"thread/delete":                handle((*conn).threadDelete),
		"turn/start":                   handle((*conn).turnStart),
		"turn/steer":                   handle((*conn).turnSteer),
		"turn/interrupt":               handle((*conn).turnInterrupt),
		"attachment/read":              read(handle((*conn).attachmentRead)),
		"model/list":                   read(handle((*conn).modelList)),
		"tool/list":                    read(handle((*conn).toolList)),
		"mcpServer/list":               read(handle((*conn).mcpServerList)),
		"mcpServer/set":                handle((*conn).mcpServerSet),
		"mcpServer/delete":             handle((*conn).mcpServerDelete),
		"workspace/list":               read(handle((*conn).workspaceList)),
		"workspace/add":                handle((*conn).workspaceAdd),
		"workspace/rename":             handle((*conn).workspaceRename),
		"workspace/move":               handle((*conn).workspaceMove),
		"workspace/archive":            handle((*conn).workspaceArchive),
		"workspace/setKind":            handle((*conn).workspaceSetKind),
		"workspace/updatePermissions":  handle((*conn).workspacePermissions),
		"workspace/updateInstructions": handle((*conn).workspaceInstructions),
		"workspace/updateProviders":    handle((*conn).workspaceProviders),
		"environment/list":             read(handle((*conn).environmentList)),
		"environment/add":              handle((*conn).environmentAdd),
		"environment/rename":           handle((*conn).environmentRename),
		"environment/delete":           handle((*conn).environmentDelete),
		"cron/list":                    read(handle((*conn).cronList)),
		"cron/create":                  handle((*conn).cronCreate),
		"cron/pause":                   handle((*conn).cronPause),
		"cron/delete":                  handle((*conn).cronDelete),
		"memory/list":                  read(handle((*conn).memoryList)),
		"memory/status":                read(handle((*conn).memoryStatus)),
		"memory/update":                handle((*conn).memoryUpdate),
		"memory/move":                  handle((*conn).memoryMove),
		"memory/forget":                handle((*conn).memoryForget),
		"usage/query":                  read(handle((*conn).usageQuery)),
		"presence/record":              handle((*conn).presenceRecord),
		"presence/summary":             read(handle((*conn).presenceSummary)),
		"search":                       read(handle((*conn).search)),
		"todo/list":                    handle((*conn).todoList),
		"todo/add":                     handle((*conn).todoAdd),
		"todo/update":                  handle((*conn).todoUpdate),
		"settings/read":                read(handle((*conn).settingsRead)),
		"settings/update":              handle((*conn).settingsUpdate),
		"settings/updatePermissions":   handle((*conn).settingsPermissions),
	},
	notifications: map[string]reflect.Type{
		notifyThreadChanged:      reflect.TypeFor[ThreadChangedNotification](),
		notifyTurnChanged:        reflect.TypeFor[TurnChangedNotification](),
		notifyItemChanged:        reflect.TypeFor[ItemChangedNotification](),
		notifyAgentMessageDelta:  reflect.TypeFor[DeltaNotification](),
		notifyReasoningDelta:     reflect.TypeFor[DeltaNotification](),
		notifyWorkspaceChanged:   reflect.TypeFor[WorkspaceChangedNotification](),
		notifyEnvironmentChanged: reflect.TypeFor[EnvironmentChangedNotification](),
		notifyCronChanged:        reflect.TypeFor[CronChangedNotification](),
		notifySettingsChanged:    reflect.TypeFor[SettingsChangedNotification](),
		notifyMCPServerChanged:   reflect.TypeFor[MCPServerChangedNotification](),
		notifyMemoryChanged:      reflect.TypeFor[MemoryChangedNotification](),
		notifyTodoChanged:        reflect.TypeFor[TodoChangedNotification](),
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
		errWorkspaceArchived:       "workspaceArchived",
		errThreadArchived:          "threadArchived",
		errThreadBusy:              "threadBusy",
		errSearchTimedOut:          "searchTimedOut",
		errRefused:                 "refused",
		errSecondHub:               "secondHub",
		errNoHub:                   "noHub",
	},
	closes: map[int]Ending{
		int(websocket.StatusGoingAway):       {"goingAway", ReconnectSoon},
		int(websocket.StatusAbnormalClosure): {"abnormal", ReconnectBackoff},
		int(websocket.StatusPolicyViolation): {"policyViolation", ReconnectBackoff},
		int(websocket.StatusMessageTooBig):   {"messageTooBig", ReconnectBackoff},
		int(websocket.StatusInternalError):   {"internalError", ReconnectBackoff},
		int(wshub.StatusReplaced):            {"replaced", ReconnectNever},
	},
	handshakes: map[int]Ending{
		http.StatusUnauthorized:        {"unauthorized", ReconnectNever},
		http.StatusForbidden:           {"forbidden", ReconnectNever},
		http.StatusInternalServerError: {"internalError", ReconnectBackoff},
		http.StatusServiceUnavailable:  {"unavailable", ReconnectRetryAfter},
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
	read           bool
}

func read(m method) method {
	m.read = true
	return m
}

func concurrent(name string) bool {
	return protocol.methods[name].read
}

type serverRequest struct {
	params, result reflect.Type
}

func handle[P, R any](h func(*conn, context.Context, P) (R, error)) method {
	return handleThen(func(c *conn, ctx context.Context, p P) (R, func(), error) {
		r, err := h(c, ctx, p)
		return r, nil, err
	})
}

func handleThen[P, R any](h func(*conn, context.Context, P) (R, func(), error)) method {
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
	WorkspaceID   string `json:"workspaceId,omitempty"`
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
	WorkspaceID *string       `json:"workspaceId,omitempty"`
	Archived    bool          `json:"archived,omitempty"`
	After       *ThreadCursor `json:"after,omitempty"`
	Limit       int           `json:"limit,omitempty"`
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

type DeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
	Offset   int    `json:"offset"`
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
	Seq     int64       `json:"seq"`
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
	Seq  int64       `json:"seq"`
}

type MCPServer struct {
	MCPServerConfig
	Status *MCPServerStatus `json:"status,omitempty"`
}

type MCPServerStatus struct {
	Name         string   `json:"name"`
	Connected    bool     `json:"connected"`
	Tools        int      `json:"tools"`
	SkippedTools []string `json:"skippedTools,omitempty"`
	Error        string   `json:"error,omitempty"`
}

type MCPServerDeleteParams struct {
	Name string `json:"name"`
}

type MCPServerChangedNotification struct {
	Name   string           `json:"name"`
	Server *MCPServerConfig `json:"server"`
	Seq    int64            `json:"seq"`
}

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

type TurnResult struct {
	Turn TurnRow `json:"turn"`
}

type WorkspaceListResult struct {
	Data []WorkspaceRow `json:"data"`
	Seq  int64          `json:"seq"`
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

type WorkspaceInstructionsParams struct {
	WorkspaceID  string `json:"workspaceId"`
	Instructions string `json:"instructions"`
}

type WorkspaceProvidersParams struct {
	WorkspaceID string `json:"workspaceId"`
	account.Providers
}

type WorkspaceSetKindParams struct {
	WorkspaceID string                `json:"workspaceId"`
	Kind        account.WorkspaceKind `json:"kind"`
}

type WorkspacePermissionsParams struct {
	WorkspaceID string `json:"workspaceId"`
	policy.Settings
}

type WorkspaceResult struct {
	Workspace WorkspaceRow `json:"workspace"`
}

type WorkspaceChangedNotification struct {
	Workspace WorkspaceRow `json:"workspace"`
	Seq       int64        `json:"seq"`
}

type EnvironmentListResult struct {
	Data []EnvironmentRow `json:"data"`
	Seq  int64            `json:"seq"`
}

type EnvironmentAddParams struct {
	Name string                  `json:"name"`
	Kind account.EnvironmentKind `json:"kind,omitempty"`
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

type EnvironmentChangedNotification struct {
	EnvironmentID string          `json:"environmentId"`
	Environment   *EnvironmentRow `json:"environment"`
	Seq           int64           `json:"seq"`
}

type CronListParams struct {
	ThreadID string `json:"threadId,omitempty"`
}

type CronListResult struct {
	Data []CronJobRow `json:"data"`
	Seq  int64        `json:"seq"`
}

type CronChangedNotification struct {
	ID       string      `json:"id"`
	ThreadID string      `json:"threadId"`
	Job      *CronJobRow `json:"job"`
	Seq      int64       `json:"seq"`
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
	Seq  int64       `json:"seq"`
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

type MemoryChangedNotification struct {
	Memory MemoryRow `json:"memory"`
	Seq    int64     `json:"seq"`
}

type UsageQueryParams struct {
	From          time.Time        `json:"from"`
	To            time.Time        `json:"to"`
	GroupBy       store.UsageGroup `json:"groupBy"`
	OffsetMinutes int              `json:"offsetMinutes,omitempty"`
}

type UsageQueryResult struct {
	Data []UsageRow `json:"data"`
}

// UsageRow is one bucket of the aggregation: a day (YYYY-MM-DD in the caller's
// time zone), a workspace ID, an API's name or a model name, with the API that
// served the rows and how many model responses it adds up. Provider is the
// API's name for a model grouping, and empty otherwise.
type UsageRow struct {
	Key      string `json:"key"`
	Provider string `json:"provider,omitempty"`
	Calls    int64  `json:"calls"`
	conversation.Usage
}

// PresenceRecordParams appends stretches of time the user spent in the app.
type PresenceRecordParams struct {
	Rows []PresenceInput `json:"rows"`
}

// PresenceInput is one stretch: what the app was showing, and when. Sending the
// same id again only extends the stretch it already recorded.
type PresenceInput struct {
	ID          string              `json:"id"`
	ThreadID    string              `json:"threadId,omitempty"`
	TodoID      int64               `json:"todoId,omitempty"`
	WorkspaceID string              `json:"workspaceId,omitempty"`
	State       store.PresenceState `json:"state"`
	Start       time.Time           `json:"start"`
	End         time.Time           `json:"end"`
}

type PresenceSummaryParams struct {
	From          time.Time           `json:"from"`
	To            time.Time           `json:"to"`
	GroupBy       store.PresenceGroup `json:"groupBy"`
	OffsetMinutes int                 `json:"offsetMinutes,omitempty"`
}

type PresenceSummaryResult struct {
	Data []PresenceRow `json:"data"`
}

// PresenceRow is one bucket of the aggregation: a thread ID, a todo ID, a
// workspace ID, a day (YYYY-MM-DD in the caller's time zone) or "" for the
// whole app, and the seconds it holds.
type PresenceRow struct {
	Key     string `json:"key"`
	Seconds int64  `json:"seconds"`
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
	Redaction *bool `json:"redaction,omitempty"`
	Memory    *bool `json:"memory,omitempty"`
	Learn     *bool `json:"learn,omitempty"`
	// Model is the one of the deployment's models a new conversation starts
	// with; empty is the first.
	Model *string `json:"model,omitempty"`
	// LearnModel is the one of the deployment's models that learns the
	// user's memories; empty learns none.
	LearnModel *string `json:"learnModel,omitempty"`
}

type SettingsReadResult struct {
	SettingsRow
	Seq int64 `json:"seq"`
}

type SettingsChangedNotification struct {
	SettingsRow
	Seq int64 `json:"seq"`
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
	Escalated          bool                    `json:"escalated,omitempty"`
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
	Skipped bool        `json:"skipped,omitempty"`
}

type TodoListParams struct {
	Statuses []account.TodoStatus `json:"statuses,omitempty"`
	Limit    int                  `json:"limit,omitempty"`
}

type TodoListResult struct {
	Data []TodoRow `json:"data"`
	More bool      `json:"more,omitempty"`
	Seq  int64     `json:"seq"`
}

type TodoAddParams struct {
	Title string `json:"title"`
	Notes string `json:"notes,omitempty"`
}

// TodoUpdateParams changes the fields it names of a todo: a field left out stays as it is, and "" clears it.
type TodoUpdateParams struct {
	ID       int64               `json:"id"`
	Title    *string             `json:"title,omitempty"`
	Status   *account.TodoStatus `json:"status,omitempty"`
	Notes    *string             `json:"notes,omitempty"`
	Result   *string             `json:"result,omitempty"`
	ThreadID *string             `json:"threadId,omitempty"`
}

type TodoResult struct {
	Todo TodoRow `json:"todo"`
}

type TodoChangedNotification struct {
	Todo TodoRow `json:"todo"`
	Seq  int64   `json:"seq"`
}

var errorCodes = []struct {
	err  error
	code int
}{
	{store.ErrNotFound, errNotFound},
	{app.ErrNoActiveTurn, errNoActiveTurn},
	{app.ErrTurnMismatch, errTurnMismatch},
	{app.ErrNoSuchRequest, errNoSuchRequest},
	{account.ErrWorkspaceArchived, errWorkspaceArchived},
	{app.ErrThreadBusy, errThreadBusy},
	{app.ErrSecondHub, errSecondHub},
	{app.ErrNoHub, errNoHub},
	{app.ErrThreadArchived, errThreadArchived},
	{app.ErrWorkspaceBusy, errThreadBusy},
	{app.ErrQueueFull, errThreadBusy},
	{conversation.ErrRefused, errRefused},
	{app.ErrEnvironmentInUse, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidMessage, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidAnswer, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidDecision, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidPath, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidQuery, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidPresence, jsonrpc.CodeInvalidParams},
	{app.ErrInvalidTodo, jsonrpc.CodeInvalidParams},
	{app.ErrSearchTimedOut, errSearchTimedOut},
	{account.ErrInvalidName, jsonrpc.CodeInvalidParams},
	{account.ErrInvalidMCPServer, jsonrpc.CodeInvalidParams},
	{account.ErrUnknownModel, jsonrpc.CodeInvalidParams},
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
