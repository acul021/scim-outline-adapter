package scim

import "testing"

func TestDeriveRole(t *testing.T) {
	rm := RoleMap{
		Admin:  []string{"Admins"},
		Member: []string{"Staff", "Editors"},
		Viewer: []string{"Viewers"},
	}
	tests := []struct {
		name   string
		groups []string
		want   string
	}{
		{"no groups -> viewer", nil, roleViewer},
		{"only viewer group -> viewer", []string{"Viewers"}, roleViewer},
		{"member group -> member", []string{"Editors"}, roleMember},
		{"admin group -> admin", []string{"Admins"}, roleAdmin},
		{"admin wins over member", []string{"Editors", "Admins"}, roleAdmin},
		{"member wins over viewer", []string{"Viewers", "Staff"}, roleMember},
		{"case-insensitive", []string{"admins"}, roleAdmin},
		{"unknown group -> viewer", []string{"Something"}, roleViewer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rm.DeriveRole(tt.groups); got != tt.want {
				t.Fatalf("DeriveRole(%v) = %q, want %q", tt.groups, got, tt.want)
			}
		})
	}
}

func TestJoinSplitName(t *testing.T) {
	if got := joinName(&Name{GivenName: "Ada", FamilyName: "Lovelace"}); got != "Ada Lovelace" {
		t.Fatalf("joinName = %q", got)
	}
	if got := joinName(&Name{Formatted: "Grace Hopper"}); got != "Grace Hopper" {
		t.Fatalf("joinName formatted = %q", got)
	}
	n := splitName("Ada Lovelace")
	if n.GivenName != "Ada" || n.FamilyName != "Lovelace" || n.Formatted != "Ada Lovelace" {
		t.Fatalf("splitName = %+v", n)
	}
	if splitName("") != nil {
		t.Fatal("expected nil for empty name")
	}
}
