package agent

import (
	"fmt"
	"slices"

	"github.com/0xfakeSpike/ollupi/agent/permission"
	"github.com/0xfakeSpike/ollupi/repo"
	"github.com/0xfakeSpike/ollupi/tools"
	"github.com/0xfakeSpike/ollupi/tools/subagent"
)

func (a *Agent) policyFor(t repo.ThreadRow) permission.Policy {
	if t.ApprovalPolicy != "" {
		return permission.Policy(t.ApprovalPolicy)
	}
	if w, err := a.workspaceOf(t); err == nil && w.ApprovalPolicy != "" {
		return permission.Policy(w.ApprovalPolicy)
	}
	return a.ApprovalPolicy
}

func (a *Agent) rulesFor(t repo.ThreadRow) permission.Rules {
	rules := a.Permissions
	if w, err := a.workspaceOf(t); err == nil && w.Permissions.Set() {
		rules = w.Permissions
	}
	if t.ParentThreadID != "" {
		rules.Deny = append(slices.Clone(rules.Deny), subagent.Tool)
	}
	return rules
}

func (a *Agent) workspaceOf(t repo.ThreadRow) (repo.WorkspaceRow, error) {
	if t.WorkspaceID == "" {
		return repo.WorkspaceRow{}, repo.ErrNotFound
	}
	return a.DB.Workspace(t.WorkspaceID)
}

func (a *Agent) toolsFor(names []string, policy permission.Policy, rules permission.Rules) ([]tools.ToolDescriptor, *permission.Checker, error) {
	checker, err := permission.New(policy, rules, a.registry)
	if err != nil {
		return nil, nil, err
	}
	catalog := a.registry.Descriptors()
	descriptors := catalog
	if names != nil {
		for i, name := range names {
			if slices.Contains(names[:i], name) {
				return nil, nil, fmt.Errorf("tool %q is listed twice", name)
			}
			if !slices.ContainsFunc(catalog, func(t tools.ToolDescriptor) bool { return t.Name == name }) {
				return nil, nil, fmt.Errorf("tool %q is not in the tool registry", name)
			}
		}
		descriptors = slices.DeleteFunc(slices.Clone(catalog), func(t tools.ToolDescriptor) bool {
			return !slices.Contains(names, t.Name)
		})
	}
	return slices.DeleteFunc(slices.Clone(descriptors), func(t tools.ToolDescriptor) bool { return !checker.Offers(t.Name) }), checker, nil
}
