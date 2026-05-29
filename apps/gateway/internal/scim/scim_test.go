package scim

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const testTenant = "tenant-a"

// storeFactory builds a fresh Store for a subtest.
type storeFactory struct {
	name  string
	build func(t *testing.T) Store
}

func allStoreFactories(t *testing.T) []storeFactory {
	t.Helper()
	return []storeFactory{
		{
			name:  "memory",
			build: func(t *testing.T) Store { return NewMemoryStore() },
		},
		{
			name:  "sqlite",
			build: func(t *testing.T) Store { return newSQLiteTestStore(t) },
		},
	}
}

func newSQLiteTestStore(t *testing.T) Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "scim.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatalf("new sqlite store: %v", err)
	}
	if err := store.Ready(context.Background()); err != nil {
		t.Fatalf("sqlite ready: %v", err)
	}
	return store
}

func sampleUser() User {
	return User{
		Schemas:     []string{SchemaUser},
		UserName:    "alice@example.com",
		ExternalID:  "ext-alice",
		DisplayName: "Alice Example",
		Active:      true,
		Emails:      []Email{{Value: "alice@example.com", Type: "work", Primary: true}},
		Password:    "super-secret-should-be-dropped",
	}
}

func TestCreateUserDropsPassword(t *testing.T) {
	ctx := context.Background()
	for _, factory := range allStoreFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.build(t)
			created, err := store.CreateUser(ctx, testTenant, sampleUser())
			if err != nil {
				t.Fatalf("create user: %v", err)
			}
			if created.ID == "" {
				t.Fatal("expected generated id")
			}
			if created.Password != "" {
				t.Fatalf("password must be dropped, got %q", created.Password)
			}
			if created.Meta.ResourceType != ResourceTypeUser {
				t.Fatalf("meta resourceType = %q", created.Meta.ResourceType)
			}
			if created.Meta.Created == "" || created.Meta.LastModified == "" || created.Meta.Version == "" {
				t.Fatalf("meta incomplete: %+v", created.Meta)
			}
			if created.Meta.Location != usersLocationBase+created.ID {
				t.Fatalf("meta location = %q", created.Meta.Location)
			}

			got, err := store.GetUser(ctx, testTenant, created.ID)
			if err != nil {
				t.Fatalf("get user: %v", err)
			}
			if got.Password != "" {
				t.Fatalf("persisted password leaked: %q", got.Password)
			}
			if got.UserName != "alice@example.com" {
				t.Fatalf("userName = %q", got.UserName)
			}

			// For the SQLite store, assert the raw row never stored the secret.
			if sqlite, ok := store.(*SQLiteStore); ok {
				var blob string
				row := sqlite.db.QueryRowContext(ctx,
					`SELECT user_name || '|' || COALESCE(external_id,'') || '|' || display_name || '|' || emails_json FROM gateway_scim_users WHERE id = ?`,
					created.ID)
				if err := row.Scan(&blob); err != nil {
					t.Fatalf("scan raw row: %v", err)
				}
				if strings.Contains(blob, "super-secret") {
					t.Fatalf("password persisted in sqlite row: %q", blob)
				}
			}
		})
	}
}

func TestReplaceAndGetUser(t *testing.T) {
	ctx := context.Background()
	for _, factory := range allStoreFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.build(t)
			created, err := store.CreateUser(ctx, testTenant, sampleUser())
			if err != nil {
				t.Fatalf("create: %v", err)
			}

			replacement := sampleUser()
			replacement.DisplayName = "Alice Renamed"
			replacement.Password = "another-secret"
			replaced, err := store.ReplaceUser(ctx, testTenant, created.ID, replacement)
			if err != nil {
				t.Fatalf("replace: %v", err)
			}
			if replaced.DisplayName != "Alice Renamed" {
				t.Fatalf("displayName = %q", replaced.DisplayName)
			}
			if replaced.Meta.Created != created.Meta.Created {
				t.Fatalf("created changed on replace: %q -> %q", created.Meta.Created, replaced.Meta.Created)
			}
			if replaced.Password != "" {
				t.Fatal("replace leaked password")
			}
			if replaced.Meta.Version == created.Meta.Version {
				t.Fatal("expected version to change after content change")
			}

			_, err = store.ReplaceUser(ctx, testTenant, "missing", sampleUser())
			if scimErr, ok := AsError(err); !ok || scimErr.StatusCode() != 404 {
				t.Fatalf("expected 404 replacing missing user, got %v", err)
			}
		})
	}
}

func TestPatchUserActiveFalse(t *testing.T) {
	ctx := context.Background()
	for _, factory := range allStoreFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.build(t)
			created, err := store.CreateUser(ctx, testTenant, sampleUser())
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if !created.Active {
				t.Fatal("expected created user active")
			}
			patched, err := store.PatchUser(ctx, testTenant, created.ID, []PatchOperation{
				{Op: "replace", Path: "active", Value: []byte("false")},
			})
			if err != nil {
				t.Fatalf("patch: %v", err)
			}
			if patched.Active {
				t.Fatal("expected active=false after patch")
			}

			patched2, err := store.PatchUser(ctx, testTenant, created.ID, []PatchOperation{
				{Op: "replace", Path: "displayName", Value: []byte(`"Patched Name"`)},
			})
			if err != nil {
				t.Fatalf("patch displayName: %v", err)
			}
			if patched2.DisplayName != "Patched Name" {
				t.Fatalf("displayName = %q", patched2.DisplayName)
			}

			_, err = store.PatchUser(ctx, testTenant, created.ID, []PatchOperation{
				{Op: "replace", Path: "unsupported", Value: []byte(`"x"`)},
			})
			if scimErr, ok := AsError(err); !ok || scimErr.StatusCode() != 400 {
				t.Fatalf("expected 400 for unsupported path, got %v", err)
			}
		})
	}
}

func TestDeleteUser(t *testing.T) {
	ctx := context.Background()
	for _, factory := range allStoreFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.build(t)
			created, err := store.CreateUser(ctx, testTenant, sampleUser())
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if err := store.DeleteUser(ctx, testTenant, created.ID); err != nil {
				t.Fatalf("delete: %v", err)
			}
			_, err = store.GetUser(ctx, testTenant, created.ID)
			if scimErr, ok := AsError(err); !ok || scimErr.StatusCode() != 404 {
				t.Fatalf("expected 404 after delete, got %v", err)
			}
			if err := store.DeleteUser(ctx, testTenant, created.ID); err == nil {
				t.Fatal("expected error deleting already-deleted user")
			}
		})
	}
}

func TestListUsersFilterAndPagination(t *testing.T) {
	ctx := context.Background()
	for _, factory := range allStoreFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.build(t)
			for _, name := range []string{"u1", "u2", "u3", "u4", "u5"} {
				u := sampleUser()
				u.UserName = name
				u.ExternalID = "ext-" + name
				if _, err := store.CreateUser(ctx, testTenant, u); err != nil {
					t.Fatalf("create %s: %v", name, err)
				}
			}

			// Filter: userName eq "u3"
			filtered, err := store.ListUsers(ctx, testTenant, ListParams{Filter: `userName eq "u3"`})
			if err != nil {
				t.Fatalf("list filtered: %v", err)
			}
			if filtered.TotalResults != 1 || len(filtered.Resources) != 1 {
				t.Fatalf("expected 1 result, got total=%d len=%d", filtered.TotalResults, len(filtered.Resources))
			}
			only := filtered.Resources[0].(User)
			if only.UserName != "u3" {
				t.Fatalf("filtered userName = %q", only.UserName)
			}
			if filtered.Schemas[0] != SchemaListResponse {
				t.Fatalf("list schema = %q", filtered.Schemas[0])
			}

			// Pagination: startIndex 2, count 2 over the 5 sorted users.
			page, err := store.ListUsers(ctx, testTenant, ListParams{StartIndex: 2, Count: 2})
			if err != nil {
				t.Fatalf("list paginated: %v", err)
			}
			if page.TotalResults != 5 {
				t.Fatalf("totalResults = %d", page.TotalResults)
			}
			if page.StartIndex != 2 || page.ItemsPerPage != 2 {
				t.Fatalf("pagination meta start=%d perPage=%d", page.StartIndex, page.ItemsPerPage)
			}
			first := page.Resources[0].(User)
			second := page.Resources[1].(User)
			if first.UserName != "u2" || second.UserName != "u3" {
				t.Fatalf("unexpected page contents: %q,%q", first.UserName, second.UserName)
			}

			// Unsupported filter returns a 400-style SCIM error.
			_, err = store.ListUsers(ctx, testTenant, ListParams{Filter: `userName co "u"`})
			if scimErr, ok := AsError(err); !ok || scimErr.StatusCode() != 400 {
				t.Fatalf("expected 400 for unsupported filter, got %v", err)
			}
		})
	}
}

func TestExternalIDUniquenessPerTenant(t *testing.T) {
	ctx := context.Background()
	for _, factory := range allStoreFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.build(t)
			first := sampleUser()
			first.UserName = "first"
			first.ExternalID = "dup"
			if _, err := store.CreateUser(ctx, testTenant, first); err != nil {
				t.Fatalf("create first: %v", err)
			}
			second := sampleUser()
			second.UserName = "second"
			second.ExternalID = "dup"
			_, err := store.CreateUser(ctx, testTenant, second)
			if scimErr, ok := AsError(err); !ok || scimErr.StatusCode() != 409 {
				t.Fatalf("expected 409 externalId conflict, got %v", err)
			}

			// Same externalId is allowed in a different tenant.
			if _, err := store.CreateUser(ctx, "tenant-b", second); err != nil {
				t.Fatalf("create in other tenant: %v", err)
			}
		})
	}
}

func TestUserNameUniquenessPerTenant(t *testing.T) {
	ctx := context.Background()
	for _, factory := range allStoreFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.build(t)
			u := sampleUser()
			u.ExternalID = ""
			if _, err := store.CreateUser(ctx, testTenant, u); err != nil {
				t.Fatalf("create: %v", err)
			}
			dup := sampleUser()
			dup.ExternalID = ""
			_, err := store.CreateUser(ctx, testTenant, dup)
			if scimErr, ok := AsError(err); !ok || scimErr.StatusCode() != 409 {
				t.Fatalf("expected 409 userName conflict, got %v", err)
			}
		})
	}
}

func TestTenantScopeIsolation(t *testing.T) {
	ctx := context.Background()
	for _, factory := range allStoreFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.build(t)
			created, err := store.CreateUser(ctx, testTenant, sampleUser())
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			// Other tenant cannot see or fetch the user.
			_, err = store.GetUser(ctx, "tenant-other", created.ID)
			if scimErr, ok := AsError(err); !ok || scimErr.StatusCode() != 404 {
				t.Fatalf("expected 404 cross-tenant get, got %v", err)
			}
			list, err := store.ListUsers(ctx, "tenant-other", ListParams{})
			if err != nil {
				t.Fatalf("list other tenant: %v", err)
			}
			if list.TotalResults != 0 {
				t.Fatalf("expected empty list for other tenant, got %d", list.TotalResults)
			}
		})
	}
}

func TestGroupLifecycleAndMembership(t *testing.T) {
	ctx := context.Background()
	for _, factory := range allStoreFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			store := factory.build(t)
			user, err := store.CreateUser(ctx, testTenant, sampleUser())
			if err != nil {
				t.Fatalf("create user: %v", err)
			}

			group, err := store.CreateGroup(ctx, testTenant, Group{
				Schemas:     []string{SchemaGroup},
				DisplayName: "Engineering",
				ExternalID:  "grp-eng",
			})
			if err != nil {
				t.Fatalf("create group: %v", err)
			}
			if group.ID == "" || group.Meta.ResourceType != ResourceTypeGroup {
				t.Fatalf("unexpected group meta: %+v", group)
			}

			// Add a member via PATCH.
			patched, err := store.PatchGroup(ctx, testTenant, group.ID, []PatchOperation{
				{Op: "add", Path: "members", Value: []byte(`[{"value":"` + user.ID + `","display":"Alice"}]`)},
			})
			if err != nil {
				t.Fatalf("patch group members: %v", err)
			}
			if len(patched.Members) != 1 || patched.Members[0].Value != user.ID {
				t.Fatalf("unexpected members: %+v", patched.Members)
			}

			// List groups filtered by displayName.
			list, err := store.ListGroups(ctx, testTenant, ListParams{Filter: `displayName eq "Engineering"`})
			if err != nil {
				t.Fatalf("list groups: %v", err)
			}
			if list.TotalResults != 1 {
				t.Fatalf("expected 1 group, got %d", list.TotalResults)
			}

			// Remove members via PATCH.
			cleared, err := store.PatchGroup(ctx, testTenant, group.ID, []PatchOperation{
				{Op: "remove", Path: "members"},
			})
			if err != nil {
				t.Fatalf("patch remove members: %v", err)
			}
			if len(cleared.Members) != 0 {
				t.Fatalf("expected no members, got %+v", cleared.Members)
			}

			// Replace then delete.
			replaced, err := store.ReplaceGroup(ctx, testTenant, group.ID, Group{DisplayName: "Platform", ExternalID: "grp-eng"})
			if err != nil {
				t.Fatalf("replace group: %v", err)
			}
			if replaced.DisplayName != "Platform" {
				t.Fatalf("displayName = %q", replaced.DisplayName)
			}
			if err := store.DeleteGroup(ctx, testTenant, group.ID); err != nil {
				t.Fatalf("delete group: %v", err)
			}
			_, err = store.GetGroup(ctx, testTenant, group.ID)
			if scimErr, ok := AsError(err); !ok || scimErr.StatusCode() != 404 {
				t.Fatalf("expected 404 after group delete, got %v", err)
			}
		})
	}
}

func TestParseFilterErrors(t *testing.T) {
	cases := []string{
		`userName co "x"`,
		`bogus eq "x"`,
		`userName eq x`,
		`userName eq`,
	}
	for _, c := range cases {
		if _, err := parseFilter(c, userFilterAttrs); err == nil {
			t.Fatalf("expected error for filter %q", c)
		}
	}
	if f, err := parseFilter(`externalId eq "ext-1"`, userFilterAttrs); err != nil || f == nil || f.Value != "ext-1" {
		t.Fatalf("valid filter failed: f=%+v err=%v", f, err)
	}
	if f, err := parseFilter("", userFilterAttrs); err != nil || f != nil {
		t.Fatalf("empty filter should be nil/no error, got f=%+v err=%v", f, err)
	}
}
