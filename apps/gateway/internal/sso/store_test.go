package sso

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func runConfigStoreSuite(t *testing.T, store ConfigStore) {
	t.Helper()
	ctx := context.Background()
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}

	oidc := OIDCConfig{
		Issuer:           "https://idp.example/",
		ClientID:         "ubag-gateway",
		ClientSecretRef:  "secretref://vault/oidc/ubag",
		AllowedAudiences: []string{"ubag-gateway"},
		AttributeMapping: AttributeMapping{TenantAttribute: "tenant", AppAttribute: "app"},
	}
	if err := store.SetOIDC(ctx, "tenant-a", oidc); err != nil {
		t.Fatalf("set oidc: %v", err)
	}
	got, found, err := store.GetOIDC(ctx, "tenant-a")
	if err != nil || !found {
		t.Fatalf("get oidc: found=%v err=%v", found, err)
	}
	if got.ClientSecretRef != oidc.ClientSecretRef || got.Issuer != oidc.Issuer {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// Scope isolation: tenant-b must not see tenant-a's config.
	if _, found, err := store.GetOIDC(ctx, "tenant-b"); err != nil || found {
		t.Fatalf("expected no oidc for tenant-b, found=%v err=%v", found, err)
	}

	saml := SAMLConfig{
		EntityID:   "https://sp.ubag.example/",
		IdPSSOURL:  "https://idp.example/sso",
		IdPCertPEM: "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----",
	}
	if err := store.SetSAML(ctx, "tenant-a", saml); err != nil {
		t.Fatalf("set saml: %v", err)
	}
	if _, found, err := store.GetSAML(ctx, "tenant-b"); err != nil || found {
		t.Fatalf("expected no saml for tenant-b, found=%v err=%v", found, err)
	}

	// Update existing tenant config (upsert).
	oidc.ClientID = "ubag-gateway-v2"
	if err := store.SetOIDC(ctx, "tenant-a", oidc); err != nil {
		t.Fatalf("update oidc: %v", err)
	}
	got, _, _ = store.GetOIDC(ctx, "tenant-a")
	if got.ClientID != "ubag-gateway-v2" {
		t.Errorf("expected updated client id, got %q", got.ClientID)
	}

	if err := store.SetOIDC(ctx, "tenant-b", oidc); err != nil {
		t.Fatalf("set oidc tenant-b: %v", err)
	}
	listed, err := store.ListOIDC(ctx)
	if err != nil {
		t.Fatalf("list oidc: %v", err)
	}
	if len(listed) != 2 {
		t.Errorf("expected 2 oidc records, got %d", len(listed))
	}

	samlList, err := store.ListSAML(ctx)
	if err != nil {
		t.Fatalf("list saml: %v", err)
	}
	if len(samlList) != 1 {
		t.Errorf("expected 1 saml record, got %d", len(samlList))
	}
}

func TestMemoryStore(t *testing.T) {
	runConfigStoreSuite(t, NewMemoryStore())
}

func TestSQLiteStore(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "sso.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	runConfigStoreSuite(t, NewSQLiteStore(db))
}

// TestSQLiteStore_NoPlaintextSecretPersisted asserts the persisted row only
// carries the client secret *reference id*, never a plaintext secret.
func TestSQLiteStore_NoPlaintextSecretPersisted(t *testing.T) {
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "sso.db")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	store := NewSQLiteStore(db)
	const ref = "secretref://vault/oidc/tenant-a"
	cfg := OIDCConfig{
		Issuer:          "https://idp.example/",
		ClientID:        "ubag-gateway",
		ClientSecretRef: ref,
	}
	if err := store.SetOIDC(ctx, "tenant-a", cfg); err != nil {
		t.Fatalf("set oidc: %v", err)
	}

	var clientSecretRef, configJSON string
	row := db.QueryRowContext(ctx, `SELECT client_secret_ref, config_json FROM gateway_sso_oidc_config WHERE tenant_id = ?`, "tenant-a")
	if err := row.Scan(&clientSecretRef, &configJSON); err != nil {
		t.Fatalf("scan row: %v", err)
	}
	if clientSecretRef != ref {
		t.Errorf("client_secret_ref = %q, want %q", clientSecretRef, ref)
	}
	// The persisted JSON must contain the reference and must not contain any
	// plaintext-secret field name.
	if !strings.Contains(configJSON, ref) {
		t.Errorf("config_json missing reference id: %s", configJSON)
	}
	for _, forbidden := range []string{"ClientSecret\"", "client_secret\"", "private_key", "PrivateKey", "password"} {
		if strings.Contains(configJSON, forbidden) {
			t.Errorf("config_json contains forbidden secret field %q: %s", forbidden, configJSON)
		}
	}
}
