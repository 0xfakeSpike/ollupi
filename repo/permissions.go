package repo

type PermissionRules struct {
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

type PermissionSettings struct {
	ApprovalPolicy string          `json:"approvalPolicy,omitempty"`
	Rules          PermissionRules `json:"permissions"`
}
