package kanban

import (
	"encoding/base64"
	"encoding/json"
)

// pageCursor is the decoded form of the opaque pagination cursor. It encodes
// the ordering key tuple plus the id tiebreaker (SPEC-08 §5.2 rule 11); the
// server owns the format and callers treat it as a black box.
type pageCursor struct {
	Keys []string `json:"k"`
	ID   string   `json:"id"`
}

func encodeCursor(keys []string, id string) string {
	body, err := json.Marshal(pageCursor{Keys: keys, ID: id})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(body)
}

// decodeCursor returns ok=false for anything unreadable, which callers
// translate into "start from the first page" (rule 11: a stale cursor is not
// an error).
func decodeCursor(s string) (pageCursor, bool) {
	if s == "" {
		return pageCursor{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return pageCursor{}, false
	}
	var c pageCursor
	if err := json.Unmarshal(body, &c); err != nil || c.ID == "" {
		return pageCursor{}, false
	}
	return c, true
}
