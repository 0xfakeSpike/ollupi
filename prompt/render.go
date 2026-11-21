package prompt

import (
	"encoding/json"
	"strings"
)

func OutputText(raw json.RawMessage) string {
	v := decoded(raw)
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(prettyJSON(v))
}
