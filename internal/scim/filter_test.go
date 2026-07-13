package scim

import "testing"

func TestParseFilter(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantNil  bool
		wantErr  bool
		wantAttr string
		wantVal  string
	}{
		{name: "empty", in: "", wantNil: true},
		{name: "userName eq", in: `userName eq "alice@example.com"`, wantAttr: "username", wantVal: "alice@example.com"},
		{name: "emails.value eq", in: `emails.value eq "bob@example.com"`, wantAttr: "emails.value", wantVal: "bob@example.com"},
		{name: "externalId eq", in: `externalId eq "abc-123"`, wantAttr: "externalid", wantVal: "abc-123"},
		{name: "displayName eq", in: `displayName eq "Editors"`, wantAttr: "displayname", wantVal: "Editors"},
		{name: "case-insensitive op", in: `userName EQ "x"`, wantAttr: "username", wantVal: "x"},
		{name: "value with spaces", in: `displayName eq "Team Lead"`, wantAttr: "displayname", wantVal: "Team Lead"},
		{name: "unsupported op", in: `userName co "x"`, wantErr: true},
		{name: "unquoted value", in: `userName eq x`, wantErr: true},
		{name: "malformed", in: `userName`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parseFilter(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if f != nil {
					t.Fatalf("expected nil filter, got %+v", f)
				}
				return
			}
			if f.attr != tt.wantAttr || f.value != tt.wantVal {
				t.Fatalf("got attr=%q val=%q, want attr=%q val=%q", f.attr, f.value, tt.wantAttr, tt.wantVal)
			}
		})
	}
}
