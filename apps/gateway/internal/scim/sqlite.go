package scim

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SQLiteStore is a tenant-scoped SCIM Store backed by SQLite via database/sql.
// Emails, group references and member references are stored as JSON text
// columns. meta.version (etag) is recomputed from resource content on every
// write. The store creates its tables (IF NOT EXISTS) on construction.
type SQLiteStore struct {
	db  *sql.DB
	now func() time.Time
}

const sqliteUsersDDL = `
CREATE TABLE IF NOT EXISTS gateway_scim_users (
	tenant_id    TEXT NOT NULL,
	id           TEXT NOT NULL,
	user_name    TEXT NOT NULL,
	external_id  TEXT,
	display_name TEXT NOT NULL DEFAULT '',
	active       INTEGER NOT NULL DEFAULT 1,
	emails_json  TEXT NOT NULL DEFAULT '[]',
	groups_json  TEXT NOT NULL DEFAULT '[]',
	version      TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL,
	PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS gateway_scim_users_username
	ON gateway_scim_users (tenant_id, user_name);
CREATE UNIQUE INDEX IF NOT EXISTS gateway_scim_users_externalid
	ON gateway_scim_users (tenant_id, external_id)
	WHERE external_id IS NOT NULL AND external_id != '';
`

const sqliteGroupsDDL = `
CREATE TABLE IF NOT EXISTS gateway_scim_groups (
	tenant_id    TEXT NOT NULL,
	id           TEXT NOT NULL,
	display_name TEXT NOT NULL,
	external_id  TEXT,
	members_json TEXT NOT NULL DEFAULT '[]',
	version      TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL,
	PRIMARY KEY (tenant_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS gateway_scim_groups_externalid
	ON gateway_scim_groups (tenant_id, external_id)
	WHERE external_id IS NOT NULL AND external_id != '';
`

// NewSQLiteStore constructs a SQLiteStore and ensures its schema exists.
func NewSQLiteStore(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("scim: sqlite store requires a non-nil *sql.DB")
	}
	store := &SQLiteStore{db: db, now: func() time.Time { return time.Now() }}
	if err := store.ensureSchema(context.Background()); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) ensureSchema(ctx context.Context) error {
	for _, ddl := range []string{sqliteUsersDDL, sqliteGroupsDDL} {
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("scim: ensure schema: %w", err)
		}
	}
	return nil
}

func (s *SQLiteStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("scim: sqlite store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	return s.ensureSchema(ctx)
}

// --- Users ---

func (s *SQLiteStore) CreateUser(ctx context.Context, tenantID string, user User) (User, error) {
	if err := validateUserWrite(user); err != nil {
		return User{}, err
	}
	now := nowUTC(s.now)
	user.ID = newResourceID("usr")
	stored := stampUser(user, now, now)
	if err := s.insertUser(ctx, tenantID, stored); err != nil {
		return User{}, err
	}
	return stored, nil
}

func (s *SQLiteStore) insertUser(ctx context.Context, tenantID string, u User) error {
	emails, err := marshalJSON(u.Emails)
	if err != nil {
		return err
	}
	groups, err := marshalJSON(u.Groups)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO gateway_scim_users (
	tenant_id, id, user_name, external_id, display_name, active,
	emails_json, groups_json, version, created_at, updated_at
) VALUES (?, ?, ?, nullif(?, ''), ?, ?, ?, ?, ?, ?, ?)`,
		tenantID, u.ID, u.UserName, u.ExternalID, u.DisplayName, boolToInt(u.Active),
		emails, groups, u.Meta.Version, u.Meta.Created, u.Meta.LastModified)
	if err != nil {
		return mapUniqueErr(err, "user")
	}
	return nil
}

func (s *SQLiteStore) GetUser(ctx context.Context, tenantID, id string) (User, error) {
	row := s.db.QueryRowContext(ctx, selectUserSQL+` WHERE tenant_id = ? AND id = ?`, tenantID, id)
	user, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, errNotFound("user not found: " + id)
	}
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *SQLiteStore) ReplaceUser(ctx context.Context, tenantID, id string, user User) (User, error) {
	if err := validateUserWrite(user); err != nil {
		return User{}, err
	}
	existing, err := s.GetUser(ctx, tenantID, id)
	if err != nil {
		return User{}, err
	}
	now := nowUTC(s.now)
	user.ID = id
	created := parseStoredTime(existing.Meta.Created, now)
	stored := stampUser(user, created, now)
	if err := s.updateUser(ctx, tenantID, stored); err != nil {
		return User{}, err
	}
	return stored, nil
}

func (s *SQLiteStore) PatchUser(ctx context.Context, tenantID, id string, ops []PatchOperation) (User, error) {
	existing, err := s.GetUser(ctx, tenantID, id)
	if err != nil {
		return User{}, err
	}
	patched := existing
	if err := applyUserPatch(&patched, ops); err != nil {
		return User{}, err
	}
	if err := validateUserWrite(patched); err != nil {
		return User{}, err
	}
	now := nowUTC(s.now)
	created := parseStoredTime(existing.Meta.Created, now)
	stored := stampUser(patched, created, now)
	if err := s.updateUser(ctx, tenantID, stored); err != nil {
		return User{}, err
	}
	return stored, nil
}

func (s *SQLiteStore) updateUser(ctx context.Context, tenantID string, u User) error {
	emails, err := marshalJSON(u.Emails)
	if err != nil {
		return err
	}
	groups, err := marshalJSON(u.Groups)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE gateway_scim_users SET
	user_name = ?, external_id = nullif(?, ''), display_name = ?, active = ?,
	emails_json = ?, groups_json = ?, version = ?, updated_at = ?
WHERE tenant_id = ? AND id = ?`,
		u.UserName, u.ExternalID, u.DisplayName, boolToInt(u.Active),
		emails, groups, u.Meta.Version, u.Meta.LastModified, tenantID, u.ID)
	if err != nil {
		return mapUniqueErr(err, "user")
	}
	return nil
}

func (s *SQLiteStore) DeleteUser(ctx context.Context, tenantID, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM gateway_scim_users WHERE tenant_id = ? AND id = ?`, tenantID, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return errNotFound("user not found: " + id)
	}
	return nil
}

func (s *SQLiteStore) ListUsers(ctx context.Context, tenantID string, params ListParams) (ListResponse, error) {
	filter, err := parseFilter(params.Filter, userFilterAttrs)
	if err != nil {
		return ListResponse{}, err
	}
	startIndex, count := normalizeListParams(params)
	query := selectUserSQL + ` WHERE tenant_id = ?`
	args := []any{tenantID}
	if filter != nil {
		query += ` AND ` + userFilterColumn(filter.Attr) + ` = ?`
		args = append(args, filter.Value)
	}
	query += ` ORDER BY user_name ASC, id ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return ListResponse{}, err
	}
	defer rows.Close()
	matched := make([]User, 0)
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			return ListResponse{}, err
		}
		matched = append(matched, user)
	}
	if err := rows.Err(); err != nil {
		return ListResponse{}, err
	}
	window := paginate(matched, startIndex, count)
	return buildUserListResponse(window, len(matched), startIndex), nil
}

// --- Groups ---

func (s *SQLiteStore) CreateGroup(ctx context.Context, tenantID string, group Group) (Group, error) {
	if err := validateGroupWrite(group); err != nil {
		return Group{}, err
	}
	now := nowUTC(s.now)
	group.ID = newResourceID("grp")
	stored := stampGroup(group, now, now)
	if err := s.insertGroup(ctx, tenantID, stored); err != nil {
		return Group{}, err
	}
	return stored, nil
}

func (s *SQLiteStore) insertGroup(ctx context.Context, tenantID string, g Group) error {
	members, err := marshalJSON(g.Members)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO gateway_scim_groups (
	tenant_id, id, display_name, external_id, members_json, version, created_at, updated_at
) VALUES (?, ?, ?, nullif(?, ''), ?, ?, ?, ?)`,
		tenantID, g.ID, g.DisplayName, g.ExternalID, members, g.Meta.Version, g.Meta.Created, g.Meta.LastModified)
	if err != nil {
		return mapUniqueErr(err, "group")
	}
	return nil
}

func (s *SQLiteStore) GetGroup(ctx context.Context, tenantID, id string) (Group, error) {
	row := s.db.QueryRowContext(ctx, selectGroupSQL+` WHERE tenant_id = ? AND id = ?`, tenantID, id)
	group, err := scanGroup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, errNotFound("group not found: " + id)
	}
	if err != nil {
		return Group{}, err
	}
	return group, nil
}

func (s *SQLiteStore) ReplaceGroup(ctx context.Context, tenantID, id string, group Group) (Group, error) {
	if err := validateGroupWrite(group); err != nil {
		return Group{}, err
	}
	existing, err := s.GetGroup(ctx, tenantID, id)
	if err != nil {
		return Group{}, err
	}
	now := nowUTC(s.now)
	group.ID = id
	created := parseStoredTime(existing.Meta.Created, now)
	stored := stampGroup(group, created, now)
	if err := s.updateGroup(ctx, tenantID, stored); err != nil {
		return Group{}, err
	}
	return stored, nil
}

func (s *SQLiteStore) PatchGroup(ctx context.Context, tenantID, id string, ops []PatchOperation) (Group, error) {
	existing, err := s.GetGroup(ctx, tenantID, id)
	if err != nil {
		return Group{}, err
	}
	patched := existing
	if err := applyGroupPatch(&patched, ops); err != nil {
		return Group{}, err
	}
	if err := validateGroupWrite(patched); err != nil {
		return Group{}, err
	}
	now := nowUTC(s.now)
	created := parseStoredTime(existing.Meta.Created, now)
	stored := stampGroup(patched, created, now)
	if err := s.updateGroup(ctx, tenantID, stored); err != nil {
		return Group{}, err
	}
	return stored, nil
}

func (s *SQLiteStore) updateGroup(ctx context.Context, tenantID string, g Group) error {
	members, err := marshalJSON(g.Members)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE gateway_scim_groups SET
	display_name = ?, external_id = nullif(?, ''), members_json = ?, version = ?, updated_at = ?
WHERE tenant_id = ? AND id = ?`,
		g.DisplayName, g.ExternalID, members, g.Meta.Version, g.Meta.LastModified, tenantID, g.ID)
	if err != nil {
		return mapUniqueErr(err, "group")
	}
	return nil
}

func (s *SQLiteStore) DeleteGroup(ctx context.Context, tenantID, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM gateway_scim_groups WHERE tenant_id = ? AND id = ?`, tenantID, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return errNotFound("group not found: " + id)
	}
	return nil
}

func (s *SQLiteStore) ListGroups(ctx context.Context, tenantID string, params ListParams) (ListResponse, error) {
	filter, err := parseFilter(params.Filter, groupFilterAttrs)
	if err != nil {
		return ListResponse{}, err
	}
	startIndex, count := normalizeListParams(params)
	query := selectGroupSQL + ` WHERE tenant_id = ?`
	args := []any{tenantID}
	if filter != nil {
		query += ` AND ` + groupFilterColumn(filter.Attr) + ` = ?`
		args = append(args, filter.Value)
	}
	query += ` ORDER BY display_name ASC, id ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return ListResponse{}, err
	}
	defer rows.Close()
	matched := make([]Group, 0)
	for rows.Next() {
		group, err := scanGroup(rows)
		if err != nil {
			return ListResponse{}, err
		}
		matched = append(matched, group)
	}
	if err := rows.Err(); err != nil {
		return ListResponse{}, err
	}
	window := paginate(matched, startIndex, count)
	return buildGroupListResponse(window, len(matched), startIndex), nil
}

// --- scanning helpers ---

const selectUserSQL = `SELECT id, user_name, external_id, display_name, active, emails_json, groups_json, version, created_at, updated_at FROM gateway_scim_users`

const selectGroupSQL = `SELECT id, display_name, external_id, members_json, version, created_at, updated_at FROM gateway_scim_groups`

// rowScanner abstracts *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (User, error) {
	var (
		u          User
		externalID sql.NullString
		active     int
		emails     string
		groups     string
		created    string
		updated    string
	)
	if err := row.Scan(&u.ID, &u.UserName, &externalID, &u.DisplayName, &active, &emails, &groups, &u.Meta.Version, &created, &updated); err != nil {
		return User{}, err
	}
	u.ExternalID = externalID.String
	u.Active = active != 0
	if err := json.Unmarshal([]byte(emails), &u.Emails); err != nil {
		return User{}, fmt.Errorf("scim: decode emails: %w", err)
	}
	if err := json.Unmarshal([]byte(groups), &u.Groups); err != nil {
		return User{}, fmt.Errorf("scim: decode groups: %w", err)
	}
	u.Schemas = []string{SchemaUser}
	u.Meta.ResourceType = ResourceTypeUser
	u.Meta.Created = created
	u.Meta.LastModified = updated
	u.Meta.Location = usersLocationBase + u.ID
	return u, nil
}

func scanGroup(row rowScanner) (Group, error) {
	var (
		g          Group
		externalID sql.NullString
		members    string
		created    string
		updated    string
	)
	if err := row.Scan(&g.ID, &g.DisplayName, &externalID, &members, &g.Meta.Version, &created, &updated); err != nil {
		return Group{}, err
	}
	g.ExternalID = externalID.String
	if err := json.Unmarshal([]byte(members), &g.Members); err != nil {
		return Group{}, fmt.Errorf("scim: decode members: %w", err)
	}
	g.Schemas = []string{SchemaGroup}
	g.Meta.ResourceType = ResourceTypeGroup
	g.Meta.Created = created
	g.Meta.LastModified = updated
	g.Meta.Location = groupsLocationBase + g.ID
	return g, nil
}

func userFilterColumn(attr string) string {
	switch attr {
	case "username":
		return "user_name"
	case "externalid":
		return "external_id"
	case "displayname":
		return "display_name"
	default:
		return "user_name"
	}
}

func groupFilterColumn(attr string) string {
	switch attr {
	case "externalid":
		return "external_id"
	default:
		return "display_name"
	}
}

func marshalJSON(value any) (string, error) {
	switch v := value.(type) {
	case []Email:
		if v == nil {
			return "[]", nil
		}
	case []GroupRef:
		if v == nil {
			return "[]", nil
		}
	case []MemberRef:
		if v == nil {
			return "[]", nil
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("scim: encode json column: %w", err)
	}
	return string(raw), nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// mapUniqueErr converts a SQLite UNIQUE constraint violation into a SCIM 409.
func mapUniqueErr(err error, resource string) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "unique") || strings.Contains(msg, "constraint failed") {
		return errConflict(resource + " uniqueness constraint violated")
	}
	return err
}
