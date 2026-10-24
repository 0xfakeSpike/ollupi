package repo

type PermissionRules struct {
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

func (r PermissionRules) Set() bool {
	return len(r.Allow) > 0 || len(r.Ask) > 0 || len(r.Deny) > 0
}

type PermissionSettings struct {
	ApprovalPolicy string          `json:"approvalPolicy,omitempty"`
	Rules          PermissionRules `json:"permissions"`
}

func (s PermissionSettings) Set() bool {
	return s.ApprovalPolicy != "" || s.Rules.Set()
}
