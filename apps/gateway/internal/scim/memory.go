package scim

import (
	"context"
	"strings"
	"sync"
	"time"
)

// MemoryStore is an in-memory, tenant-scoped SCIM Store guarded by a mutex.
// It is suitable for tests and small single-process deployments.
type MemoryStore struct {
	mu     sync.Mutex
	now    func() time.Time
	users  map[string]User  // key: tenantID + "\x00" + id
	groups map[string]Group // key: tenantID + "\x00" + id
}

// NewMemoryStore returns an empty in-memory SCIM store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		now:    func() time.Time { return time.Now() },
		users:  map[string]User{},
		groups: map[string]Group{},
	}
}

// Ready always succeeds for the in-memory store.
func (m *MemoryStore) Ready(context.Context) error { return nil }

func scopeKey(tenantID, id string) string { return tenantID + "\x00" + id }

// --- Users ---

func (m *MemoryStore) CreateUser(_ context.Context, tenantID string, user User) (User, error) {
	if err := validateUserWrite(user); err != nil {
		return User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkUserUniqueLocked(tenantID, "", user); err != nil {
		return User{}, err
	}
	now := nowUTC(m.now)
	user.ID = newResourceID("usr")
	stored := stampUser(user, now, now)
	m.users[scopeKey(tenantID, stored.ID)] = stored
	return cloneUser(stored), nil
}

func (m *MemoryStore) GetUser(_ context.Context, tenantID, id string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.users[scopeKey(tenantID, id)]
	if !ok {
		return User{}, errNotFound("user not found: " + id)
	}
	return cloneUser(stored), nil
}

func (m *MemoryStore) ReplaceUser(_ context.Context, tenantID, id string, user User) (User, error) {
	if err := validateUserWrite(user); err != nil {
		return User{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.users[scopeKey(tenantID, id)]
	if !ok {
		return User{}, errNotFound("user not found: " + id)
	}
	if err := m.checkUserUniqueLocked(tenantID, id, user); err != nil {
		return User{}, err
	}
	now := nowUTC(m.now)
	user.ID = id
	created := parseStoredTime(existing.Meta.Created, now)
	stored := stampUser(user, created, now)
	m.users[scopeKey(tenantID, id)] = stored
	return cloneUser(stored), nil
}

func (m *MemoryStore) PatchUser(_ context.Context, tenantID, id string, ops []PatchOperation) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.users[scopeKey(tenantID, id)]
	if !ok {
		return User{}, errNotFound("user not found: " + id)
	}
	patched := cloneUser(existing)
	if err := applyUserPatch(&patched, ops); err != nil {
		return User{}, err
	}
	if err := validateUserWrite(patched); err != nil {
		return User{}, err
	}
	if err := m.checkUserUniqueLocked(tenantID, id, patched); err != nil {
		return User{}, err
	}
	now := nowUTC(m.now)
	created := parseStoredTime(existing.Meta.Created, now)
	stored := stampUser(patched, created, now)
	m.users[scopeKey(tenantID, id)] = stored
	return cloneUser(stored), nil
}

func (m *MemoryStore) DeleteUser(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := scopeKey(tenantID, id)
	if _, ok := m.users[key]; !ok {
		return errNotFound("user not found: " + id)
	}
	delete(m.users, key)
	return nil
}

func (m *MemoryStore) ListUsers(_ context.Context, tenantID string, params ListParams) (ListResponse, error) {
	filter, err := parseFilter(params.Filter, userFilterAttrs)
	if err != nil {
		return ListResponse{}, err
	}
	startIndex, count := normalizeListParams(params)
	m.mu.Lock()
	defer m.mu.Unlock()
	matched := make([]User, 0)
	prefix := tenantID + "\x00"
	for key, u := range m.users {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if matchUserFilter(u, filter) {
			matched = append(matched, cloneUser(u))
		}
	}
	sortUsers(matched)
	window := paginate(matched, startIndex, count)
	return buildUserListResponse(window, len(matched), startIndex), nil
}

// checkUserUniqueLocked enforces per-tenant uniqueness of userName and
// externalId, ignoring the resource identified by excludeID (for updates).
func (m *MemoryStore) checkUserUniqueLocked(tenantID, excludeID string, candidate User) error {
	prefix := tenantID + "\x00"
	for key, u := range m.users {
		if !strings.HasPrefix(key, prefix) || u.ID == excludeID {
			continue
		}
		if candidate.UserName != "" && u.UserName == candidate.UserName {
			return errConflict("userName already exists: " + candidate.UserName)
		}
		if candidate.ExternalID != "" && u.ExternalID == candidate.ExternalID {
			return errConflict("externalId already exists: " + candidate.ExternalID)
		}
	}
	return nil
}

// --- Groups ---

func (m *MemoryStore) CreateGroup(_ context.Context, tenantID string, group Group) (Group, error) {
	if err := validateGroupWrite(group); err != nil {
		return Group{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkGroupUniqueLocked(tenantID, "", group); err != nil {
		return Group{}, err
	}
	now := nowUTC(m.now)
	group.ID = newResourceID("grp")
	stored := stampGroup(group, now, now)
	m.groups[scopeKey(tenantID, stored.ID)] = stored
	return cloneGroup(stored), nil
}

func (m *MemoryStore) GetGroup(_ context.Context, tenantID, id string) (Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.groups[scopeKey(tenantID, id)]
	if !ok {
		return Group{}, errNotFound("group not found: " + id)
	}
	return cloneGroup(stored), nil
}

func (m *MemoryStore) ReplaceGroup(_ context.Context, tenantID, id string, group Group) (Group, error) {
	if err := validateGroupWrite(group); err != nil {
		return Group{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.groups[scopeKey(tenantID, id)]
	if !ok {
		return Group{}, errNotFound("group not found: " + id)
	}
	if err := m.checkGroupUniqueLocked(tenantID, id, group); err != nil {
		return Group{}, err
	}
	now := nowUTC(m.now)
	group.ID = id
	created := parseStoredTime(existing.Meta.Created, now)
	stored := stampGroup(group, created, now)
	m.groups[scopeKey(tenantID, id)] = stored
	return cloneGroup(stored), nil
}

func (m *MemoryStore) PatchGroup(_ context.Context, tenantID, id string, ops []PatchOperation) (Group, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.groups[scopeKey(tenantID, id)]
	if !ok {
		return Group{}, errNotFound("group not found: " + id)
	}
	patched := cloneGroup(existing)
	if err := applyGroupPatch(&patched, ops); err != nil {
		return Group{}, err
	}
	if err := validateGroupWrite(patched); err != nil {
		return Group{}, err
	}
	if err := m.checkGroupUniqueLocked(tenantID, id, patched); err != nil {
		return Group{}, err
	}
	now := nowUTC(m.now)
	created := parseStoredTime(existing.Meta.Created, now)
	stored := stampGroup(patched, created, now)
	m.groups[scopeKey(tenantID, id)] = stored
	return cloneGroup(stored), nil
}

func (m *MemoryStore) DeleteGroup(_ context.Context, tenantID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := scopeKey(tenantID, id)
	if _, ok := m.groups[key]; !ok {
		return errNotFound("group not found: " + id)
	}
	delete(m.groups, key)
	return nil
}

func (m *MemoryStore) ListGroups(_ context.Context, tenantID string, params ListParams) (ListResponse, error) {
	filter, err := parseFilter(params.Filter, groupFilterAttrs)
	if err != nil {
		return ListResponse{}, err
	}
	startIndex, count := normalizeListParams(params)
	m.mu.Lock()
	defer m.mu.Unlock()
	matched := make([]Group, 0)
	prefix := tenantID + "\x00"
	for key, g := range m.groups {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if matchGroupFilter(g, filter) {
			matched = append(matched, cloneGroup(g))
		}
	}
	sortGroups(matched)
	window := paginate(matched, startIndex, count)
	return buildGroupListResponse(window, len(matched), startIndex), nil
}

func (m *MemoryStore) checkGroupUniqueLocked(tenantID, excludeID string, candidate Group) error {
	if candidate.ExternalID == "" {
		return nil
	}
	prefix := tenantID + "\x00"
	for key, g := range m.groups {
		if !strings.HasPrefix(key, prefix) || g.ID == excludeID {
			continue
		}
		if g.ExternalID == candidate.ExternalID {
			return errConflict("externalId already exists: " + candidate.ExternalID)
		}
	}
	return nil
}

func cloneUser(u User) User {
	u.Schemas = cloneStrings(u.Schemas)
	u.Emails = cloneEmails(u.Emails)
	u.Groups = cloneGroupRefs(u.Groups)
	u.Password = ""
	return u
}

func cloneGroup(g Group) Group {
	g.Schemas = cloneStrings(g.Schemas)
	g.Members = cloneMemberRefs(g.Members)
	return g
}

// parseStoredTime parses an RFC3339 timestamp from meta, falling back to def.
func parseStoredTime(value string, def time.Time) time.Time {
	if value == "" {
		return def
	}
	if parsed, err := time.Parse(timeLayout, value); err == nil {
		return parsed.UTC()
	}
	return def
}
