// Package compliance implements §28 data-classification and privacy controls
// for HIPAA/GDPR workloads: classification tags, cache/log bypass rules, and
// privacy request lifecycle (export + erase).
package compliance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Data classification
// ─────────────────────────────────────────────────────────────────────────────

// DataClassification indicates the sensitivity of data processed by a job.
type DataClassification int

const (
	ClassPublic       DataClassification = iota // no restriction
	ClassInternal                               // internal only
	ClassConfidential                           // confidential — no external exposure
	ClassRestricted                             // restricted to specific roles
	ClassPII                                    // Personally Identifiable Information
	ClassPHI                                    // Protected Health Information (HIPAA)
)

func (c DataClassification) String() string {
	switch c {
	case ClassPublic:
		return "public"
	case ClassInternal:
		return "internal"
	case ClassConfidential:
		return "confidential"
	case ClassRestricted:
		return "restricted"
	case ClassPII:
		return "pii"
	case ClassPHI:
		return "phi"
	default:
		return "unknown"
	}
}

// medicalCommandPrefixes identifies command types that carry PHI.
var medicalCommandPrefixes = []string{
	"radiology.", "pathology.", "clinical.", "ehr.", "hipaa.",
	"loinc.", "icd10.", "medical.", "patient.", "health.",
}

// piiCommandPrefixes identifies command types that typically carry PII.
var piiCommandPrefixes = []string{
	"user.", "personal.", "contact.", "identity.", "gdpr.",
}

// ClassifyRequest returns the data classification for a job based on its
// tenant and command_type. PHI > PII > Internal.
func ClassifyRequest(tenantID, commandType string) DataClassification {
	ct := strings.ToLower(strings.TrimSpace(commandType))
	for _, prefix := range medicalCommandPrefixes {
		if strings.HasPrefix(ct, prefix) {
			return ClassPHI
		}
	}
	for _, prefix := range piiCommandPrefixes {
		if strings.HasPrefix(ct, prefix) {
			return ClassPII
		}
	}
	_ = tenantID // reserved for per-tenant classification rules
	return ClassInternal
}

// ShouldSkipCache reports whether a job with classification c must bypass the
// response cache (PII and PHI are never cached).
func ShouldSkipCache(c DataClassification) bool {
	return c == ClassPII || c == ClassPHI
}

// ShouldSkipLog reports whether a job's payload with classification c must not
// be written to access logs (PHI only — PII may be logged with redaction).
func ShouldSkipLog(c DataClassification) bool {
	return c == ClassPHI
}

// ─────────────────────────────────────────────────────────────────────────────
// Privacy request lifecycle
// ─────────────────────────────────────────────────────────────────────────────

// RequestKind distinguishes export from erasure requests.
type RequestKind string

const (
	KindExport RequestKind = "export"
	KindErase  RequestKind = "erase"
)

// RequestStatus is the lifecycle state of a privacy request.
type RequestStatus string

const (
	StatusPending    RequestStatus = "pending"
	StatusProcessing RequestStatus = "processing"
	StatusCompleted  RequestStatus = "completed"
	StatusFailed     RequestStatus = "failed"
)

// PrivacyRequest is a GDPR/HIPAA data-subject request.
type PrivacyRequest struct {
	ID         string
	TenantID   string
	SubjectRef string
	Kind       RequestKind
	Status     RequestStatus
	Receipt    string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ErrNotFound is returned when a request ID is not found.
var ErrNotFound = errors.New("compliance: request not found")

// Store persists privacy requests.
type Store interface {
	Create(ctx context.Context, req PrivacyRequest) (PrivacyRequest, error)
	Get(ctx context.Context, id string) (PrivacyRequest, bool, error)
	UpdateStatus(ctx context.Context, id string, status RequestStatus) (PrivacyRequest, bool, error)
}

// MemoryStore is an in-memory privacy request store.
type MemoryStore struct {
	mu   sync.Mutex
	reqs map[string]PrivacyRequest
	now  func() time.Time
}

// NewMemoryStore returns an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		reqs: make(map[string]PrivacyRequest),
		now:  time.Now,
	}
}

func (m *MemoryStore) Create(_ context.Context, req PrivacyRequest) (PrivacyRequest, error) {
	if strings.TrimSpace(req.TenantID) == "" {
		return PrivacyRequest{}, errors.New("compliance: tenant_id required")
	}
	if strings.TrimSpace(req.SubjectRef) == "" {
		return PrivacyRequest{}, errors.New("compliance: subject_ref required")
	}
	if req.Kind != KindExport && req.Kind != KindErase {
		return PrivacyRequest{}, fmt.Errorf("compliance: unknown kind %q", req.Kind)
	}
	now := m.now().UTC()
	req.ID = newRequestID()
	req.Status = StatusPending
	req.Receipt = newRequestID()
	req.CreatedAt = now
	req.UpdatedAt = now

	m.mu.Lock()
	m.reqs[req.ID] = req
	m.mu.Unlock()
	return req, nil
}

func (m *MemoryStore) Get(_ context.Context, id string) (PrivacyRequest, bool, error) {
	m.mu.Lock()
	req, ok := m.reqs[id]
	m.mu.Unlock()
	return req, ok, nil
}

func (m *MemoryStore) UpdateStatus(_ context.Context, id string, status RequestStatus) (PrivacyRequest, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, ok := m.reqs[id]
	if !ok {
		return PrivacyRequest{}, false, nil
	}
	req.Status = status
	req.UpdatedAt = m.now().UTC()
	m.reqs[id] = req
	return req, true, nil
}

func newRequestID() string {
	buf := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return "priv_" + hex.EncodeToString(buf)
}
