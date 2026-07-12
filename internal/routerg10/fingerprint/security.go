package fingerprint

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

var ErrForbiddenField = errors.New("fingerprint: forbidden field in output")

// AssertNoForbiddenFields rejects JSON that contains secret or identifying fields.
func AssertNoForbiddenFields(raw []byte) error {
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("fingerprint: malformed json: %w", err)
	}
	return walkNoForbidden(probe, "")
}

func walkNoForbidden(node any, prefix string) error {
	switch typed := node.(type) {
	case map[string]any:
		for key, value := range typed {
			full := key
			if prefix != "" {
				full = prefix + "." + key
			}
			lower := stringsLower(key)
			if slices.Contains(ForbiddenOutputFields, lower) {
				return fmt.Errorf("%w: %q", ErrForbiddenField, full)
			}
			if err := walkNoForbidden(value, full); err != nil {
				return err
			}
		}
	case []any:
		for i, value := range typed {
			full := fmt.Sprintf("%s[%d]", prefix, i)
			if err := walkNoForbidden(value, full); err != nil {
				return err
			}
		}
	}
	return nil
}

func stringsLower(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// RedactMap removes forbidden keys from a generic map (used in tests).
func RedactMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		if slices.Contains(ForbiddenOutputFields, stringsLower(key)) {
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			out[key] = RedactMap(typed)
		default:
			out[key] = value
		}
	}
	return out
}
