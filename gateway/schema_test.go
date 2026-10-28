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

	"github.com/0xfakeSpike/ollupi/agent/memory"
	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/agent/skills"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/cron"
	"github.com/0xfakeSpike/ollupi/tools/plan"
	"github.com/0xfakeSpike/ollupi/tools/shell"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
	"github.com/0xfakeSpike/ollupi/tools/userinput"
	"github.com/google/jsonschema-go/jsonschema"
)

var update = flag.Bool("update", false, "rewrite protocol.schema.json from the protocol table")

// contract is the layout of protocol.schema.json.
type contract struct {
	Schema              string                        `json:"$schema"`
	Defs                map[string]*jsonschema.Schema `json:"$defs"`
	Methods             map[string]messageSchemas     `json:"methods"`
	Notifications       map[string]*jsonschema.Schema `json:"notifications"`
	ServerRequests      map[string]messageSchemas     `json:"serverRequests"`
	ClientNotifications map[string]*jsonschema.Schema `json:"clientNotifications"`
	Errors              map[int]string                `json:"errors"`
	BuiltinTools        map[string]toolSchemas        `json:"builtinTools"`
	Providers           []string                      `json:"providers"`
}

type messageSchemas struct {
	Params *jsonschema.Schema `json:"params"`
	Result *jsonschema.Schema `json:"result"`
}

// toolSchemas tell the UI how to read a built-in tool's call item: the
// arguments the model sent and the output it got back.
type toolSchemas struct {
	Arguments *jsonschema.Schema `json:"arguments,omitempty"`
	Output    *jsonschema.Schema `json:"output,omitempty"`
}

var builtinTools = map[string]struct{ arguments, output reflect.Type }{
	plan.Tool:          {arguments: reflect.TypeFor[plan.Plan]()},
	subagent.Tool:      {arguments: reflect.TypeFor[subagent.Call]()},
	userinput.Tool:     {arguments: reflect.TypeFor[userinput.Request](), output: reflect.TypeFor[userinput.Answers]()},
	shell.ToolName:     {output: reflect.TypeFor[shell.CommandOutput]()},
	memory.ToolSearch:  {arguments: reflect.TypeFor[memory.SearchArgs](), output: reflect.TypeFor[memory.SearchResult]()},
	cron.ToolCreateJob: {arguments: reflect.TypeFor[cron.CreateArgs](), output: reflect.TypeFor[repo.CronJobView]()},
	cron.ToolPauseJob:  {arguments: reflect.TypeFor[cron.PauseArgs](), output: reflect.TypeFor[repo.CronJobView]()},
	cron.ToolDeleteJob: {arguments: reflect.TypeFor[cron.NameArgs](), output: reflect.TypeFor[cron.Deleted]()},
	cron.ToolListJobs:  {arguments: reflect.TypeFor[cron.ListArgs](), output: reflect.TypeFor[cron.JobList]()},
}

// providers are the ids ItemRow.server takes for ollupi's own providers.
var providers = []string{cron.ProviderName, skills.ProviderName, memory.ProviderName}

// enums are the value lists declared beside each string type's constants.
var enums = []any{
	repo.ItemTypeValues, repo.ItemStatusValues, repo.TurnStatusValues, repo.ThreadStatusValues,
	repo.DecisionValues, repo.MemoryRowStatusValues, repo.MemoryScopeValues, permission.PolicyValues, plan.StatusValues, tools.AwaitByValues,
}

// plainStrings are named string types that hold any string.
var plainStrings = []reflect.Type{reflect.TypeFor[repo.Ref]()}

type field struct {
	owner reflect.Type
	name  string
}

// fieldTypes give a field the type of the values it always holds where Go
// cannot: repo cannot import permission for PermissionSettings.ApprovalPolicy.
var fieldTypes = map[field]reflect.Type{
	{reflect.TypeFor[repo.PermissionSettings](), "ApprovalPolicy"}: reflect.TypeFor[permission.Policy](),
}

// arrays are the slice fields the gateway never sends as null.
var arrays = []field{
	{reflect.TypeFor[repo.ThreadView](), "Turns"},
	{reflect.TypeFor[repo.ThreadView](), "Items"},
	{reflect.TypeFor[repo.MemoryRow](), "Sources"},
	{reflect.TypeFor[ThreadListResult](), "Data"},
	{reflect.TypeFor[ModelListResult](), "Data"},
	{reflect.TypeFor[ToolListResult](), "Data"},
	{reflect.TypeFor[MCPServerListResult](), "Data"},
	{reflect.TypeFor[WorkspaceListResult](), "Data"},
	{reflect.TypeFor[CronListResult](), "Data"},
	{reflect.TypeFor[MemoryListResult](), "Data"},
	{reflect.TypeFor[repo.MemoryStats](), "IDs"},
	{reflect.TypeFor[memory.SearchResult](), "Memories"},
	{reflect.TypeFor[cron.JobList](), "Jobs"},
	{reflect.TypeFor[plan.Plan](), "Steps"},
	{reflect.TypeFor[userinput.Request](), "Questions"},
	{reflect.TypeFor[userinput.Question](), "Options"},
	{reflect.TypeFor[ApprovalRequestParams](), "AvailableDecisions"},
	{reflect.TypeFor[UserInputRequestParams](), "Questions"},
}

// defNames name the definitions whose Go names mean little outside their package.
var defNames = map[reflect.Type]string{
	reflect.TypeFor[permission.Policy]():   "ApprovalPolicy",
	reflect.TypeFor[plan.Step]():           "PlanStep",
	reflect.TypeFor[plan.Status]():         "PlanStepStatus",
	reflect.TypeFor[subagent.Call]():       "SubagentCall",
	reflect.TypeFor[userinput.Request]():   "UserInputRequest",
	reflect.TypeFor[userinput.Question]():  "UserInputQuestion",
	reflect.TypeFor[userinput.Option]():    "UserInputOption",
	reflect.TypeFor[userinput.Answer]():    "UserInputAnswer",
	reflect.TypeFor[userinput.Answers]():   "UserInputAnswers",
	reflect.TypeFor[memory.SearchResult](): "MemorySearchResult",
	reflect.TypeFor[memory.SearchArgs]():   "MemorySearchArgs",
	reflect.TypeFor[cron.CreateArgs]():     "CronCreateArgs",
	reflect.TypeFor[cron.PauseArgs]():      "CronPauseArgs",
	reflect.TypeFor[cron.NameArgs]():       "CronNameArgs",
	reflect.TypeFor[cron.ListArgs]():       "CronListArgs",
	reflect.TypeFor[cron.Deleted]():        "CronDeleted",
	reflect.TypeFor[cron.JobList]():        "CronJobList",
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

// constants returns the values of the string constants of type typ declared in dir.
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
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return nil, fmt.Errorf("%s: a %s constant is not a string literal", fset.Position(v.Pos()), typ)
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						return nil, err
					}
					values = append(values, s)
				}
			}
		}
	}
	return values, nil
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
		if strings.Contains(name, tools.ProviderSeparator) {
			return nil, fmt.Errorf("built-in tool %s: its name must not contain %q, which only MCP tools have", name, tools.ProviderSeparator)
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

// visit finds the named types reachable from t that need a definition.
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
	// jsonschema-go accepts only object schemas for embedded structs, so
	// the types t embeds are spelled out in its definition, not referenced.
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
	if c, ok := reflect.Zero(t).Interface().(tools.Constrainer); ok {
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

// nullableRefs rewrites a pointer to a defined type, which jsonschema-go
// gives as a $ref beside "type": "null", into "the type, or null".
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

// jsonName is the name encoding/json gives f, empty for an embedded struct
// whose fields it promotes, and false when f is not encoded.
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
