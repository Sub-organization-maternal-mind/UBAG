package helperauth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/conversations"
)

var (
	// Same pattern as executor.helperProfileRefPattern: no path characters.
	opaqueRefRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	t0          = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
)

func allNodes(string) bool { return true }

func only(ids ...string) func(string) bool {
	return func(id string) bool {
		for _, want := range ids {
			if id == want {
				return true
			}
		}
		return false
	}
}

func TestMintProfileRefIsOpaqueAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		ref := MintProfileRef()
		if !opaqueRefRe.MatchString(ref) {
			t.Fatalf("ref %q is not an opaque token", ref)
		}
		if seen[ref] {
			t.Fatalf("duplicate ref %q", ref)
		}
		seen[ref] = true
	}
}

// testProfileStoreContract is shared by the memory and Postgres stores.
func testProfileStoreContract(t *testing.T, store ProfileStore, tenantA, tenantB string) {
	t.Helper()
	ctx := context.Background()
	if err := store.Ready(ctx); err != nil {
		t.Fatalf("ready: %v", err)
	}
	const provider = "chatgpt_web"

	a1, err := store.Bind(ctx, tenantA, provider, "id-1", "node-1", t0)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if a1.State != ProfileActive || a1.TenantID != tenantA || a1.Provider != provider ||
		a1.IdentityRef != "id-1" || a1.NodeID != "node-1" || !a1.CreatedAt.Equal(t0) || !opaqueRefRe.MatchString(a1.ProfileRef) {
		t.Fatalf("unexpected binding %+v", a1)
	}
	if strings.Contains(a1.ProfileRef, tenantA) || strings.Contains(a1.ProfileRef, "id-1") {
		t.Fatalf("profile_ref %q reveals the tenant or identity", a1.ProfileRef)
	}

	// Idempotent for the same tuple; different identity or node is a new profile.
	again, err := store.Bind(ctx, tenantA, provider, "id-1", "node-1", t0.Add(time.Hour))
	if err != nil || again.ProfileRef != a1.ProfileRef || !again.CreatedAt.Equal(t0) {
		t.Fatalf("rebind = %+v, %v; want the original %+v", again, err, a1)
	}
	a2, err := store.Bind(ctx, tenantA, provider, "id-2", "node-1", t0)
	if err != nil || a2.ProfileRef == a1.ProfileRef {
		t.Fatalf("second identity = %+v, %v", a2, err)
	}
	a3, err := store.Bind(ctx, tenantA, provider, "id-1", "node-0", t0)
	if err != nil || a3.ProfileRef == a1.ProfileRef {
		t.Fatalf("same identity on another node = %+v, %v", a3, err)
	}

	// Invalid input fails closed.
	for name, args := range map[string][4]string{
		"empty tenant":   {"", provider, "id", "node-1"},
		"empty provider": {tenantA, "", "id", "node-1"},
		"empty identity": {tenantA, provider, "", "node-1"},
		"bad node id":    {tenantA, provider, "id", "../node"},
		"space":          {tenantA, provider, "i d", "node-1"},
		"oversized":      {tenantA, provider, strings.Repeat("x", 129), "node-1"},
	} {
		if _, err := store.Bind(ctx, args[0], args[1], args[2], args[3], t0); !errors.Is(err, ErrInvalidProfile) {
			t.Errorf("%s: err = %v, want ErrInvalidProfile", name, err)
		}
	}

	// Tenant B can never see, resolve or revoke tenant A's profile_ref.
	if _, found, err := store.Resolve(ctx, tenantB, a1.ProfileRef); err != nil || found {
		t.Fatalf("cross-tenant resolve: found=%v err=%v", found, err)
	}
	if list, err := store.List(ctx, tenantB, provider); err != nil || len(list) != 0 {
		t.Fatalf("cross-tenant list = %v, %v", list, err)
	}
	if revoked, err := store.Revoke(ctx, tenantB, a1.ProfileRef, t0); err != nil || revoked {
		t.Fatalf("cross-tenant revoke = %v, %v", revoked, err)
	}
	if _, found, _ := store.Resolve(ctx, tenantA, a1.ProfileRef); !found {
		t.Fatal("tenant A's profile was touched by tenant B")
	}
	// Even a successful B binding is B's own ref, never A's.
	b1, err := store.Bind(ctx, tenantB, provider, "id-1", "node-1", t0)
	if err != nil || b1.ProfileRef == a1.ProfileRef {
		t.Fatalf("tenant B binding = %+v, %v", b1, err)
	}

	// List is scoped to tenant and provider, ordered by (node, ref).
	if _, err := store.Bind(ctx, tenantA, "gemini_web", "id-1", "node-1", t0); err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx, tenantA, provider)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %v, %v; want 3", list, err)
	}
	for i := 1; i < len(list); i++ {
		prev, cur := list[i-1], list[i]
		if prev.NodeID > cur.NodeID || (prev.NodeID == cur.NodeID && prev.ProfileRef >= cur.ProfileRef) {
			t.Fatalf("list not ordered by (node_id, profile_ref): %v", list)
		}
	}

	// Revocation is sticky: the ref is dead, a new bind mints a new one.
	if revoked, err := store.Revoke(ctx, tenantA, a1.ProfileRef, t0); err != nil || !revoked {
		t.Fatalf("revoke = %v, %v", revoked, err)
	}
	if revoked, err := store.Revoke(ctx, tenantA, a1.ProfileRef, t0); err != nil || revoked {
		t.Fatalf("second revoke = %v, %v; want idempotent false", revoked, err)
	}
	if _, found, _ := store.Resolve(ctx, tenantA, a1.ProfileRef); found {
		t.Fatal("revoked profile still resolves")
	}
	rebound, err := store.Bind(ctx, tenantA, provider, "id-1", "node-1", t0)
	if err != nil || rebound.ProfileRef == a1.ProfileRef {
		t.Fatalf("bind after revoke = %+v, %v; want a new profile_ref", rebound, err)
	}
	if list, _ := store.List(ctx, tenantA, provider); len(list) != 3 {
		t.Fatalf("list after revoke+rebind = %d, want 3", len(list))
	}
}

func TestMemoryProfileStoreContract(t *testing.T) {
	testProfileStoreContract(t, NewMemoryProfileStore(), "tenant-a", "tenant-b")
}

func TestMemoryProfileStoreLimit(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryProfileStore()
	for i := 0; i < MaxProfilesPerProvider; i++ {
		if _, err := store.Bind(ctx, "t", "chatgpt_web", fmt.Sprintf("id-%d", i), "node-1", t0); err != nil {
			t.Fatalf("bind %d: %v", i, err)
		}
	}
	if _, err := store.Bind(ctx, "t", "chatgpt_web", "one-too-many", "node-1", t0); !errors.Is(err, ErrTooManyProfiles) {
		t.Fatalf("err = %v, want ErrTooManyProfiles", err)
	}
	// An existing tuple still binds (idempotent) and another tenant has its own budget.
	if _, err := store.Bind(ctx, "t", "chatgpt_web", "id-0", "node-1", t0); err != nil {
		t.Fatalf("idempotent rebind at the limit: %v", err)
	}
	if _, err := store.Bind(ctx, "other", "chatgpt_web", "id-0", "node-1", t0); err != nil {
		t.Fatalf("other tenant: %v", err)
	}
}

func binding(tenant, provider, node, ref string) Binding {
	return Binding{ProfileRef: ref, TenantID: tenant, Provider: provider, IdentityRef: "id", NodeID: node, State: ProfileActive, CreatedAt: t0}
}

func refs(bs []Binding) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.ProfileRef
	}
	return out
}

func TestEligibleProfilesTenantIsolation(t *testing.T) {
	// A careless caller hands over every row, including other tenants'.
	all := []Binding{
		binding("tenant-a", "chatgpt_web", "node-1", "pr_a1"),
		binding("tenant-a", "chatgpt_web", "node-2", "pr_a2"),
	}
	_, err := EligibleProfiles(SelectInput{TenantID: "tenant-b", Provider: "chatgpt_web", Bindings: all, NodeEligible: allNodes})
	if !errors.Is(err, ErrNoMatchingProfile) {
		t.Fatalf("tenant B err = %v, want ErrNoMatchingProfile (queue)", err)
	}

	// With its own profile present, B gets only that one, never A's.
	all = append(all, binding("tenant-b", "chatgpt_web", "node-3", "pr_b1"))
	got, err := EligibleProfiles(SelectInput{TenantID: "tenant-b", Provider: "chatgpt_web", Bindings: all, NodeEligible: allNodes})
	if err != nil || len(got) != 1 || got[0].ProfileRef != "pr_b1" {
		t.Fatalf("tenant B got %v, %v; want only pr_b1", refs(got), err)
	}

	// B's profile on an ineligible node means queue, not A's profile.
	_, err = EligibleProfiles(SelectInput{TenantID: "tenant-b", Provider: "chatgpt_web", Bindings: all, NodeEligible: only("node-1", "node-2")})
	if !errors.Is(err, ErrNoMatchingProfile) {
		t.Fatalf("err = %v, want ErrNoMatchingProfile", err)
	}
}

func TestEligibleProfilesFiltering(t *testing.T) {
	revoked := binding("t", "chatgpt_web", "node-1", "pr_revoked")
	revoked.State = ProfileRevoked
	other := binding("t", "gemini_web", "node-1", "pr_other_provider")
	nodeless := binding("t", "chatgpt_web", "", "pr_nodeless")
	refless := binding("t", "chatgpt_web", "node-1", "")
	good2 := binding("t", "chatgpt_web", "node-2", "pr_2")
	good1 := binding("t", "chatgpt_web", "node-1", "pr_1")
	in := SelectInput{
		TenantID: "t", Provider: "chatgpt_web", NodeEligible: allNodes,
		Bindings: []Binding{good2, revoked, other, nodeless, refless, good1},
	}
	got, err := EligibleProfiles(in)
	if err != nil || strings.Join(refs(got), ",") != "pr_1,pr_2" {
		t.Fatalf("got %v, %v; want pr_1,pr_2 ordered by node", refs(got), err)
	}
	in.NodeEligible = only("node-2")
	if got, err := EligibleProfiles(in); err != nil || strings.Join(refs(got), ",") != "pr_2" {
		t.Fatalf("eligible filter: got %v, %v", refs(got), err)
	}
	// The caller's slice is not reordered or aliased.
	if in.Bindings[0].ProfileRef != "pr_2" {
		t.Fatal("EligibleProfiles reordered the caller's slice")
	}
	// Fail closed: missing tenant, provider or eligibility predicate.
	for name, bad := range map[string]SelectInput{
		"no tenant":      {Provider: "chatgpt_web", Bindings: in.Bindings, NodeEligible: allNodes},
		"no provider":    {TenantID: "t", Bindings: in.Bindings, NodeEligible: allNodes},
		"no eligibility": {TenantID: "t", Provider: "chatgpt_web", Bindings: in.Bindings},
	} {
		if _, err := EligibleProfiles(bad); !errors.Is(err, ErrNoMatchingProfile) {
			t.Errorf("%s: err = %v, want ErrNoMatchingProfile", name, err)
		}
	}
}

func TestConversationResumesOnlyOnBoundNodeAndProfile(t *testing.T) {
	p1 := binding("t", "chatgpt_web", "node-1", "pr_1")
	p2 := binding("t", "chatgpt_web", "node-2", "pr_2")
	conv := func(mut func(*conversations.Conversation)) *conversations.Conversation {
		c := &conversations.Conversation{
			TenantID: "t", AppID: "app", Target: "chatgpt_web", ConversationKey: "k",
			ProviderThreadRef: "https://chat.example/c/1", State: conversations.StateActive,
			NodeID: "node-2", ProfileRef: "pr_2",
		}
		if mut != nil {
			mut(c)
		}
		return c
	}
	sel := func(c *conversations.Conversation, eligible func(string) bool, bs ...Binding) ([]Binding, error) {
		return EligibleProfiles(SelectInput{TenantID: "t", Provider: "chatgpt_web", Bindings: bs, Conversation: c, NodeEligible: eligible})
	}

	// Both profiles are eligible; the conversation still gets exactly its own.
	got, err := sel(conv(nil), allNodes, p1, p2)
	if err != nil || len(got) != 1 || got[0].ProfileRef != "pr_2" || got[0].NodeID != "node-2" {
		t.Fatalf("resume = %v, %v; want only pr_2 on node-2", refs(got), err)
	}

	cases := []struct {
		name     string
		c        *conversations.Conversation
		eligible func(string) bool
		bs       []Binding
		want     error
	}{
		{"bound node not eligible: wait, do not relocate", conv(nil), only("node-1"), []Binding{p1, p2}, ErrAffinityUnavailable},
		{"bound profile revoked", conv(nil), allNodes, []Binding{p1}, ErrConversationBroken},
		{"profile now lives on another node", conv(func(c *conversations.Conversation) { c.NodeID = "node-1" }), allNodes, []Binding{p1, p2}, ErrConversationBroken},
		{"thread bound outside the helper plane", conv(func(c *conversations.Conversation) { c.NodeID, c.ProfileRef = "", "" }), allNodes, []Binding{p1, p2}, ErrConversationBroken},
		{"conversation marked broken", conv(func(c *conversations.Conversation) { c.State = conversations.StateBroken }), allNodes, []Binding{p1, p2}, ErrConversationBroken},
		{"another tenant's conversation", conv(func(c *conversations.Conversation) { c.TenantID = "other" }), allNodes, []Binding{p1, p2}, ErrConversationBroken},
		{"another provider's conversation", conv(func(c *conversations.Conversation) { c.Target = "gemini_web" }), allNodes, []Binding{p1, p2}, ErrConversationBroken},
		{"conversation points at another tenant's profile_ref", conv(func(c *conversations.Conversation) { c.ProfileRef = "pr_a1" }), allNodes, []Binding{binding("other", "chatgpt_web", "node-2", "pr_a1"), p1, p2}, ErrConversationBroken},
	}
	for _, tc := range cases {
		if got, err := sel(tc.c, tc.eligible, tc.bs...); !errors.Is(err, tc.want) || len(got) != 0 {
			t.Errorf("%s: got %v, %v; want %v", tc.name, refs(got), err, tc.want)
		}
	}

	// No chat thread yet: any eligible profile may start it.
	fresh := conv(func(c *conversations.Conversation) { c.ProviderThreadRef, c.NodeID, c.ProfileRef = "", "", "" })
	if got, err := sel(fresh, allNodes, p1, p2); err != nil || len(got) != 2 {
		t.Fatalf("fresh conversation = %v, %v; want both profiles", refs(got), err)
	}
}

// A lost node breaks its conversations in place; they never move.
func TestLostNodeBreaksConversationWithoutMigration(t *testing.T) {
	ctx := context.Background()
	convs := conversations.NewMemoryStore()
	profiles := NewMemoryProfileStore()
	p1, _ := profiles.Bind(ctx, "t", "chatgpt_web", "id-1", "node-1", t0)
	p2, _ := profiles.Bind(ctx, "t", "chatgpt_web", "id-2", "node-2", t0)
	if _, err := convs.Bind(ctx, conversations.Conversation{
		TenantID: "t", AppID: "app", Target: "chatgpt_web", ConversationKey: "k",
		ProviderThreadRef: "https://chat.example/c/1", NodeID: "node-1", ProfileRef: p1.ProfileRef,
	}); err != nil {
		t.Fatal(err)
	}
	key := conversations.Key{TenantID: "t", AppID: "app", Target: "chatgpt_web", ConversationKey: "k"}
	resolve := func() *conversations.Conversation {
		c, found, err := convs.Resolve(ctx, key)
		if err != nil || !found {
			t.Fatalf("resolve: %v %v", found, err)
		}
		return &c
	}
	list := func(eligible func(string) bool) ([]Binding, error) {
		bs, _ := profiles.List(ctx, "t", "chatgpt_web")
		return EligibleProfiles(SelectInput{TenantID: "t", Provider: "chatgpt_web", Bindings: bs, Conversation: resolve(), NodeEligible: eligible})
	}

	if got, err := list(allNodes); err != nil || len(got) != 1 || got[0].ProfileRef != p1.ProfileRef {
		t.Fatalf("before loss: %v, %v", refs(got), err)
	}
	if n, err := convs.MarkNodeBroken(ctx, "node-1"); err != nil || n != 1 {
		t.Fatalf("MarkNodeBroken = %d, %v", n, err)
	}
	got, err := list(only("node-2")) // node-2 (p2) is healthy and eligible
	if !errors.Is(err, ErrConversationBroken) || len(got) != 0 {
		t.Fatalf("after loss: %v, %v; want ErrConversationBroken, not a move to %s", refs(got), err, p2.ProfileRef)
	}
	if c := resolve(); c.NodeID != "node-1" || c.ProfileRef != p1.ProfileRef || c.ProviderThreadRef == "" {
		t.Fatalf("conversation was migrated or rewritten: %+v", c)
	}
	// on_missing=restart: selecting again without the conversation starts a
	// fresh chat on an eligible profile.
	bs, _ := profiles.List(ctx, "t", "chatgpt_web")
	if got, err := EligibleProfiles(SelectInput{TenantID: "t", Provider: "chatgpt_web", Bindings: bs, NodeEligible: only("node-2")}); err != nil || got[0].ProfileRef != p2.ProfileRef {
		t.Fatalf("restart selection = %v, %v", refs(got), err)
	}
}
