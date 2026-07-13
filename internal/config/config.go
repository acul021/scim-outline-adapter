// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config holds runtime configuration loaded from the environment.
type Config struct {
	ListenAddr string

	// SCIMToken is the static bearer token authentik presents. It is compared
	// in constant time on every request.
	SCIMToken string

	OutlineURL   string
	OutlineToken string

	// RoleMap maps an Outline team role to the set of SCIM group displayNames
	// whose members should hold that role. Membership in an admin group wins
	// over a member group, which wins over the default (viewer).
	RoleMapAdmin  []string
	RoleMapMember []string
	RoleMapViewer []string

	// HardDeleteUsers switches SCIM DELETE /Users/{id} from suspend to a
	// permanent users.delete. active=false always suspends regardless.
	HardDeleteUsers bool

	// SuppressInviteEmails stops Outline from mailing an invite for every
	// SCIM-provisioned user. Defaults to true: with IdP-driven provisioning the
	// account is claimed via SSO email match, so invite mail is just noise —
	// set SUPPRESS_INVITE_EMAILS=false to restore Outline's default behavior.
	SuppressInviteEmails bool
}

// FromEnv builds a Config from environment variables and returns an error if
// required values are missing.
func FromEnv() (*Config, error) {
	c := &Config{
		ListenAddr:           os.Getenv("LISTEN_ADDR"),
		SCIMToken:            os.Getenv("SCIM_TOKEN"),
		OutlineURL:           strings.TrimRight(os.Getenv("OUTLINE_URL"), "/"),
		OutlineToken:         os.Getenv("OUTLINE_TOKEN"),
		RoleMapAdmin:         splitCSV(os.Getenv("ROLE_MAP_ADMIN")),
		RoleMapMember:        splitCSV(os.Getenv("ROLE_MAP_MEMBER")),
		RoleMapViewer:        splitCSV(os.Getenv("ROLE_MAP_VIEWER")),
		HardDeleteUsers:      os.Getenv("HARD_DELETE_USERS") == "true",
		SuppressInviteEmails: os.Getenv("SUPPRESS_INVITE_EMAILS") != "false",
	}

	if c.SCIMToken == "" {
		return nil, fmt.Errorf("SCIM_TOKEN is required")
	}
	if c.OutlineURL == "" {
		return nil, fmt.Errorf("OUTLINE_URL is required")
	}
	if c.OutlineToken == "" {
		return nil, fmt.Errorf("OUTLINE_TOKEN is required")
	}
	if c.ListenAddr == "" {
		c.ListenAddr = ":8080"
	}
	return c, nil
}

// splitCSV splits a comma-separated env value, trimming whitespace and dropping
// empty entries so ROLE_MAP_ADMIN="A, B ,, C" yields [A B C].
func splitCSV(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}
