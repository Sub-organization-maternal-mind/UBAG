package ubag

import (
	"fmt"
	"regexp"
)

var traceparentRE = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$`)

func BuildTraceparent(traceID, spanID string) string {
	return fmt.Sprintf("00-%s-%s-01", traceID, spanID)
}

// ParseTraceparent returns (traceID, spanID, ok).
func ParseTraceparent(value string) (string, string, bool) {
	m := traceparentRE.FindStringSubmatch(value)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}
