package antigravity

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewStoreDefaults(t *testing.T) {
	store := NewStore(t.TempDir())
	config := store.GetConfig()
	if config.DefaultModel != "gemini-3.8-flash" {
		t.Errorf("default model = %q, want gemini-3.8-flash", config.DefaultModel)
	}
	if config.DefaultEffort != "high" {
		t.Errorf("default effort = %q, want high", config.DefaultEffort)
	}
}

func TestOAuthAccountSlotsAreTenantScopedAndDoNotRequireSecrets(t *testing.T) {
	t.Setenv("UBAG_ANTIGRAVITY_ACCOUNT_COUNT", "0")
	dataDir := t.TempDir()
	store := NewStore(dataDir)
	account, err := store.AddAccount("tenant_a", "Primary", "pro")
	if err != nil {
		t.Fatalf("add OAuth account: %v", err)
	}
	if account.Label != "Primary" || account.TenantID != "tenant_a" {
		t.Fatalf("unexpected OAuth account metadata: %#v", account)
	}
	if got := store.ListAccounts("tenant_b"); len(got) != 0 {
		t.Fatalf("another tenant can see OAuth accounts: %#v", got)
	}
	accounts := NewStore(dataDir).ListAccounts("tenant_a")
	if len(accounts) != 1 || accounts[0].ID != account.ID {
		t.Fatalf("OAuth account metadata did not persist: %#v", accounts)
	}
}

func TestOAuthSlotsIgnoreAPIKeyEnvironmentAndLimitEachTenant(t *testing.T) {
	t.Setenv("UBAG_ANTIGRAVITY_ACCOUNT_1_API_KEY", "legacy-key")
	store := NewStore(t.TempDir())
	if count := store.GetConfig().AccountCount; count != 0 {
		t.Fatalf("legacy API key loaded as account metadata: count=%d", count)
	}
	if got := store.ListAccounts("tenant_a"); len(got) != 0 {
		t.Fatalf("legacy API key registered an OAuth account: %#v", got)
	}
	for number := 1; number <= 3; number++ {
		if _, err := store.AddAccount("tenant_a", fmt.Sprintf("Pro %d", number), "pro"); err != nil {
			t.Fatalf("add OAuth slot %d: %v", number, err)
		}
	}
	if _, err := store.AddAccount("tenant_a", "Fourth", "pro"); err == nil {
		t.Fatal("accepted a fourth OAuth slot for the same tenant")
	}
	if _, err := store.AddAccount("tenant_b", "Own slot", "pro"); err != nil {
		t.Fatalf("another tenant should have its own three slots: %v", err)
	}
}

func TestOAuthAccountStoreUsesConfiguredDurableDirectory(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("UBAG_ANTIGRAVITY_DATA_DIR", dataDir)
	store := NewStore("")
	if store.dataDir != dataDir {
		t.Fatalf("account metadata directory = %q, want %q", store.dataDir, dataDir)
	}
	if _, err := store.AddAccount("tenant_a", "Primary", "pro"); err != nil {
		t.Fatalf("save OAuth metadata: %v", err)
	}
	if got := NewStore("").ListAccounts("tenant_a"); len(got) != 1 {
		t.Fatalf("durable OAuth metadata did not reload: %#v", got)
	}
}

func TestOAuthAccountsRotateAndCoolDownWithoutQuotaPercentages(t *testing.T) {
	dataDir := t.TempDir()
	store := NewStore(dataDir)
	first, err := store.AddAccount("tenant_a", "First", "pro")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddAccount("tenant_a", "Second", "pro")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddAccount("tenant_b", "Other tenant", "pro"); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccountUsed("tenant_a", first.ID); err != nil {
		t.Fatal(err)
	}
	if accounts := store.EligibleAccounts("tenant_a", time.Now()); len(accounts) != 2 || accounts[0].ID != second.ID {
		t.Fatalf("account selection did not rotate after use: %#v", accounts)
	}
	if err := store.MarkAccountExhausted("tenant_a", second.ID, 300); err != nil {
		t.Fatal(err)
	}
	if accounts := NewStore(dataDir).EligibleAccounts("tenant_a", time.Now()); len(accounts) != 1 || accounts[0].ID != first.ID {
		t.Fatalf("cooldown did not persist or leaked another tenant: %#v", accounts)
	}
	if accounts := store.EligibleAccounts("tenant_a", time.Now().Add(301*time.Second)); len(accounts) != 2 {
		t.Fatalf("cooldown never expired: %#v", accounts)
	}
}

func TestAccountSocketPathSeparatesSlots(t *testing.T) {
	got := AccountSocketPath("/run/ubag-antigravity", "acct_2")
	want := filepath.Join("/run/ubag-antigravity", "acct_2", "acct_2.sock")
	if got != want {
		t.Fatalf("account socket path = %q; want %q", got, want)
	}
}

func TestRemovedAccountIDIsNeverReassignedToAnotherTenant(t *testing.T) {
	dataDir := t.TempDir()
	store := NewStore(dataDir)
	oldAccount, err := store.AddAccount("tenant_a", "Original", "pro")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveAccount("tenant_a", oldAccount.ID); err != nil {
		t.Fatal(err)
	}
	reopened := NewStore(dataDir)
	newAccount, err := reopened.AddAccount("tenant_b", "Replacement", "pro")
	if err != nil {
		t.Fatal(err)
	}
	if newAccount.ID == oldAccount.ID {
		t.Fatalf("reassigned %s to another tenant while its CLI worker may still be signed in", oldAccount.ID)
	}
}

func TestLegacyAccountArrayPreservesNextAvailableID(t *testing.T) {
	dataDir := t.TempDir()
	legacy := `[{"account_id":"acct_3","tenant_id":"tenant_a","label":"Existing","enabled":true,"tier":"pro"}]`
	if err := os.WriteFile(filepath.Join(dataDir, "accounts.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(dataDir)
	created, err := store.AddAccount("tenant_b", "New", "pro")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "acct_4" {
		t.Fatalf("legacy account ID progression = %s; want acct_4", created.ID)
	}
	if accounts := NewStore(dataDir).ListAccounts("tenant_a"); len(accounts) != 1 || accounts[0].ID != "acct_3" {
		t.Fatalf("legacy account lost after migration: %#v", accounts)
	}
}

func TestFailedAccountWriteDoesNotPublishSlot(t *testing.T) {
	store := NewStore(t.TempDir())
	dataDir := store.dataDir
	store.dataDir = filepath.Join(t.TempDir(), "missing")
	if _, err := store.AddAccount("tenant_a", "Not persisted", "pro"); err == nil {
		t.Fatal("expected metadata write to fail")
	}
	if accounts := store.ListAccounts("tenant_a"); len(accounts) != 0 {
		t.Fatalf("failed account write published a slot: %#v", accounts)
	}
	store.dataDir = dataDir
	account, err := store.AddAccount("tenant_a", "Persisted", "pro")
	if err != nil || account.ID != "acct_1" {
		t.Fatalf("failed write consumed slot id: account=%#v error=%v", account, err)
	}
}

func TestVerificationJobIsTenantScopedDurableAndAtomic(t *testing.T) {
	dataDir := t.TempDir()
	store := NewStore(dataDir)
	account, err := store.AddAccount("tenant_a", "Primary", "pro")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetVerificationJob("tenant_b", account.ID, "job_foreign"); err == nil {
		t.Fatal("other tenant replaced account verification")
	}
	if err := store.SetVerificationJob("tenant_a", account.ID, "job_first"); err != nil {
		t.Fatal(err)
	}
	if got := NewStore(dataDir).ListAccounts("tenant_a"); len(got) != 1 || got[0].VerificationJobID != "job_first" {
		t.Fatalf("verification job did not persist: %#v", got)
	}
	store.dataDir = filepath.Join(t.TempDir(), "missing")
	if err := store.SetVerificationJob("tenant_a", account.ID, "job_unwritten"); err == nil {
		t.Fatal("expected verification write to fail")
	}
	if got := store.ListAccounts("tenant_a"); len(got) != 1 || got[0].VerificationJobID != "job_first" {
		t.Fatalf("failed write changed in-memory verification: %#v", got)
	}
	store.dataDir = dataDir
	if err := store.SetVerificationJob("tenant_a", account.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got := NewStore(dataDir).ListAccounts("tenant_a"); len(got) != 1 || got[0].VerificationJobID != "" {
		t.Fatalf("new sign-in did not clear durable verification: %#v", got)
	}
}
