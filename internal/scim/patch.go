package scim

import (
	"encoding/json"
	"fmt"
	"strings"
)

// patchRequest is the SCIM PATCH body (RFC 7644 §3.5.2).
type patchRequest struct {
	Schemas    []string  `json:"schemas"`
	Operations []patchOp `json:"Operations"`
}

// patchOp is a single PATCH operation. Value is left raw because its shape
// depends on op and path (scalar for `active`, array of members for `members`).
type patchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// userPatchResult is the reduced intent of a set of user PATCH ops. Nil pointers
// mean "unchanged"; the handler applies only what is set.
type userPatchResult struct {
	setActive     *bool
	setName       *string
	setExternalID *string
}

// applyUserPatch reduces user PATCH operations into a userPatchResult. It
// supports `replace` (and `add`, treated identically) on `active`, `name.*`,
// `displayName` and `userName`, and the path-less replace form where value is a
// map of attributes. `remove` is not meaningful for these scalars and is ignored.
func applyUserPatch(ops []patchOp) (userPatchResult, error) {
	var res userPatchResult
	for _, op := range ops {
		action := strings.ToLower(op.Op)
		if action == "remove" {
			continue
		}
		if action != "replace" && action != "add" {
			return res, fmt.Errorf("unsupported user patch op %q", op.Op)
		}

		path := strings.ToLower(strings.TrimSpace(op.Path))
		if path == "" {
			// Path-less: value is an object of attributes to replace.
			var attrs map[string]json.RawMessage
			if err := json.Unmarshal(op.Value, &attrs); err != nil {
				return res, fmt.Errorf("patch value is not an object: %w", err)
			}
			for k, v := range attrs {
				if err := res.applyUserAttr(strings.ToLower(k), v); err != nil {
					return res, err
				}
			}
			continue
		}
		if err := res.applyUserAttr(path, op.Value); err != nil {
			return res, err
		}
	}
	return res, nil
}

// applyUserAttr records a single attribute assignment onto the result.
func (res *userPatchResult) applyUserAttr(path string, raw json.RawMessage) error {
	switch path {
	case "active":
		b, err := decodeBool(raw)
		if err != nil {
			return fmt.Errorf("active: %w", err)
		}
		res.setActive = &b
	case "name":
		var n Name
		if err := json.Unmarshal(raw, &n); err != nil {
			return fmt.Errorf("name: %w", err)
		}
		formatted := joinName(&n)
		if formatted != "" {
			res.setName = &formatted
		}
	case "name.formatted", "displayname":
		s, err := decodeString(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		res.setName = &s
	case "name.givenname", "name.familyname":
		// A single component alone is not enough to rebuild the full name; the
		// handler falls back to a PUT for structured renames. Ignore here.
	case "externalid":
		s, err := decodeString(raw)
		if err != nil {
			return fmt.Errorf("externalId: %w", err)
		}
		res.setExternalID = &s
	case "username", "emails", "emails.value":
		// userName == Outline email, which is immutable via SCIM PATCH. Accept
		// silently so authentik does not error.
	default:
		// Unknown attributes are tolerated: authentik sends extra fields we do
		// not model, and rejecting them would fail otherwise-valid requests.
	}
	return nil
}

// memberDelta is the reduced intent of group `members` PATCH ops. When
// replaceAll is true, members is the complete desired set; otherwise add and
// remove are applied as deltas.
type memberDelta struct {
	add        []string
	remove     []string
	members    []string
	replaceAll bool
}

// applyGroupMemberPatch reduces group PATCH operations touching `members`.
// Non-member paths (e.g. a displayName replace) are ignored here and handled
// separately by the group handler.
func applyGroupMemberPatch(ops []patchOp) (memberDelta, error) {
	var d memberDelta
	for _, op := range ops {
		action := strings.ToLower(op.Op)
		path := strings.TrimSpace(op.Path)
		lpath := strings.ToLower(path)

		// A remove targeting a specific member via a value filter:
		// path = `members[value eq "<id>"]`.
		if action == "remove" && strings.HasPrefix(lpath, "members[") {
			id, err := memberIDFromPath(path)
			if err != nil {
				return d, err
			}
			d.remove = append(d.remove, id)
			continue
		}

		if lpath != "members" {
			continue
		}

		ids, err := memberValues(op.Value)
		if err != nil {
			return d, err
		}
		switch action {
		case "add":
			d.add = append(d.add, ids...)
		case "remove":
			// remove with path=members and a value list, or no value (clear all).
			if len(ids) == 0 {
				d.replaceAll = true
				d.members = nil
			} else {
				d.remove = append(d.remove, ids...)
			}
		case "replace":
			d.replaceAll = true
			d.members = ids
		default:
			return d, fmt.Errorf("unsupported members op %q", op.Op)
		}
	}
	return d, nil
}

// memberValues extracts user ids from a members value, accepting either an array
// of member objects/strings or a single member object.
func memberValues(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") {
		var arr []Member
		if err := json.Unmarshal(raw, &arr); err != nil {
			return nil, fmt.Errorf("members value: %w", err)
		}
		out := make([]string, 0, len(arr))
		for _, m := range arr {
			if m.Value != "" {
				out = append(out, m.Value)
			}
		}
		return out, nil
	}
	var m Member
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("members value: %w", err)
	}
	if m.Value == "" {
		return nil, nil
	}
	return []string{m.Value}, nil
}

// memberIDFromPath extracts <id> from `members[value eq "<id>"]`.
func memberIDFromPath(path string) (string, error) {
	open := strings.Index(path, "[")
	close := strings.LastIndex(path, "]")
	if open < 0 || close < 0 || close < open {
		return "", fmt.Errorf("malformed member path %q", path)
	}
	inner := path[open+1 : close]
	f, err := parseFilter(inner)
	if err != nil || f == nil {
		return "", fmt.Errorf("member path filter %q: %v", inner, err)
	}
	if f.attr != "value" {
		return "", fmt.Errorf("member path must filter on value, got %q", f.attr)
	}
	return f.value, nil
}

// decodeBool accepts a JSON bool or a quoted "true"/"false" string, which some
// SCIM clients (including authentik in places) emit.
func decodeBool(raw json.RawMessage) (bool, error) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	return false, fmt.Errorf("not a boolean: %s", string(raw))
}

// decodeString decodes a JSON string value.
func decodeString(raw json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("not a string: %s", string(raw))
	}
	return s, nil
}
