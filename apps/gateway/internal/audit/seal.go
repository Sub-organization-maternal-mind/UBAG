package audit

import (
	"context"
	"fmt"
	"time"
)

// SealHead appends a signed chain anchor record for the given tenant.
// The anchor records the current chain head hash and sequence, making
// future VerifyChain calls anchorable to a specific checkpoint.
//
// The seal record uses:
//
//	Action:     "audit:seal"
//	Resource:   "audit:chain"
//	Outcome:    "sealed"
//	Attributes: {"head_hash": headHash, "head_seq": seq}
//
// On an empty chain the head hash is GenesisHash ("") and seq is 0.
func SealHead(ctx context.Context, store Store, tenantID, appID, actor string) (Record, error) {
	headHash, seq, err := store.Head(ctx, tenantID)
	if err != nil {
		return Record{}, fmt.Errorf("audit: seal head: %w", err)
	}
	rec := Record{
		TenantID:   tenantID,
		AppID:      appID,
		Actor:      actor,
		Action:     "audit:seal",
		Resource:   "audit:chain",
		Outcome:    "sealed",
		OccurredAt: time.Now(),
		Attributes: map[string]any{
			"head_hash": headHash,
			"head_seq":  seq,
		},
	}
	result, err := store.Append(ctx, rec)
	if err != nil {
		return Record{}, fmt.Errorf("audit: seal head append: %w", err)
	}
	return result, nil
}
