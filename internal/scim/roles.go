package scim

import "strings"

// Outline team roles.
const (
	roleAdmin  = "admin"
	roleMember = "member"
	roleViewer = "viewer"
)

// RoleMap maps SCIM group displayNames to Outline team roles. The viewer set is
// carried for documentation/parity but the derivation default is already viewer,
// so it does not change the outcome.
type RoleMap struct {
	Admin  []string
	Member []string
	Viewer []string
}

// DeriveRole computes the Outline role a user should hold given the display
// names of the groups they belong to. Admin membership wins over member, which
// wins over the viewer default. Comparison is case-insensitive.
func (rm RoleMap) DeriveRole(groupNames []string) string {
	set := make(map[string]bool, len(groupNames))
	for _, n := range groupNames {
		set[strings.ToLower(strings.TrimSpace(n))] = true
	}
	if containsAny(set, rm.Admin) {
		return roleAdmin
	}
	if containsAny(set, rm.Member) {
		return roleMember
	}
	return roleViewer
}

// containsAny reports whether any name in names is present in set (lowercased).
func containsAny(set map[string]bool, names []string) bool {
	for _, n := range names {
		if set[strings.ToLower(strings.TrimSpace(n))] {
			return true
		}
	}
	return false
}

// joinName builds Outline's single display name from a SCIM Name. It prefers the
// formatted value and otherwise joins given + family.
func joinName(n *Name) string {
	if n == nil {
		return ""
	}
	if s := strings.TrimSpace(n.Formatted); s != "" {
		return s
	}
	return strings.TrimSpace(strings.TrimSpace(n.GivenName + " " + n.FamilyName))
}

// splitName reconstructs a SCIM Name from Outline's single display name by
// splitting on the first space. This is lossy but round-trips the common case.
func splitName(full string) *Name {
	full = strings.TrimSpace(full)
	if full == "" {
		return nil
	}
	n := &Name{Formatted: full}
	if i := strings.IndexByte(full, ' '); i >= 0 {
		n.GivenName = full[:i]
		n.FamilyName = strings.TrimSpace(full[i+1:])
	} else {
		n.GivenName = full
	}
	return n
}
