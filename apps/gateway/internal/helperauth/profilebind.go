// Package helperauth holds the primary-side trust decisions of the helper plane
// (UBAG_HELPER_PLANE, default off). This file is profile and conversation
// affinity: which browser profile on which helper node a tenant's job may use.
//
// A profile_ref is an opaque handle for ONE logged-in browser profile on ONE
// helper node, owned by one (tenant, provider, identity_ref). The primary mints
// it (random; it reveals neither tenant nor identity) and is the only party that
// maps it back. The helper receives only the profile_ref (see
// executor.HelperAttemptSpec, which strips user_data_dir, profile_dir,
// profile_path and the manual-session context) and derives its profile
// directory from it alone. Logins happen out of band on the helper (safe mode:
// no automated login), so a profile_ref exists only after an operator binds it.
//
// Fail closed: a tenant is only ever offered its own active profiles; when none
// is on an eligible node the answer is "queue", never "assign something else".
// Nothing here is wired into dispatch yet (the placer is P4.17), so with the
// flag off the package is never called.
package helperauth

import (
	"context"
	"crypto/rand"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/conversations"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
)

// Profile binding states.
const (
	ProfileActive  = "active"
	ProfileRevoked = "revoked"
)

// MaxProfilesPerProvider bounds the active bindings of one (tenant, provider).
const MaxProfilesPerProvider = 256

const maxTokenLen = 128

var (
	// ErrNoMatchingProfile: the tenant has no active profile on an eligible
	// node (F9). The caller queues the job; it never assigns another tenant's
	// profile or an unbound one.
	ErrNoMatchingProfile = errors.New("helperauth: no eligible profile_ref for the tenant; queue, do not assign")
	// ErrAffinityUnavailable: the conversation resumes only on its bound node
	// and profile and that node is not eligible right now. Queue until it is.
	ErrAffinityUnavailable = errors.New("helperauth: the conversation's bound node is not eligible; queue until it is")
	// ErrConversationBroken: the conversation cannot be resumed (marked
	// broken, bound outside the helper plane, scope mismatch, or its profile is
	// gone or revoked). The caller applies the job's on_missing policy: fail,
	// or start a fresh chat by selecting again without the conversation.
	ErrConversationBroken = errors.New("helperauth: the conversation cannot be resumed on a helper")
	// ErrInvalidProfile: a binding field is missing, oversized or malformed.
	ErrInvalidProfile = errors.New("helperauth: invalid profile binding")
	// ErrTooManyProfiles: MaxProfilesPerProvider active bindings already exist.
	ErrTooManyProfiles = errors.New("helperauth: profile limit reached for the tenant and provider")
	// ErrNotConfigured: the store is nil or has no database.
	ErrNotConfigured = errors.New("helperauth: profile store is not configured")
)

// Binding is one tenant-owned profile. Provider is the job target id (for
// example chatgpt_web, the same string as Conversation.Target); IdentityRef is
// the opaque slot reference the topology lanes use. NodeID is the helper node
// whose browser profile holds the login.
type Binding struct {
	ProfileRef  string
	TenantID    string
	Provider    string
	IdentityRef string
	NodeID      string
	State       string
	CreatedAt   time.Time
	RevokedAt   time.Time
}

// ProfileStore is the tenant-owned profile_ref registry. Memory and Postgres
// implement it; SQLite deliberately does not (the edge profile refuses helper
// mode). Every lookup is scoped by tenant: another tenant's profile_ref is
// indistinguishable from an unknown one. Callers pass the clock in.
type ProfileStore interface {
	// Ready fails closed when the backing schema is not usable.
	Ready(ctx context.Context) error
	// Bind registers the profile for (tenant, provider, identity, node) and
	// returns it. It is idempotent: an active binding for the same tuple is
	// returned unchanged. A revoked profile never comes back, so binding again
	// after a revoke mints a new profile_ref (and needs a fresh login).
	Bind(ctx context.Context, tenantID, provider, identityRef, nodeID string, now time.Time) (Binding, error)
	// Resolve returns the tenant's ACTIVE profile for profileRef; revoked,
	// unknown and other tenants' refs all report found=false.
	Resolve(ctx context.Context, tenantID, profileRef string) (Binding, bool, error)
	// List returns the tenant's active profiles for provider ordered by
	// (node_id, profile_ref), at most MaxProfilesPerProvider.
	List(ctx context.Context, tenantID, provider string) ([]Binding, error)
	// Revoke retires the tenant's active profile_ref (sticky) and reports
	// whether one was revoked; it is idempotent.
	Revoke(ctx context.Context, tenantID, profileRef string, now time.Time) (bool, error)
}

// MintProfileRef returns a new opaque profile_ref: "pr_" plus 128 random bits.
// It satisfies the executor's profile_ref pattern (no path characters).
func MintProfileRef() string { return "pr_" + rand.Text() }

func validToken(s string) bool {
	if s == "" || len(s) > maxTokenLen {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

func validateBindArgs(tenantID, provider, identityRef, nodeID string) error {
	if !validToken(tenantID) || !validToken(provider) || !validToken(identityRef) || !nodes.ValidNodeID(nodeID) {
		return ErrInvalidProfile
	}
	return nil
}

func sortBindings(b []Binding) {
	sort.Slice(b, func(i, j int) bool {
		if b[i].NodeID != b[j].NodeID {
			return b[i].NodeID < b[j].NodeID
		}
		return b[i].ProfileRef < b[j].ProfileRef
	})
}

// SelectInput is the pure input of EligibleProfiles.
type SelectInput struct {
	TenantID string
	// Provider is the job target id.
	Provider string
	// Bindings are the candidate profiles, normally ProfileStore.List for the
	// tenant and provider. Anything that is not the tenant's active profile
	// for the provider is ignored, so a careless caller cannot leak across
	// tenants.
	Bindings []Binding
	// Conversation is the job's conversation binding, or nil when the job has
	// none (or the caller is starting a fresh chat after ErrConversationBroken).
	Conversation *conversations.Conversation
	// NodeEligible reports whether a node may take work now (the P4.1
	// Evaluate decision). Nil means nothing is eligible.
	NodeEligible func(nodeID string) bool
}

// EligibleProfiles returns the profiles a job may be placed on, ordered by
// (node_id, profile_ref); the placer picks the first with a free identity lane.
//
//   - Only the tenant's own active profiles for the provider on eligible nodes
//     are ever returned. None means ErrNoMatchingProfile: queue, never assign.
//   - A conversation with a bound chat thread resumes only on its bound node
//     and profile: exactly one profile is returned, ErrAffinityUnavailable when
//     its node is not eligible (wait, do not relocate) and ErrConversationBroken
//     when it cannot resume at all (broken, bound outside the helper plane,
//     scope mismatch, profile revoked or moved).
//   - A conversation with no thread yet (or no conversation) may use any
//     eligible profile; the placer then binds node_id and profile_ref on it.
func EligibleProfiles(in SelectInput) ([]Binding, error) {
	if in.TenantID == "" || in.Provider == "" || in.NodeEligible == nil {
		return nil, ErrNoMatchingProfile
	}
	own := make([]Binding, 0, len(in.Bindings))
	for _, b := range in.Bindings {
		if b.TenantID == in.TenantID && b.Provider == in.Provider && b.State == ProfileActive &&
			b.ProfileRef != "" && b.NodeID != "" {
			own = append(own, b)
		}
	}
	sortBindings(own)

	if c := in.Conversation; c != nil {
		if c.State == conversations.StateBroken || c.TenantID != in.TenantID || c.Target != in.Provider {
			return nil, ErrConversationBroken
		}
		if c.ProviderThreadRef != "" {
			if c.NodeID == "" || c.ProfileRef == "" {
				return nil, ErrConversationBroken // the thread lives on the local worker
			}
			for _, b := range own {
				if b.ProfileRef != c.ProfileRef {
					continue
				}
				if b.NodeID != c.NodeID {
					return nil, ErrConversationBroken
				}
				if !in.NodeEligible(b.NodeID) {
					return nil, ErrAffinityUnavailable
				}
				return []Binding{b}, nil
			}
			return nil, ErrConversationBroken
		}
	}

	eligible := own[:0]
	for _, b := range own {
		if in.NodeEligible(b.NodeID) {
			eligible = append(eligible, b)
		}
	}
	if len(eligible) == 0 {
		return nil, ErrNoMatchingProfile
	}
	return eligible, nil
}

// MemoryProfileStore is the in-process ProfileStore (development and tests).
type MemoryProfileStore struct {
	mu    sync.Mutex
	byRef map[string]Binding
}

var _ ProfileStore = (*MemoryProfileStore)(nil)

func NewMemoryProfileStore() *MemoryProfileStore {
	return &MemoryProfileStore{byRef: make(map[string]Binding)}
}

func (s *MemoryProfileStore) Ready(context.Context) error { return nil }

func (s *MemoryProfileStore) Bind(_ context.Context, tenantID, provider, identityRef, nodeID string, now time.Time) (Binding, error) {
	if err := validateBindArgs(tenantID, provider, identityRef, nodeID); err != nil {
		return Binding{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// ponytail: linear scan; the table is operator-sized (256 per tenant and provider).
	active := 0
	for _, b := range s.byRef {
		if b.State != ProfileActive || b.TenantID != tenantID || b.Provider != provider {
			continue
		}
		if b.IdentityRef == identityRef && b.NodeID == nodeID {
			return b, nil
		}
		active++
	}
	if active >= MaxProfilesPerProvider {
		return Binding{}, ErrTooManyProfiles
	}
	b := Binding{
		ProfileRef: MintProfileRef(), TenantID: tenantID, Provider: provider, IdentityRef: identityRef,
		NodeID: nodeID, State: ProfileActive, CreatedAt: now.UTC().Truncate(time.Microsecond),
	}
	s.byRef[b.ProfileRef] = b
	return b, nil
}

func (s *MemoryProfileStore) Resolve(_ context.Context, tenantID, profileRef string) (Binding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.byRef[profileRef]
	if !ok || b.TenantID != tenantID || b.State != ProfileActive {
		return Binding{}, false, nil
	}
	return b, true, nil
}

func (s *MemoryProfileStore) List(_ context.Context, tenantID, provider string) ([]Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Binding{}
	for _, b := range s.byRef {
		if b.State == ProfileActive && b.TenantID == tenantID && b.Provider == provider {
			out = append(out, b)
		}
	}
	sortBindings(out)
	return out, nil
}

func (s *MemoryProfileStore) Revoke(_ context.Context, tenantID, profileRef string, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.byRef[profileRef]
	if !ok || b.TenantID != tenantID || b.State != ProfileActive {
		return false, nil
	}
	b.State, b.RevokedAt = ProfileRevoked, now.UTC().Truncate(time.Microsecond)
	s.byRef[profileRef] = b
	return true, nil
}
