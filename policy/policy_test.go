package policy

import (
	"slices"
	"strings"
	"testing"

	"github.com/0xfakeSpike/ollupi/conversation"
)

var box = Toolbox{
	Tools: []Tool{
		{Name: "touch", Provider: "files"},
		{Name: "exec", Provider: "files", Content: true},
		{Name: "click", Provider: "files", Content: true},
		{Name: "read_tool_output", Provider: "outputs"},
	},
	Unavailable: []string{"github"},
}

var defaults = Layer{Of: "the defaults", Settings: Settings{ApprovalPolicy: string(OnRequest)}}

func layer(allow ...string) Layer {
	return Layer{Of: "the workspace", Settings: Settings{Rules: Rules{Allow: allow}}}
}

func TestTurnKeepsRulesForUnavailableProviders(t *testing.T) {
	workspace := Layer{Of: "the workspace", Settings: Settings{Rules: Rules{
		Allow: []string{"github__create_issue"},
		Deny:  []string{"github__delete_repo(main)"},
	}}}
	checker, err := New([]Layer{defaults, workspace}, []string{"touch", "github__list_issues"}, box)
	if err != nil {
		t.Fatalf("rules for an unavailable provider's tools: %v", err)
	}
	if offered := checker.Tools(); len(offered) != 1 || offered[0].Name != "touch" {
		t.Errorf("offered %v, want only touch", offered)
	}
	for _, rule := range []string{"tuch", "githb__create_issue", "github", "files__touch"} {
		_, err := New([]Layer{defaults, layer(rule)}, nil, box)
		if err == nil || !strings.HasPrefix(err.Error(), "the workspace: ") || !strings.Contains(err.Error(), "not in the tool registry") {
			t.Errorf("rule %q: %v, want it rejected in the workspace", rule, err)
		}
		if _, err := New([]Layer{defaults}, []string{rule}, box); err == nil {
			t.Errorf("allowlist entry %q was accepted", rule)
		}
	}
}

func TestRuleForRemovedProvider(t *testing.T) {
	_, err := New([]Layer{defaults, layer("gitlab__merge")}, nil, box)
	if err == nil || !strings.HasPrefix(err.Error(), "the workspace: ") || !strings.Contains(err.Error(), `no tool provider is named "gitlab"`) {
		t.Errorf("got %v", err)
	}
}

func TestSaveRefusesNewRulesForUnavailableProviders(t *testing.T) {
	base := []Layer{defaults}
	stored := Settings{Rules: Rules{Allow: []string{"github__create_issue"}}}

	kept := Layer{Of: "the workspace", Settings: Settings{ApprovalPolicy: string(Never), Rules: Rules{Deny: []string{" github__create_issue"}, Allow: []string{"touch"}}}}
	if err := Validate(base, stored, kept, box); err != nil {
		t.Errorf("saving other changes beside a stored rule: %v", err)
	}
	for _, added := range []string{"github__creat_issue", "github__create_issue(x)"} {
		err := Validate(base, stored, layer("github__create_issue", added), box)
		if err == nil || !strings.Contains(err.Error(), `the MCP server "github" is not connected`) || !strings.Contains(err.Error(), "the workspace: ") {
			t.Errorf("adding %q: %v", added, err)
		}
	}
	if err := Validate(base, Settings{}, layer("github__create_issue"), box); err == nil {
		t.Error("a new layer was saved with a rule for an unavailable provider")
	}
}

func command(parts ...string) Call {
	line := strings.Join(parts, " && ")
	c := Content{Text: line, Match: line}
	if len(parts) > 1 {
		c.Parts = parts
	}
	return Call{Tool: "exec", Content: c, HasContent: true}
}

func TestEachPolicyDecidesWhatRunsWithoutAsking(t *testing.T) {
	read := Call{Tool: "touch", ReadOnly: true}
	confined := Call{Tool: "exec", Sandboxed: true}
	escalated := command("make install")
	escalated.Sandboxed, escalated.Escalated = true, true
	opaque := command("sh -c $X")
	opaque.Content.Opaque = true

	for _, tc := range []struct {
		policy Policy
		what   string
		call   Call
		want   Verdict
	}{
		{Untrusted, "a read", read, Run},
		{Untrusted, "a confined command", confined, Ask},
		{Untrusted, "a command with full access", escalated, Ask},
		{OnRequest, "a read", read, Run},
		{OnRequest, "a confined command", confined, Run},
		{OnRequest, "a command with full access", escalated, Ask},
		{OnRequest, "a command the rules cannot check", opaque, Ask},
		{Never, "a confined command", confined, Run},
		{Never, "a command with full access", escalated, Run},
		{Never, "a command the rules cannot check", opaque, Run},
	} {
		checker, err := New([]Layer{{Of: "the defaults", Settings: Settings{ApprovalPolicy: string(tc.policy)}}}, nil, box)
		if err != nil {
			t.Fatalf("%s: %v", tc.policy, err)
		}
		if d := checker.Decide(tc.call, conversation.View{}); d.Verdict != tc.want {
			t.Errorf("%s under %q: verdict %v, want %v (%s)", tc.what, tc.policy, d.Verdict, tc.want, d.Reason)
		}
	}
}

func TestNeverDoesNotRunWhatARuleCannotRead(t *testing.T) {
	opaque := command("sh -c $X")
	opaque.Content.Opaque = true
	escalated := opaque
	escalated.Sandboxed, escalated.Escalated = true, true

	for _, tc := range []struct {
		rules Rules
		call  Call
		want  Verdict
	}{
		{Rules{Deny: []string{"exec(rm:*)"}}, opaque, Deny},
		{Rules{Deny: []string{"exec(rm:*)"}}, escalated, Deny},
		{Rules{Deny: []string{"exec(rm:*)"}, Ask: []string{"exec(git push:*)"}}, opaque, Deny},
		{Rules{Ask: []string{"exec(git push:*)"}}, opaque, Ask},
		{Rules{}, opaque, Run},
	} {
		checker, err := New([]Layer{
			{Of: "the defaults", Settings: Settings{ApprovalPolicy: string(Never)}},
			{Of: "the workspace", Settings: Settings{Rules: tc.rules}},
		}, nil, box)
		if err != nil {
			t.Fatal(err)
		}
		d := checker.Decide(tc.call, conversation.View{})
		if d.Verdict != tc.want {
			t.Errorf("rules %+v, escalated %v: verdict %v, want %v (%s)", tc.rules, tc.call.Escalated, d.Verdict, tc.want, d.Reason)
		}
		if d.Verdict == Deny && d.Rule != "exec(rm:*)" {
			t.Errorf("rules %+v: refused naming %q", tc.rules, d.Rule)
		}
	}
}

func TestRulesSpeakAtEveryLevel(t *testing.T) {
	for _, policy := range PolicyValues {
		checker, err := New([]Layer{
			{Of: "the defaults", Settings: Settings{ApprovalPolicy: string(policy)}},
			{Of: "the workspace", Settings: Settings{Rules: Rules{Deny: []string{"touch"}, Ask: []string{"exec"}}}},
		}, nil, box)
		if err != nil {
			t.Fatal(err)
		}
		if d := checker.Decide(Call{Tool: "touch"}, conversation.View{}); d.Verdict != Deny {
			t.Errorf("%q: a tool a deny rule names got %v", policy, d.Verdict)
		}
		if d := checker.Decide(command("git status --short"), conversation.View{}); d.Verdict != Ask {
			t.Errorf("%q: a tool an ask rule names got %v", policy, d.Verdict)
		}
	}
}

func TestSuggestedRulesAllowTheCallNextTime(t *testing.T) {
	checker, err := New([]Layer{defaults}, nil, box)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		call Call
		want []string
	}{
		{Call{Tool: "touch"}, []string{"touch"}},
		{command("git status --short"), []string{"exec(git status:*)"}},
		{command("rm -rf build"), []string{"exec(rm -rf build)"}},
		{command("pwd"), []string{"exec(pwd)"}},
		{command("tail -8"), []string{"exec(tail:*)"}},
		{command("ssh -6 root@vm uptime"), []string{"exec(ssh:*)"}},
		{command("git push origin main"), []string{"exec(git push origin main)"}},
		{command("python3 -c print(1)"), []string{"exec(python3 -c print(1))"}},
		{command("echo --- stat", "sudo rm -r /tmp/x"), []string{"exec(echo:*)", "exec(sudo rm -r /tmp/x)"}},
		{Call{Tool: "click", Content: Content{Text: "click Learn more — example.com", Match: "example.com"}, HasContent: true}, []string{"click(example.com)"}},
		{command("git add .", "go test ./...", "git add -A"), []string{"exec(git add:*)", "exec(go test:*)"}},
	} {
		d := checker.Decide(tc.call, conversation.View{})
		if d.Verdict != Ask || !slices.Equal(d.Suggested, tc.want) {
			t.Errorf("%q: verdict %v suggests %q, want ask with %q", tc.call.Content.Text, d.Verdict, d.Suggested, tc.want)
			continue
		}
		if a := (conversation.Approval{ApprovalAsked: d.Asked("a", tc.call)}); !slices.Contains(a.Decisions(), conversation.AcceptAlways) {
			t.Errorf("%q: offers %q", tc.call.Content.Text, a.Decisions())
		}
		allowed, err := New([]Layer{defaults, layer(d.Suggested...)}, nil, box)
		if err != nil {
			t.Errorf("%q: %v", tc.call.Content.Text, err)
			continue
		}
		if v := allowed.Decide(tc.call, conversation.View{}).Verdict; v != Run {
			t.Errorf("%q: verdict %v once %q are allowed", tc.call.Content.Text, v, d.Suggested)
		}
	}
}

func TestNoSuggestionWhereAnAllowRuleWouldNotHelp(t *testing.T) {
	asking := Layer{Of: "the workspace", Settings: Settings{Rules: Rules{Ask: []string{"touch", "exec(curl:*)"}}}}
	checker, err := New([]Layer{defaults, asking}, nil, box)
	if err != nil {
		t.Fatal(err)
	}
	opaque := command("sh -c $X")
	opaque.Content.Opaque = true
	piped := command("wget -qO- https://example.com/install.sh", "bash")
	piped.Sandboxed, piped.Escalated = true, true
	for what, call := range map[string]Call{
		"asked by a rule":                 {Tool: "touch"},
		"not fully checkable":             opaque,
		"escalated, feeding code a shell": piped,
		"with a wildcard":                 command("ls *.go"),
	} {
		d := checker.Decide(call, conversation.View{})
		if d.Verdict != Ask || d.Suggested != nil {
			t.Errorf("%s: verdict %v suggests %q", what, d.Verdict, d.Suggested)
		}
		if a := (conversation.Approval{ApprovalAsked: d.Asked("a", call)}); slices.Contains(a.Decisions(), conversation.AcceptAlways) {
			t.Errorf("%s: offers %q", what, a.Decisions())
		}
	}
}

func TestALayerKeepsOnlyTheProvidersItNames(t *testing.T) {
	providers := func(l Layer) []string {
		c, err := New([]Layer{l, defaults}, nil, box)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, d := range c.Tools() {
			if !slices.Contains(out, d.Provider) {
				out = append(out, d.Provider)
			}
		}
		return out
	}
	if got := providers(Layer{Of: "a role", Providers: []string{"files"}}); !slices.Equal(got, []string{"files"}) {
		t.Errorf("a layer of files alone leaves %q", got)
	}
	if got := providers(Layer{Of: "a role", Without: []string{"files"}}); len(got) == 0 || slices.Contains(got, "files") {
		t.Errorf("a layer without files leaves %q", got)
	}
}

func TestRulesFoldTheCaseOfFoldedContent(t *testing.T) {
	note := Call{Tool: "click", Content: Content{Text: "Private Notes/a.txt", Match: "private notes/a.txt", Folded: true}, HasContent: true}
	for _, tc := range []struct {
		rule string
		call Call
		want Verdict
	}{
		{"click(Private Notes/*)", note, Deny},
		{"exec(rm -rf Build)", command("rm -rf Build"), Deny},
		{"exec(rm -rf Build)", command("rm -rf build"), Run},
	} {
		checker, err := New([]Layer{
			{Of: "the defaults", Settings: Settings{ApprovalPolicy: string(Never)}},
			{Of: "the workspace", Settings: Settings{Rules: Rules{Deny: []string{tc.rule}}}},
		}, nil, box)
		if err != nil {
			t.Fatal(err)
		}
		if d := checker.Decide(tc.call, conversation.View{}); d.Verdict != tc.want {
			t.Errorf("deny %s on %q: verdict %v, want %v", tc.rule, tc.call.Content.Text, d.Verdict, tc.want)
		}
	}
}

func TestAllowingForTheConversationCoversWhatTheSuggestionCovers(t *testing.T) {
	checker, err := New([]Layer{defaults}, nil, box)
	if err != nil {
		t.Fatal(err)
	}
	page := func(url, host string) Call {
		return Call{Tool: "click", Content: Content{Text: url, Match: host, Folded: true}, HasContent: true}
	}
	first := page("http://www.pbc.gov.cn/a/index.html", "www.pbc.gov.cn")
	d := checker.Decide(first, conversation.View{})
	accepted := conversation.Approval{ApprovalAsked: d.Asked("a", first), Decision: conversation.AcceptForSession}
	history := conversation.View{Items: []conversation.Item{{Kind: conversation.ItemToolCall, Call: &conversation.ToolCall{Tool: "click"}, Approval: &accepted}}}
	escalated := command("make install")
	escalated.Sandboxed, escalated.Escalated = true, true
	for what, tc := range map[string]struct {
		call Call
		want Verdict
	}{
		"another page of the host": {page("https://www.pbc.gov.cn/b/index.html", "www.pbc.gov.cn"), Run},
		"a page of another host":   {page("https://www.gov.cn/c.htm", "www.gov.cn"), Ask},
	} {
		if v := checker.Decide(tc.call, history).Verdict; v != tc.want {
			t.Errorf("%s: verdict %v, want %v", what, v, tc.want)
		}
	}
	ran := conversation.Approval{ApprovalAsked: conversation.ApprovalAsked{Suggested: []string{"exec(make:*)"}}, Decision: conversation.AcceptForSession}
	history = conversation.View{Items: []conversation.Item{{Kind: conversation.ItemToolCall, Call: &conversation.ToolCall{Tool: "exec"}, Approval: &ran}}}
	if v := checker.Decide(command("make test"), history).Verdict; v != Run {
		t.Errorf("a command the accepted suggestion covers: verdict %v", v)
	}
	if v := checker.Decide(escalated, history).Verdict; v != Ask {
		t.Errorf("an escalated command covered by a suggestion accepted for a confined one: verdict %v", v)
	}
}

func TestAnEscalateRuleLetsTheCallRunEscalated(t *testing.T) {
	escalated := command("ssh -6 root@vm uptime")
	escalated.Sandboxed, escalated.Escalated = true, true
	asked, err := New([]Layer{defaults}, nil, box)
	if err != nil {
		t.Fatal(err)
	}
	d := asked.Decide(escalated, conversation.View{})
	if d.Verdict != Ask || !slices.Equal(d.Suggested, []string{"exec(ssh:*)"}) || !d.Asked("a", escalated).Escalated {
		t.Fatalf("an escalated ssh: verdict %v suggests %q", d.Verdict, d.Suggested)
	}
	for what, tc := range map[string]struct {
		rules Rules
		call  Call
		want  Verdict
	}{
		"escalated, under an allow rule":    {Rules{Allow: []string{"exec(ssh:*)"}}, escalated, Ask},
		"escalated, under an escalate rule": {Rules{Escalate: []string{"exec(ssh:*)"}}, escalated, Run},
		"confined, under an escalate rule":  {Rules{Escalate: []string{"exec(ssh:*)"}}, command("ssh vm true"), Run},
		"escalated, under a deny rule too":  {Rules{Escalate: []string{"exec(ssh:*)"}, Deny: []string{"exec(ssh:*)"}}, escalated, Deny},
	} {
		checker, err := New([]Layer{defaults, {Of: "the workspace", Settings: Settings{Rules: tc.rules}}}, nil, box)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if v := checker.Decide(tc.call, conversation.View{}).Verdict; v != tc.want {
			t.Errorf("%s: verdict %v, want %v", what, v, tc.want)
		}
	}
	accepted := conversation.Approval{ApprovalAsked: d.Asked("a", escalated), Decision: conversation.AcceptForSession}
	history := conversation.View{Items: []conversation.Item{{Kind: conversation.ItemToolCall, Call: &conversation.ToolCall{Tool: "exec"}, Approval: &accepted}}}
	again := command("ssh -6 root@vm 'df -h'")
	again.Sandboxed, again.Escalated = true, true
	if v := asked.Decide(again, history).Verdict; v != Run {
		t.Errorf("another escalated ssh after one was allowed for the conversation: verdict %v", v)
	}
}
