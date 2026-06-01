package compliance

import (
	"context"
	"testing"
)

func TestClassifyRequestPHI(t *testing.T) {
	cases := []string{
		"radiology.ct.report",
		"medical.note.generate",
		"hipaa.phi.extract",
		"loinc.lab.result",
		"health.record.summarize",
	}
	for _, ct := range cases {
		if got := ClassifyRequest("tenant", ct); got != ClassPHI {
			t.Errorf("ClassifyRequest(%q) = %v, want PHI", ct, got)
		}
	}
}

func TestClassifyRequestPII(t *testing.T) {
	cases := []string{"user.profile.update", "personal.data.export", "gdpr.subject"}
	for _, ct := range cases {
		if got := ClassifyRequest("tenant", ct); got != ClassPII {
			t.Errorf("ClassifyRequest(%q) = %v, want PII", ct, got)
		}
	}
}

func TestClassifyRequestInternal(t *testing.T) {
	cases := []string{"submit", "generate.text", "translate.document"}
	for _, ct := range cases {
		if got := ClassifyRequest("tenant", ct); got != ClassInternal {
			t.Errorf("ClassifyRequest(%q) = %v, want Internal", ct, got)
		}
	}
}

func TestShouldSkipCache(t *testing.T) {
	if !ShouldSkipCache(ClassPHI) {
		t.Error("PHI must skip cache")
	}
	if !ShouldSkipCache(ClassPII) {
		t.Error("PII must skip cache")
	}
	if ShouldSkipCache(ClassInternal) {
		t.Error("Internal must not skip cache")
	}
	if ShouldSkipCache(ClassPublic) {
		t.Error("Public must not skip cache")
	}
}

func TestShouldSkipLog(t *testing.T) {
	if !ShouldSkipLog(ClassPHI) {
		t.Error("PHI must skip log")
	}
	if ShouldSkipLog(ClassPII) {
		t.Error("PII must not skip log")
	}
	if ShouldSkipLog(ClassInternal) {
		t.Error("Internal must not skip log")
	}
}

func TestPrivacyRequestCreateGet(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	req, err := store.Create(ctx, PrivacyRequest{
		TenantID:   "tenant1",
		SubjectRef: "user@example.com",
		Kind:       KindExport,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if req.ID == "" {
		t.Error("ID must be set")
	}
	if req.Status != StatusPending {
		t.Errorf("Status = %v, want pending", req.Status)
	}
	if req.Receipt == "" {
		t.Error("Receipt must be set")
	}

	got, ok, err := store.Get(ctx, req.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.SubjectRef != "user@example.com" {
		t.Errorf("SubjectRef = %q", got.SubjectRef)
	}
}

func TestPrivacyRequestUpdateStatus(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	req, _ := store.Create(ctx, PrivacyRequest{TenantID: "t", SubjectRef: "s", Kind: KindErase})

	updated, ok, err := store.UpdateStatus(ctx, req.ID, StatusCompleted)
	if err != nil || !ok {
		t.Fatalf("UpdateStatus: ok=%v err=%v", ok, err)
	}
	if updated.Status != StatusCompleted {
		t.Errorf("Status = %v, want completed", updated.Status)
	}
}

func TestPrivacyRequestCreateValidation(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()

	if _, err := store.Create(ctx, PrivacyRequest{TenantID: "", SubjectRef: "s", Kind: KindExport}); err == nil {
		t.Error("Create must fail for empty tenant_id")
	}
	if _, err := store.Create(ctx, PrivacyRequest{TenantID: "t", SubjectRef: "", Kind: KindExport}); err == nil {
		t.Error("Create must fail for empty subject_ref")
	}
	if _, err := store.Create(ctx, PrivacyRequest{TenantID: "t", SubjectRef: "s", Kind: "unknown"}); err == nil {
		t.Error("Create must fail for unknown kind")
	}
}

func TestDataClassificationString(t *testing.T) {
	if ClassPHI.String() != "phi" {
		t.Errorf("PHI.String() = %q", ClassPHI.String())
	}
	if ClassPII.String() != "pii" {
		t.Errorf("PII.String() = %q", ClassPII.String())
	}
}
