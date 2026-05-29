// Package scim implements a minimal but RFC 7643/7644 compliant SCIM v2
// provisioning store for the UBAG gateway. It models Users and Groups, supports
// create/get/replace/patch/delete/list operations, SCIM pagination, a minimal
// "eq" filter, and SCIM PatchOp. Two backends are provided: an in-memory store
// (MemoryStore) and a SQLite-backed store (SQLiteStore) using only the Go
// standard library and database/sql.
//
// All resources are scoped by tenantID. The SCIM "password" attribute, if
// present on a write, is dropped and never persisted. Timestamps are stored in
// UTC using RFC3339; every resource carries SCIM meta (resourceType, created,
// lastModified, version/etag, location).
package scim

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SCIM schema URNs (RFC 7643 §8.7 / RFC 7644 §3.x).
const (
	SchemaUser         = "urn:ietf:params:scim:schemas:core:2.0:User"
	SchemaGroup        = "urn:ietf:params:scim:schemas:core:2.0:Group"
	SchemaListResponse = "urn:ietf:params:scim:api:messages:2.0:ListResponse"
	SchemaError        = "urn:ietf:params:scim:api:messages:2.0:Error"
	SchemaPatchOp      = "urn:ietf:params:scim:api:messages:2.0:PatchOp"
)

// SCIM resource types and the canonical content type for SCIM payloads.
const (
	ResourceTypeUser  = "User"
	ResourceTypeGroup = "Group"
	ContentType       = "application/scim+json"
)

// Base location prefixes used for meta.location. These mirror the suggested
// wiring under /v1/scim/v2.
const (
	usersLocationBase  = "/v1/scim/v2/Users/"
	groupsLocationBase = "/v1/scim/v2/Groups/"
)

// timeLayout is the canonical RFC3339 layout (UTC) used for all SCIM times.
const timeLayout = time.RFC3339

// Meta is the common SCIM meta attribute (RFC 7643 §3.1).
type Meta struct {
	ResourceType string `json:"resourceType"`
	Created      string `json:"created"`
	LastModified string `json:"lastModified"`
	Version      string `json:"version"`
	Location     string `json:"location,omitempty"`
}

// Email is a SCIM multi-valued email attribute.
type Email struct {
	Value   string `json:"value"`
	Type    string `json:"type,omitempty"`
	Primary bool   `json:"primary,omitempty"`
	Display string `json:"display,omitempty"`
}

// GroupRef is a reference from a User to a Group it belongs to.
type GroupRef struct {
	Value   string `json:"value"`
	Ref     string `json:"$ref,omitempty"`
	Display string `json:"display,omitempty"`
	Type    string `json:"type,omitempty"`
}

// MemberRef is a reference from a Group to one of its members.
type MemberRef struct {
	Value   string `json:"value"`
	Ref     string `json:"$ref,omitempty"`
	Display string `json:"display,omitempty"`
	Type    string `json:"type,omitempty"`
}

// User maps the SCIM core User resource (RFC 7643 §4.1).
//
// Password is only ever read from inbound writes and is dropped before
// persistence; it is never returned by the store.
type User struct {
	Schemas     []string   `json:"schemas"`
	ID          string     `json:"id"`
	ExternalID  string     `json:"externalId,omitempty"`
	UserName    string     `json:"userName"`
	DisplayName string     `json:"displayName,omitempty"`
	Active      bool       `json:"active"`
	Emails      []Email    `json:"emails,omitempty"`
	Groups      []GroupRef `json:"groups,omitempty"`
	Password    string     `json:"password,omitempty"`
	Meta        Meta       `json:"meta"`
}

// Group maps the SCIM core Group resource (RFC 7643 §4.2).
type Group struct {
	Schemas     []string    `json:"schemas"`
	ID          string      `json:"id"`
	ExternalID  string      `json:"externalId,omitempty"`
	DisplayName string      `json:"displayName"`
	Members     []MemberRef `json:"members,omitempty"`
	Meta        Meta        `json:"meta"`
}

// PatchOperation is a single SCIM PatchOp operation (RFC 7644 §3.5.2).
type PatchOperation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// PatchRequest is the body of a SCIM PATCH request.
type PatchRequest struct {
	Schemas    []string         `json:"schemas"`
	Operations []PatchOperation `json:"Operations"`
}

// ListParams carries SCIM list query parameters.
type ListParams struct {
	// StartIndex is the 1-based index of the first result (SCIM default 1).
	StartIndex int
	// Count is the maximum number of results to return per page.
	Count int
	// Filter is an optional SCIM filter expression. Only a single `eq`
	// comparison on a supported attribute is implemented.
	Filter string
}

// ListResponse is the SCIM ListResponse envelope (RFC 7644 §3.4.2).
type ListResponse struct {
	Schemas      []string `json:"schemas"`
	TotalResults int      `json:"totalResults"`
	StartIndex   int      `json:"startIndex"`
	ItemsPerPage int      `json:"itemsPerPage"`
	Resources    []any    `json:"Resources"`
}

// Store is the tenant-scoped SCIM provisioning interface implemented by both
// MemoryStore and SQLiteStore.
type Store interface {
	Ready(ctx context.Context) error

	CreateUser(ctx context.Context, tenantID string, user User) (User, error)
	GetUser(ctx context.Context, tenantID, id string) (User, error)
	ReplaceUser(ctx context.Context, tenantID, id string, user User) (User, error)
	PatchUser(ctx context.Context, tenantID, id string, ops []PatchOperation) (User, error)
	DeleteUser(ctx context.Context, tenantID, id string) error
	ListUsers(ctx context.Context, tenantID string, params ListParams) (ListResponse, error)

	CreateGroup(ctx context.Context, tenantID string, group Group) (Group, error)
	GetGroup(ctx context.Context, tenantID, id string) (Group, error)
	ReplaceGroup(ctx context.Context, tenantID, id string, group Group) (Group, error)
	PatchGroup(ctx context.Context, tenantID, id string, ops []PatchOperation) (Group, error)
	DeleteGroup(ctx context.Context, tenantID, id string) error
	ListGroups(ctx context.Context, tenantID string, params ListParams) (ListResponse, error)
}

// Error is a SCIM error response (RFC 7644 §3.12). It also implements the Go
// error interface so it can flow through the store API and be unwrapped by the
// transport layer to obtain the HTTP status.
type Error struct {
	Schemas  []string `json:"schemas"`
	Status   string   `json:"status"`
	ScimType string   `json:"scimType,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "scim: <nil>"
	}
	if e.ScimType != "" {
		return "scim error " + e.Status + " (" + e.ScimType + "): " + e.Detail
	}
	return "scim error " + e.Status + ": " + e.Detail
}

// StatusCode returns the numeric HTTP status for this error (0 if unparsable).
func (e *Error) StatusCode() int {
	if e == nil {
		return 0
	}
	n, err := strconv.Atoi(e.Status)
	if err != nil {
		return 0
	}
	return n
}

// NewError builds a SCIM Error with the given HTTP status, scimType and detail.
func NewError(status int, scimType, detail string) *Error {
	return &Error{
		Schemas:  []string{SchemaError},
		Status:   strconv.Itoa(status),
		ScimType: scimType,
		Detail:   detail,
	}
}

// AsError extracts a *Error from err, if present, so callers can read its
// status. It returns (nil, false) for non-SCIM errors.
func AsError(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	if scimErr, ok := err.(*Error); ok {
		return scimErr, true
	}
	return nil, false
}

// Convenience constructors for the SCIM error shapes used by the store.
func errNotFound(detail string) *Error  { return NewError(404, "", detail) }
func errConflict(detail string) *Error  { return NewError(409, "uniqueness", detail) }
func errBadFilter(detail string) *Error { return NewError(400, "invalidFilter", detail) }
func errBadValue(detail string) *Error  { return NewError(400, "invalidValue", detail) }
func errBadPath(detail string) *Error   { return NewError(400, "invalidPath", detail) }

// filterEq is a parsed `attr eq "value"` expression.
type filterEq struct {
	Attr  string
	Value string
}

// userFilterAttrs / groupFilterAttrs are the attributes that may appear on the
// left-hand side of a supported eq filter.
var (
	userFilterAttrs  = map[string]bool{"username": true, "externalid": true, "displayname": true}
	groupFilterAttrs = map[string]bool{"displayname": true, "externalid": true}
)

// parseFilter parses a minimal SCIM filter of the form `attr eq "value"`.
// It returns (nil, nil) for an empty filter and a 400-style *Error for any
// unsupported expression. allowed restricts which attributes are valid.
func parseFilter(filter string, allowed map[string]bool) (*filterEq, error) {
	trimmed := strings.TrimSpace(filter)
	if trimmed == "" {
		return nil, nil
	}
	lower := strings.ToLower(trimmed)
	idx := strings.Index(lower, " eq ")
	if idx < 0 {
		return nil, errBadFilter("only the 'eq' operator is supported")
	}
	attr := strings.TrimSpace(trimmed[:idx])
	rest := strings.TrimSpace(trimmed[idx+len(" eq "):])
	if attr == "" {
		return nil, errBadFilter("missing filter attribute")
	}
	canonical := strings.ToLower(attr)
	if !allowed[canonical] {
		return nil, errBadFilter("unsupported filter attribute: " + attr)
	}
	value, err := unquoteFilterValue(rest)
	if err != nil {
		return nil, err
	}
	return &filterEq{Attr: canonical, Value: value}, nil
}

// unquoteFilterValue parses a SCIM filter literal: a double-quoted string.
func unquoteFilterValue(raw string) (string, error) {
	if len(raw) < 2 || raw[0] != '"' {
		return "", errBadFilter("filter value must be a quoted string")
	}
	value, err := strconv.Unquote(raw)
	if err != nil {
		return "", errBadFilter("malformed quoted filter value")
	}
	return value, nil
}

// nowUTC returns the current time normalized to UTC.
func nowUTC(now func() time.Time) time.Time {
	if now == nil {
		return time.Now().UTC()
	}
	return now().UTC()
}

// formatTime renders t as RFC3339 in UTC.
func formatTime(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// newResourceID returns a random opaque identifier with the given prefix.
func newResourceID(prefix string) string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is fatal-grade; fall back to time-based entropy.
		now := time.Now().UnixNano()
		for i := 0; i < len(buf); i++ {
			buf[i] = byte(now >> (uint(i%8) * 8))
		}
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

// userVersion computes a weak ETag for a user based on its content (excluding
// volatile meta and any password material).
func userVersion(u User) string {
	c := u
	c.Meta = Meta{}
	c.Password = ""
	return contentVersion(c)
}

// groupVersion computes a weak ETag for a group based on its content.
func groupVersion(g Group) string {
	c := g
	c.Meta = Meta{}
	return contentVersion(c)
}

func contentVersion(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		raw = []byte(time.Now().UTC().Format(time.RFC3339Nano))
	}
	sum := sha256.Sum256(raw)
	return `W/"` + hex.EncodeToString(sum[:])[:32] + `"`
}

// normalizeListParams applies SCIM defaults (1-based startIndex, default count).
func normalizeListParams(params ListParams) (startIndex, count int) {
	startIndex = params.StartIndex
	if startIndex <= 0 {
		startIndex = 1
	}
	count = params.Count
	if count <= 0 {
		count = 100
	}
	return startIndex, count
}

// paginate slices resources for the given 1-based startIndex and count.
func paginate[T any](items []T, startIndex, count int) []T {
	if startIndex > len(items) {
		return nil
	}
	begin := startIndex - 1
	end := begin + count
	if end > len(items) {
		end = len(items)
	}
	return items[begin:end]
}

// buildUserListResponse assembles a ListResponse from a window of users.
func buildUserListResponse(window []User, total, startIndex int) ListResponse {
	resources := make([]any, 0, len(window))
	for i := range window {
		resources = append(resources, window[i])
	}
	return ListResponse{
		Schemas:      []string{SchemaListResponse},
		TotalResults: total,
		StartIndex:   startIndex,
		ItemsPerPage: len(window),
		Resources:    resources,
	}
}

// buildGroupListResponse assembles a ListResponse from a window of groups.
func buildGroupListResponse(window []Group, total, startIndex int) ListResponse {
	resources := make([]any, 0, len(window))
	for i := range window {
		resources = append(resources, window[i])
	}
	return ListResponse{
		Schemas:      []string{SchemaListResponse},
		TotalResults: total,
		StartIndex:   startIndex,
		ItemsPerPage: len(window),
		Resources:    resources,
	}
}

// matchUserFilter reports whether a user satisfies the parsed eq filter.
func matchUserFilter(u User, f *filterEq) bool {
	if f == nil {
		return true
	}
	switch f.Attr {
	case "username":
		return u.UserName == f.Value
	case "externalid":
		return u.ExternalID == f.Value
	case "displayname":
		return u.DisplayName == f.Value
	default:
		return false
	}
}

// matchGroupFilter reports whether a group satisfies the parsed eq filter.
func matchGroupFilter(g Group, f *filterEq) bool {
	if f == nil {
		return true
	}
	switch f.Attr {
	case "displayname":
		return g.DisplayName == f.Value
	case "externalid":
		return g.ExternalID == f.Value
	default:
		return false
	}
}

// sortUsers orders users by userName then id for stable pagination.
func sortUsers(users []User) {
	sort.Slice(users, func(i, j int) bool {
		if users[i].UserName == users[j].UserName {
			return users[i].ID < users[j].ID
		}
		return users[i].UserName < users[j].UserName
	})
}

// sortGroups orders groups by displayName then id for stable pagination.
func sortGroups(groups []Group) {
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].DisplayName == groups[j].DisplayName {
			return groups[i].ID < groups[j].ID
		}
		return groups[i].DisplayName < groups[j].DisplayName
	})
}

func cloneEmails(in []Email) []Email {
	if in == nil {
		return nil
	}
	out := make([]Email, len(in))
	copy(out, in)
	return out
}

func cloneGroupRefs(in []GroupRef) []GroupRef {
	if in == nil {
		return nil
	}
	out := make([]GroupRef, len(in))
	copy(out, in)
	return out
}

func cloneMemberRefs(in []MemberRef) []MemberRef {
	if in == nil {
		return nil
	}
	out := make([]MemberRef, len(in))
	copy(out, in)
	return out
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
