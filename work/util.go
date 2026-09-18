package work

import "encoding/json"

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}

// journalSafe reports whether v comes back from the state journal as itself.
// JSON has strings, numbers, booleans, null, arrays and objects; anything
// else is restored as one of those and is no longer what the call produced.
func journalSafe(v any) bool {
	switch t := v.(type) {
	case nil, string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, json.Number:
		return true
	case []any:
		for _, e := range t {
			if !journalSafe(e) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, e := range t {
			if !journalSafe(e) {
				return false
			}
		}
		return true
	}
	return false
}
