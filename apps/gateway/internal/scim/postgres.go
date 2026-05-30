package scim

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

var _ Store = (*PostgresStore)(nil)

// PostgresStore is a tenant-scoped SCIM Store backed by Postgres
// (github.com/jackc/pgx/v5/stdlib, driver name "pgx"). It mirrors the SQLite
// store: emails, group references and member references are stored as JSON text
// columns and meta.version (etag) is recomputed from resource content on every
// write. The password attribute is never persisted. The schema is
// migration-driven (migrations/postgres/0005_enterprise_stores.sql).
type PostgresStore struct {
	db  *sql.DB
	now func() time.Time
}

// NewPostgresStore constructs a PostgresStore over db. The schema is created by
// migrations, not by this constructor; call Ready to verify it exists.
func NewPostgresStore(db *sql.DB) (*PostgresStore, error) {
	if db == nil {
		return nil, fmt.Errorf("scim: postgres store requires a non-nil *sql.DB")
	}
	return &PostgresStore{db: db, now: func() time.Time { return time.Now() }}, nil
}

func (s *PostgresStore) Ready(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("scim: postgres store is not configured")
	}
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	for _, objectName := range []string{"gateway_scim_users", "gateway_scim_groups"} {
		if err := requirePostgresObject(ctx, s.db, objectName); err != nil {
			return err
		}
	}
	return nil
}

// --- Users ---

func (s *PostgresStore) CreateUser(ctx context.Context, tenantID string, user User) (User, error) {
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

func (s *PostgresStore) insertUser(ctx context.Context, tenantID string, u User) error {
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
) VALUES ($1, $2, $3, nullif($4, ''), $5, $6, $7, $8, $9, $10, $11)`,
		tenantID, u.ID, u.UserName, u.ExternalID, u.DisplayName, boolToInt(u.Active),
		emails, groups, u.Meta.Version, u.Meta.Created, u.Meta.LastModified)
	if err != nil {
		return mapUniqueErr(err, "user")
	}
	return nil
}

func (s *PostgresStore) GetUser(ctx context.Context, tenantID, id string) (User, error) {
	row := s.db.QueryRowContext(ctx, selectUserSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	user, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, errNotFound("user not found: " + id)
	}
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *PostgresStore) ReplaceUser(ctx context.Context, tenantID, id string, user User) (User, error) {
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

func (s *PostgresStore) PatchUser(ctx context.Context, tenantID, id string, ops []PatchOperation) (User, error) {
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

func (s *PostgresStore) updateUser(ctx context.Context, tenantID string, u User) error {
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
	user_name = $1, external_id = nullif($2, ''), display_name = $3, active = $4,
	emails_json = $5, groups_json = $6, version = $7, updated_at = $8
WHERE tenant_id = $9 AND id = $10`,
		u.UserName, u.ExternalID, u.DisplayName, boolToInt(u.Active),
		emails, groups, u.Meta.Version, u.Meta.LastModified, tenantID, u.ID)
	if err != nil {
		return mapUniqueErr(err, "user")
	}
	return nil
}

func (s *PostgresStore) DeleteUser(ctx context.Context, tenantID, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM gateway_scim_users WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return errNotFound("user not found: " + id)
	}
	return nil
}

func (s *PostgresStore) ListUsers(ctx context.Context, tenantID string, params ListParams) (ListResponse, error) {
	filter, err := parseFilter(params.Filter, userFilterAttrs)
	if err != nil {
		return ListResponse{}, err
	}
	startIndex, count := normalizeListParams(params)
	query := selectUserSQL + ` WHERE tenant_id = $1`
	args := []any{tenantID}
	if filter != nil {
		query += ` AND ` + userFilterColumn(filter.Attr) + ` = $2`
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

func (s *PostgresStore) CreateGroup(ctx context.Context, tenantID string, group Group) (Group, error) {
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

func (s *PostgresStore) insertGroup(ctx context.Context, tenantID string, g Group) error {
	members, err := marshalJSON(g.Members)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO gateway_scim_groups (
	tenant_id, id, display_name, external_id, members_json, version, created_at, updated_at
) VALUES ($1, $2, $3, nullif($4, ''), $5, $6, $7, $8)`,
		tenantID, g.ID, g.DisplayName, g.ExternalID, members, g.Meta.Version, g.Meta.Created, g.Meta.LastModified)
	if err != nil {
		return mapUniqueErr(err, "group")
	}
	return nil
}

func (s *PostgresStore) GetGroup(ctx context.Context, tenantID, id string) (Group, error) {
	row := s.db.QueryRowContext(ctx, selectGroupSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	group, err := scanGroup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Group{}, errNotFound("group not found: " + id)
	}
	if err != nil {
		return Group{}, err
	}
	return group, nil
}

func (s *PostgresStore) ReplaceGroup(ctx context.Context, tenantID, id string, group Group) (Group, error) {
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

func (s *PostgresStore) PatchGroup(ctx context.Context, tenantID, id string, ops []PatchOperation) (Group, error) {
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

func (s *PostgresStore) updateGroup(ctx context.Context, tenantID string, g Group) error {
	members, err := marshalJSON(g.Members)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE gateway_scim_groups SET
	display_name = $1, external_id = nullif($2, ''), members_json = $3, version = $4, updated_at = $5
WHERE tenant_id = $6 AND id = $7`,
		g.DisplayName, g.ExternalID, members, g.Meta.Version, g.Meta.LastModified, tenantID, g.ID)
	if err != nil {
		return mapUniqueErr(err, "group")
	}
	return nil
}

func (s *PostgresStore) DeleteGroup(ctx context.Context, tenantID, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM gateway_scim_groups WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return errNotFound("group not found: " + id)
	}
	return nil
}

func (s *PostgresStore) ListGroups(ctx context.Context, tenantID string, params ListParams) (ListResponse, error) {
	filter, err := parseFilter(params.Filter, groupFilterAttrs)
	if err != nil {
		return ListResponse{}, err
	}
	startIndex, count := normalizeListParams(params)
	query := selectGroupSQL + ` WHERE tenant_id = $1`
	args := []any{tenantID}
	if filter != nil {
		query += ` AND ` + groupFilterColumn(filter.Attr) + ` = $2`
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

func requirePostgresObject(ctx context.Context, db *sql.DB, objectName string) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, objectName).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s is missing", objectName)
	}
	return nil
}
