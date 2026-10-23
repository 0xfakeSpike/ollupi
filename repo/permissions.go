package repo

type PermissionRules struct {
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

func (r PermissionRules) Set() bool {
	return len(r.Allow) > 0 || len(r.Ask) > 0 || len(r.Deny) > 0
}
