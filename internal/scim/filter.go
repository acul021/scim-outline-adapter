package scim

import (
	"fmt"
	"strings"
)

// filter is a parsed SCIM equality filter. Only `attr eq "value"` is supported,
// which is all authentik emits for user/group lookups.
type filter struct {
	attr  string
	value string
}

// parseFilter parses a SCIM `attr eq "value"` expression. Attribute names are
// lowercased for case-insensitive comparison per RFC 7644 §3.4.2.2; the value
// keeps its case. An empty input returns a nil filter and no error (no filter).
func parseFilter(raw string) (*filter, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	// Split off the operator. We only handle "eq"; anything else is rejected so
	// callers return a clear SCIM error instead of silently ignoring the filter.
	fields := strings.SplitN(raw, " ", 3)
	if len(fields) != 3 {
		return nil, fmt.Errorf("unsupported filter %q", raw)
	}
	attr, op, val := fields[0], strings.ToLower(fields[1]), fields[2]
	if op != "eq" {
		return nil, fmt.Errorf("unsupported filter operator %q (only eq)", fields[1])
	}

	val = strings.TrimSpace(val)
	if len(val) < 2 || val[0] != '"' || val[len(val)-1] != '"' {
		return nil, fmt.Errorf("filter value must be quoted: %q", raw)
	}
	val = val[1 : len(val)-1]

	return &filter{attr: strings.ToLower(attr), value: val}, nil
}
