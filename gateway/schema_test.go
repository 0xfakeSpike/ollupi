package gateway

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/agent"
	"github.com/0xfakeSpike/ollupi/app"
	"github.com/0xfakeSpike/ollupi/ask"
	"github.com/0xfakeSpike/ollupi/conversation"
	"github.com/0xfakeSpike/ollupi/memory"
	"github.com/0xfakeSpike/ollupi/policy"
	"github.com/0xfakeSpike/ollupi/run"
	"github.com/0xfakeSpike/ollupi/store"
	"github.com/0xfakeSpike/ollupi/tool/builtin/hub"
	"github.com/0xfakeSpike/ollupi/tool/builtin/plan"
	"github.com/0xfakeSpike/ollupi/tool/builtin/subagent"
	"github.com/0xfakeSpike/ollupi/tool/builtin/workspace"
	"github.com/0xfakeSpike/ollupi/tool/kit"
	"github.com/google/jsonschema-go/jsonschema"
)

var update = flag.Bool("update", false, "rewrite protocol.schema.json from the protocol table")

type contract struct {
	Schema              string                        `json:"$schema"`
	Defs                map[string]*jsonschema.Schema `json:"$defs"`
	Methods             map[string]messageSchemas     `json:"methods"`
	Notifications       map[string]*jsonschema.Schema `json:"notifications"`
	ServerRequests      map[string]messageSchemas     `json:"serverRequests"`
	ClientNotifications map[string]*jsonschema.Schema `json:"clientNotifications"`
	Errors              map[int]string                `json:"errors"`
	Closes              map[int]Ending                `json:"closes"`
	Handshakes          map[int]Ending                `json:"handshakes"`
	Limits              Limits                        `json:"limits"`
	Paths               map[string]string             `json:"paths"`
	BuiltinTools        map[string]toolSchemas        `json:"builtinTools"`
	Providers           []string                      `json:"providers"`
}

type messageSchemas struct {
	Params *jsonschema.Schema `json:"params"`
	Result *jsonschema.Schema `json:"result"`
}

type toolSchemas struct {
	Arguments *jsonschema.Schema `json:"arguments,omitempty"`
	Output    *jsonschema.Schema `json:"output,omitempty"`
}

var builtinTools = map[string]struct{ arguments, output reflect.Type }{
	agent.PlanTool:              {arguments: reflect.TypeFor[plan.Plan]()},
	string(agent.GeneralSub):    {arguments: reflect.TypeFor[subagent.Call](), output: reflect.TypeFor[run.Launched]()},
	string(agent.BrowserSub):    {arguments: reflect.TypeFor[subagent.Call](), output: reflect.TypeFor[run.Launched]()},
	string(agent.DesignSub):     {arguments: reflect.TypeFor[subagent.Call](), output: reflect.TypeFor[run.Launched]()},
	string(agent.CodebaseSub):   {arguments: reflect.TypeFor[subagent.Call](), output: reflect.TypeFor[run.Launched]()},
	string(agent.FrontendSub):   {arguments: reflect.TypeFor[subagent.Call](), output: reflect.TypeFor[run.Launched]()},
	string(agent.BackendSub):    {arguments: reflect.TypeFor[subagent.Call](), output: reflect.TypeFor[run.Launched]()},
	string(agent.WritingSub):    {arguments: reflect.TypeFor[subagent.Call](), output: reflect.TypeFor[run.Launched]()},
	string(agent.ResearchSub):   {arguments: reflect.TypeFor[subagent.Call](), output: reflect.TypeFor[run.Launched]()},
	string(agent.VerifySub):     {arguments: reflect.TypeFor[subagent.Call](), output: reflect.TypeFor[run.Launched]()},
	ask.Tool:                    {arguments: reflect.TypeFor[ask.Request](), output: reflect.TypeFor[ask.Answers]()},
	agent.ListWorkspacesTool:    {output: reflect.TypeFor[hub.Workspaces]()},
	agent.StartConversationTool: {arguments: reflect.TypeFor[hub.StartArgs](), output: reflect.TypeFor[hub.Started]()},
	agent.SendMessageTool:       {arguments: reflect.TypeFor[hub.SendArgs](), output: reflect.TypeFor[hub.Started]()},
	agent.ListConversationsTool: {arguments: reflect.TypeFor[hub.ListArgs](), output: reflect.TypeFor[hub.Conversations]()},
	agent.ReadConversationTool:  {arguments: reflect.TypeFor[hub.ReadArgs](), output: reflect.TypeFor[hub.Turns]()},
	workspace.ToolRunCommand:    {output: reflect.TypeFor[workspace.CommandOutput]()},
	workspace.ToolEditFile:      {output: reflect.TypeFor[workspace.FileOutput]()},
	workspace.ToolWriteFile:     {output: reflect.TypeFor[workspace.FileOutput]()},
	workspace.ToolReadFile:      {arguments: reflect.TypeFor[workspace.ReadFile](), output: reflect.TypeFor[workspace.ReadFileOutput]()},
	workspace.ToolGlob:          {arguments: reflect.TypeFor[workspace.Glob](), output: reflect.TypeFor[workspace.GlobOutput]()},
	workspace.ToolGrep:          {arguments: reflect.TypeFor[workspace.Grep](), output: reflect.TypeFor[workspace.GrepOutput]()},
}

var preparedExecution = reflect.TypeFor[workspace.Prepared]()

var providers = []string{agent.CronProvider, agent.SkillsProvider, account.MemoryProvider}

var enums = []any{
	conversation.ItemKinds, conversation.ItemStatuses, conversation.ThreadStatuses, conversation.Ends, conversation.Decisions,
	TurnStatuses, memory.Scopes, memory.Kinds, memory.Statuses, memory.FailureCodes, policy.PolicyValues, agent.Names,
	plan.StatusValues, account.WorkspaceKinds, account.ModelAPIs, store.UsageGroups, store.PresenceStates, store.PresenceGroups,
	account.TodoStatuses,
	hub.TodoStatuses,
	app.FileKinds, app.PreviewKinds, app.ChangeKinds, app.StagedValues,
}

var plainStrings = []reflect.Type{}

type field struct {
	owner reflect.Type
	name  string
}

var fieldTypes = map[field]reflect.Type{
	{reflect.TypeFor[policy.Settings](), "ApprovalPolicy"}: reflect.TypeFor[policy.Policy](),
}

var arrays = []field{
	{reflect.TypeFor[ThreadView](), "Turns"},
	{reflect.TypeFor[ThreadView](), "Items"},
	{reflect.TypeFor[MemoryRow](), "Sources"},
	{reflect.TypeFor[ThreadListResult](), "Data"},
	{reflect.TypeFor[ModelListResult](), "Data"},
	{reflect.TypeFor[ToolListResult](), "Data"},
	{reflect.TypeFor[ToolDescriptor](), "Roles"},
	{reflect.TypeFor[MCPServer](), "Roles"},
	{reflect.TypeFor[MCPServerListResult](), "Data"},
	{reflect.TypeFor[WorkspaceListResult](), "Data"},
	{reflect.TypeFor[EnvironmentListResult](), "Data"},
	{reflect.TypeFor[WorkspaceFilesResult](), "Data"},
	{reflect.TypeFor[ChangesResult](), "Files"},
	{reflect.TypeFor[EnvironmentFoldersResult](), "Folders"},
	{reflect.TypeFor[WorkspacePathsParams](), "Paths"},
	{reflect.TypeFor[SkillListResult](), "Data"},
	{reflect.TypeFor[SkillReadResult](), "Files"},
	{reflect.TypeFor[SkillRow](), "Roles"},
	{reflect.TypeFor[SkillRow](), "Files"},
	{reflect.TypeFor[CronListResult](), "Data"},
	{reflect.TypeFor[MemoryListResult](), "Data"},
	{reflect.TypeFor[UsageQueryResult](), "Data"},
	{reflect.TypeFor[Found](), "Threads"},
	{reflect.TypeFor[Found](), "Messages"},
	{reflect.TypeFor[Found](), "CronJobs"},
	{reflect.TypeFor[conversation.MemoryStats](), "IDs"},
	{reflect.TypeFor[plan.Plan](), "Steps"},
	{reflect.TypeFor[hub.Workspaces](), "Workspaces"},
	{reflect.TypeFor[hub.Conversations](), "Conversations"},
	{reflect.TypeFor[hub.Turns](), "Turns"},
	{reflect.TypeFor[hub.Turn](), "Messages"},
	{reflect.TypeFor[ask.Request](), "Questions"},
	{reflect.TypeFor[ask.Question](), "Options"},
	{reflect.TypeFor[Approval](), "AvailableDecisions"},
	{reflect.TypeFor[ApprovalRequestParams](), "AvailableDecisions"},
	{reflect.TypeFor[UserInputRequestParams](), "Questions"},
}

var defNames = map[reflect.Type]string{
	reflect.TypeFor[policy.Policy]():          "ApprovalPolicy",
	reflect.TypeFor[policy.Settings]():        "PermissionSettings",
	reflect.TypeFor[policy.Rules]():           "PermissionRules",
	reflect.TypeFor[agent.Name]():             "AgentRole",
	reflect.TypeFor[plan.Step]():              "PlanStep",
	reflect.TypeFor[plan.Status]():            "PlanStepStatus",
	reflect.TypeFor[account.Providers]():      "WorkspaceProviders",
	reflect.TypeFor[subagent.Call]():          "SubagentCall",
	reflect.TypeFor[hub.Workspace]():          "HubWorkspace",
	reflect.TypeFor[hub.TodoStatus]():         "HubTodoStatus",
	reflect.TypeFor[hub.Workspaces]():         "HubWorkspaces",
	reflect.TypeFor[hub.StartArgs]():          "HubStartConversation",
	reflect.TypeFor[hub.Todo]():               "HubTodo",
	reflect.TypeFor[hub.SendArgs]():           "HubSendMessage",
	reflect.TypeFor[hub.Started]():            "HubMessageSent",
	reflect.TypeFor[hub.ListArgs]():           "HubListConversations",
	reflect.TypeFor[hub.Conversations]():      "HubConversations",
	reflect.TypeFor[hub.Status]():             "HubConversation",
	reflect.TypeFor[hub.ReadArgs]():           "HubReadConversation",
	reflect.TypeFor[hub.Turns]():              "HubTurns",
	reflect.TypeFor[hub.Turn]():               "HubTurn",
	reflect.TypeFor[hub.Message]():            "HubMessage",
	reflect.TypeFor[hub.Ended]():              "HubTurnEnded",
	reflect.TypeFor[run.Launched]():           "SubagentLaunched",
	reflect.TypeFor[ask.Request]():            "UserInputRequest",
	reflect.TypeFor[ask.Question]():           "UserInputQuestion",
	reflect.TypeFor[ask.Option]():             "UserInputOption",
	reflect.TypeFor[ask.Answer]():             "UserInputAnswer",
	reflect.TypeFor[ask.Answers]():            "UserInputAnswers",
	reflect.TypeFor[workspace.Prepared]():     "PreparedExecution",
	reflect.TypeFor[memory.Failure]():         "MemoryFailure",
	reflect.TypeFor[memory.Skip]():            "MemorySkip",
	reflect.TypeFor[memory.Source]():          "MemorySource",
	reflect.TypeFor[conversation.Usage]():     "Usage",
	reflect.TypeFor[conversation.MemoryRef](): "MemoryReference",
}

var special = map[reflect.Type]*jsonschema.Schema{
	reflect.TypeFor[time.Time]():       {Type: "string", Format: "date-time"},
	reflect.TypeFor[json.RawMessage](): {},
}

func TestProtocolSchema(t *testing.T) {
	got, err := generateSchema()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile("protocol.schema.json", got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	committed, err := os.ReadFile("protocol.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, committed) {
		t.Fatal("protocol.schema.json is out of date with the protocol table; run go generate ./gateway")
	}
}

func TestEnumValuesMatchConstants(t *testing.T) {
	for _, list := range enums {
		typ := reflect.TypeOf(list).Elem()
		dir := filepath.Join("..", strings.TrimPrefix(typ.PkgPath(), "github.com/0xfakeSpike/ollupi/"))
		declared, err := constants(dir, typ.Name())
		if err != nil {
			t.Fatal(err)
		}
		listed := enumValues(list)
		slices.Sort(declared)
		slices.Sort(listed)
		if !slices.Equal(declared, listed) {
			t.Errorf("%s: the constants are %q but its value list has %q", typ, declared, listed)
		}
	}
}

func constants(dir, typ string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	var values []string
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			d, ok := decl.(*ast.GenDecl)
			if !ok || d.Tok != token.CONST {
				continue
			}
			for _, spec := range d.Specs {
				vs := spec.(*ast.ValueSpec)
				if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != typ {
					continue
				}
				for _, v := range vs.Values {
					s, err := constantValue(fset, f, v)
					if err != nil {
						return nil, fmt.Errorf("%s: a %s constant: %w", fset.Position(v.Pos()), typ, err)
					}
					values = append(values, s)
				}
			}
		}
	}
	return values, nil
}

func constantValue(fset *token.FileSet, f *ast.File, v ast.Expr) (string, error) {
	switch v := v.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			return strconv.Unquote(v.Value)
		}
	case *ast.SelectorExpr:
		pkg, ok := v.X.(*ast.Ident)
		if !ok {
			break
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if rel, ok := strings.CutPrefix(path, "github.com/0xfakeSpike/ollupi/"); ok && (imp.Name != nil && imp.Name.Name == pkg.Name || imp.Name == nil && filepath.Base(path) == pkg.Name) {
				return declaredIn(fset, filepath.Join("..", rel), v.Sel.Name)
			}
		}
	}
	return "", fmt.Errorf("it is neither a string literal nor a constant of this module that is one")
}

func declaredIn(fset *token.FileSet, dir, name string) (string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return "", err
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			return "", err
		}
		for _, decl := range f.Decls {
			if d, ok := decl.(*ast.GenDecl); ok && d.Tok == token.CONST {
				for _, spec := range d.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, id := range vs.Names {
						if id.Name == name && i < len(vs.Values) {
							return constantValue(fset, f, vs.Values[i])
						}
					}
				}
			}
		}
	}
	return "", fmt.Errorf("%s declares no constant %s", dir, name)
}

func enumValues(list any) []string {
	v := reflect.ValueOf(list)
	out := make([]string, v.Len())
	for i := range out {
		out[i] = v.Index(i).String()
	}
	return out
}

type generator struct {
	defs   map[reflect.Type]string
	names  map[string]reflect.Type
	walked map[reflect.Type]bool
	enums  map[reflect.Type][]any
}

func generateSchema() ([]byte, error) {
	g := &generator{defs: map[reflect.Type]string{}, names: map[string]reflect.Type{}, walked: map[reflect.Type]bool{}, enums: map[reflect.Type][]any{}}
	for _, list := range enums {
		typ := reflect.TypeOf(list).Elem()
		for _, v := range enumValues(list) {
			g.enums[typ] = append(g.enums[typ], v)
		}
	}
	var roots []reflect.Type
	for _, m := range protocol.methods {
		roots = append(roots, m.params, m.result)
	}
	for _, r := range protocol.serverRequests {
		roots = append(roots, r.params, r.result)
	}
	for _, typ := range protocol.notifications {
		roots = append(roots, typ)
	}
	for _, typ := range protocol.clientNotifications {
		roots = append(roots, typ)
	}
	for _, tool := range builtinTools {
		roots = append(roots, tool.arguments, tool.output)
	}
	roots = append(roots, preparedExecution, reflect.TypeFor[UploadResult]())
	for _, root := range roots {
		if root == nil {
			continue
		}
		if err := g.visit(root, false); err != nil {
			return nil, err
		}
	}

	c := contract{
		Schema:              "https://json-schema.org/draft/2020-12/schema",
		Defs:                map[string]*jsonschema.Schema{},
		Methods:             map[string]messageSchemas{},
		Notifications:       map[string]*jsonschema.Schema{},
		ServerRequests:      map[string]messageSchemas{},
		ClientNotifications: map[string]*jsonschema.Schema{},
		Errors:              protocol.errors,
		Closes:              protocol.closes,
		Handshakes:          protocol.handshakes,
		Limits:              protocol.limits,
		Paths:               protocol.paths,
		BuiltinTools:        map[string]toolSchemas{},
		Providers:           providers,
	}
	for typ, name := range g.defs {
		s, err := g.schema(typ)
		if err != nil {
			return nil, err
		}
		c.Defs[name] = s
	}
	for name, m := range protocol.methods {
		c.Methods[name] = messageSchemas{g.ref(m.params), g.ref(m.result)}
	}
	for name, r := range protocol.serverRequests {
		c.ServerRequests[name] = messageSchemas{g.ref(r.params), g.ref(r.result)}
	}
	for name, typ := range protocol.notifications {
		c.Notifications[name] = g.ref(typ)
	}
	for name, typ := range protocol.clientNotifications {
		c.ClientNotifications[name] = g.ref(typ)
	}
	for name, tool := range builtinTools {
		if strings.Contains(name, policy.ProviderSeparator) {
			return nil, fmt.Errorf("built-in tool %s: its name must not contain %q, which only MCP tools have", name, policy.ProviderSeparator)
		}
		c.BuiltinTools[name] = toolSchemas{g.ref(tool.arguments), g.ref(tool.output)}
	}
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	var check contract
	if err := json.Unmarshal(out, &check); err != nil {
		return nil, err
	}
	if _, err := (&jsonschema.Schema{Defs: check.Defs}).Resolve(nil); err != nil {
		return nil, fmt.Errorf("the generated definitions do not resolve: %w", err)
	}
	return append(out, '\n'), nil
}

func (g *generator) visit(t reflect.Type, embedded bool) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if special[t] != nil {
		return nil
	}
	switch t.Kind() {
	case reflect.Bool, reflect.Int, reflect.Int64, reflect.Float64, reflect.Interface:
		return nil
	case reflect.Slice:
		if t.Name() != "" {
			return fmt.Errorf("%s: a named slice needs a schema in special", t)
		}
		return g.visit(t.Elem(), false)
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return fmt.Errorf("%s: map keys must be strings", t)
		}
		if t.Name() != "" {
			if err := g.define(t); err != nil {
				return err
			}
		}
		return g.visit(t.Elem(), false)
	case reflect.String:
		switch {
		case t.PkgPath() == "" || slices.Contains(plainStrings, t):
			return nil
		case g.enums[t] != nil:
			return g.define(t)
		default:
			return fmt.Errorf("%s: a named string type needs its value list in enums, or a place in plainStrings", t)
		}
	case reflect.Struct:
		if t.Name() == "" {
			return fmt.Errorf("an anonymous struct %s is in the protocol; give it a name", t)
		}
		if !embedded {
			if err := g.define(t); err != nil {
				return err
			}
		}
		if g.walked[t] {
			return nil
		}
		g.walked[t] = true
		for i := range t.NumField() {
			f := t.Field(i)
			name, ok := jsonName(f)
			if !ok {
				continue
			}
			typ := f.Type
			if override, ok := fieldTypes[field{t, f.Name}]; ok {
				typ = override
			}
			if err := g.visit(typ, f.Anonymous && name == ""); err != nil {
				return fmt.Errorf("%s.%s: %w", t, f.Name, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("%s cannot be in the protocol", t)
	}
}

func (g *generator) define(t reflect.Type) error {
	if _, ok := g.defs[t]; ok {
		return nil
	}
	name, ok := defNames[t]
	if !ok {
		name = t.Name()
	}
	if !ast.IsExported(name) {
		return fmt.Errorf("%s is in the protocol; export it", t)
	}
	if other, taken := g.names[name]; taken {
		return fmt.Errorf("%s and %s are both named %s; add one to defNames", t, other, name)
	}
	g.defs[t], g.names[name] = name, t
	return nil
}

func (g *generator) ref(t reflect.Type) *jsonschema.Schema {
	if t == nil {
		return nil
	}
	return &jsonschema.Schema{Ref: "#/$defs/" + g.defs[t]}
}

func (g *generator) schema(t reflect.Type) (*jsonschema.Schema, error) {
	if values := g.enums[t]; values != nil {
		return &jsonschema.Schema{Type: "string", Enum: values}, nil
	}
	inline := map[reflect.Type]bool{}
	for _, f := range visibleFields(t) {
		if name, _ := jsonName(f); f.Anonymous && name == "" {
			inline[f.Type] = true
		}
	}
	known := map[reflect.Type]*jsonschema.Schema{}
	for typ, s := range special {
		known[typ] = s
	}
	for typ := range g.defs {
		if typ != t && !inline[typ] {
			known[typ] = g.ref(typ)
		}
	}
	s, err := jsonschema.ForType(t, &jsonschema.ForOptions{TypeSchemas: known})
	if err != nil {
		return nil, err
	}
	for _, f := range visibleFields(t) {
		name, ok := jsonName(f)
		if !ok || s.Properties[name] == nil {
			continue
		}
		owner := t
		for _, i := range f.Index[:len(f.Index)-1] {
			owner = owner.Field(i).Type
		}
		key := field{owner, f.Name}
		if override, ok := fieldTypes[key]; ok {
			s.Properties[name] = g.ref(override)
		}
		if slices.Contains(arrays, key) {
			s.Properties[name].Types, s.Properties[name].Type = nil, "array"
		}
	}
	nullableRefs(s)
	if c, ok := reflect.Zero(t).Interface().(kit.Constrainer); ok {
		c.Constrain(s)
	}
	return s, nil
}

func visibleFields(t reflect.Type) []reflect.StructField {
	if t.Kind() != reflect.Struct {
		return nil
	}
	return reflect.VisibleFields(t)
}

func nullableRefs(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if s.Ref != "" && slices.Equal(s.Types, []string{"null"}) {
		*s = jsonschema.Schema{AnyOf: []*jsonschema.Schema{{Type: "null"}, {Ref: s.Ref}}}
		return
	}
	for _, p := range s.Properties {
		nullableRefs(p)
	}
	nullableRefs(s.Items)
	nullableRefs(s.AdditionalProperties)
}

func jsonName(f reflect.StructField) (string, bool) {
	tag := f.Tag.Get("json")
	if tag == "-" || (!f.IsExported() && !f.Anonymous) {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" && !f.Anonymous {
		name = f.Name
	}
	return name, true
}
