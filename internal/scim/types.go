// Package scim implements a SCIM 2.0 (RFC 7643/7644) server that translates
// provisioning requests into Outline API calls. The SCIM resource id is the
// Outline UUID for both users and groups. The only local state is the optional
// user externalId mapping (see ExternalIDStore).
package scim

import (
	"encoding/json"
	"net/http"
)

// contentType is the SCIM media type. Responses always use it; requests are
// accepted with either it or application/json.
const contentType = "application/scim+json"

// SCIM schema URNs.
const (
	schemaUser        = "urn:ietf:params:scim:schemas:core:2.0:User"
	schemaGroup       = "urn:ietf:params:scim:schemas:core:2.0:Group"
	schemaError       = "urn:ietf:params:scim:api:messages:2.0:Error"
	schemaListResp    = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	schemaPatchOp     = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
	schemaServiceConf = "urn:ietf:params:scim:schemas:core:2.0:ServiceProviderConfig"
)

// Name is the SCIM complex name attribute. Outline has only a single display
// name, so on the way in we join given+family and on the way out we split.
type Name struct {
	Formatted  string `json:"formatted,omitempty"`
	GivenName  string `json:"givenName,omitempty"`
	FamilyName string `json:"familyName,omitempty"`
}

// Email is a SCIM multi-valued email entry.
type Email struct {
	Value   string `json:"value"`
	Primary bool   `json:"primary,omitempty"`
	Type    string `json:"type,omitempty"`
}

// Meta is the common SCIM resource metadata.
type Meta struct {
	ResourceType string `json:"resourceType"`
	Location     string `json:"location,omitempty"`
}

// User is a SCIM user resource.
type User struct {
	Schemas    []string `json:"schemas"`
	ID         string   `json:"id"`
	ExternalID string   `json:"externalId,omitempty"`
	UserName   string   `json:"userName"`
	Name       *Name    `json:"name,omitempty"`
	Emails     []Email  `json:"emails,omitempty"`
	Active     bool     `json:"active"`
	Meta       *Meta    `json:"meta,omitempty"`
}

// Member is a SCIM group member reference. Value is the Outline user UUID.
type Member struct {
	Value   string `json:"value"`
	Ref     string `json:"$ref,omitempty"`
	Display string `json:"display,omitempty"`
}

// Group is a SCIM group resource.
type Group struct {
	Schemas     []string `json:"schemas"`
	ID          string   `json:"id"`
	ExternalID  string   `json:"externalId,omitempty"`
	DisplayName string   `json:"displayName"`
	Members     []Member `json:"members"`
	Meta        *Meta    `json:"meta,omitempty"`
}

// ListResponse is the SCIM envelope for list results.
type ListResponse struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	StartIndex   int      `json:"startIndex"`
	ItemsPerPage int      `json:"itemsPerPage"`
	Resources    []any    `json:"Resources"`
}

// scimError is the RFC 7644 error body.
type scimError struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	SCIMType string   `json:"scimType,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// writeJSON writes v as application/scim+json with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a SCIM error response. scimType is optional (pass "").
func writeError(w http.ResponseWriter, status int, scimType, detail string) {
	writeJSON(w, status, scimError{
		Schemas:  []string{schemaError},
		Status:   itoa(status),
		SCIMType: scimType,
		Detail:   detail,
	})
}

// itoa avoids importing strconv in several files for a single conversion.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// serviceProviderConfig is the static capability advertisement. patch and
// filter are supported; bulk, sort, etag and password change are not.
func serviceProviderConfig() map[string]any {
	supported := func(b bool) map[string]any { return map[string]any{"supported": b} }
	return map[string]any{
		"schemas":               []string{schemaServiceConf},
		"documentationUri":      "",
		"patch":                 supported(true),
		"filter":                map[string]any{"supported": true, "maxResults": 200},
		"bulk":                  map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"sort":                  supported(false),
		"changePassword":        supported(false),
		"etag":                  supported(false),
		"authenticationSchemes": []any{},
	}
}
