package jobs

import (
	"context"
	"fmt"
	"sort"
	"time"
)

var _ AttemptStore = (*MemoryStore)(nil)

// attemptIndexLocked finds ref's attempt in the job's history (m.mu held).
func (m *MemoryStore) attemptIndexLocked(ref AttemptRef) (int, bool) {
	for i, a := range m.attempts[ref.JobID] {
		if a.AttemptID == ref.AttemptID {
			return i, true
		}
	}
	return -1, false
}

func (m *MemoryStore) BeginAttempt(_ context.Context, request BeginAttemptRequest) (Attempt, error) {
	ttl, err := request.validate()
	if err != nil {
		return Attempt{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	job, ok := m.jobs[request.JobID]
	if !ok {
		return Attempt{}, fmt.Errorf("%w: job %s", ErrAttemptNotFound, request.JobID)
	}
	if TerminalStatus(job.Status) {
		return Attempt{}, ErrAttemptJobTerminal
	}
	now := m.now().UTC()
	plan, err := planBegin(request, ttl, m.attempts[request.JobID], now)
	if err != nil || plan.replay {
		return plan.attempt, err
	}
	history := m.attempts[request.JobID]
	if plan.expire != "" {
		last := &history[len(history)-1]
		last.State, last.UpdatedAt, last.EndedAt = AttemptExpired, now, &now
	}
	m.attempts[request.JobID] = append(history, plan.attempt)
	return plan.attempt, nil
}

func (m *MemoryStore) RenewAttempt(_ context.Context, ref AttemptRef, ttl time.Duration) (Attempt, error) {
	if err := ref.validate(); err != nil {
		return Attempt{}, err
	}
	ttl, err := attemptTTL(ttl)
	if err != nil {
		return Attempt{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	i, found := m.attemptIndexLocked(ref)
	var a Attempt
	if found {
		a = m.attempts[ref.JobID][i]
	}
	if err := fenceAttempt(ref, a, found, false); err != nil {
		return Attempt{}, err
	}
	now := m.now().UTC()
	if until := now.Add(ttl); until.After(a.LeaseExpiresAt) {
		a.LeaseExpiresAt = until
	}
	a.UpdatedAt = now
	m.attempts[ref.JobID][i] = a
	return a, nil
}

func (m *MemoryStore) MarkSubmitted(_ context.Context, ref AttemptRef) (Attempt, error) {
	if err := ref.validate(); err != nil {
		return Attempt{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	i, found := m.attemptIndexLocked(ref)
	var a Attempt
	if found {
		a = m.attempts[ref.JobID][i]
	}
	if err := fenceAttempt(ref, a, found, false); err != nil {
		return Attempt{}, err
	}
	now := m.now().UTC()
	if a.SubmittedAt == nil {
		a.SubmittedAt = &now
	}
	a.UpdatedAt = now
	m.attempts[ref.JobID][i] = a
	return a, nil
}

func (m *MemoryStore) CommitEvents(_ context.Context, ref AttemptRef, events []WorkerEvent) (Job, error) {
	if err := validateAttemptCommit(ref, events); err != nil {
		return Job{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	i, found := m.attemptIndexLocked(ref)
	var a Attempt
	if found {
		a = m.attempts[ref.JobID][i]
	}
	if err := fenceAttempt(ref, a, found, true); err != nil {
		return Job{}, err
	}
	job, ok := m.jobs[ref.JobID]
	if !ok {
		return Job{}, fmt.Errorf("%w: job %s", ErrAttemptNotFound, ref.JobID)
	}
	job, err := m.applyBatchLocked(job, events)
	if err != nil {
		return Job{}, err
	}
	if a.State == AttemptActive && TerminalStatus(job.Status) {
		now := m.now().UTC()
		a.State, a.UpdatedAt, a.EndedAt = AttemptFinished, now, &now
		m.attempts[ref.JobID][i] = a
	}
	return job, nil
}

func (m *MemoryStore) ExpireAttempts(_ context.Context, limit int) ([]Attempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now().UTC()
	var due []Attempt
	for _, history := range m.attempts {
		for _, a := range history {
			if a.State == AttemptActive && !now.Before(a.LeaseExpiresAt) {
				due = append(due, a)
			}
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].LeaseExpiresAt.Equal(due[j].LeaseExpiresAt) {
			return due[i].LeaseExpiresAt.Before(due[j].LeaseExpiresAt)
		}
		if due[i].JobID != due[j].JobID {
			return due[i].JobID < due[j].JobID
		}
		return due[i].AttemptID < due[j].AttemptID
	})
	due = due[:min(len(due), expireBatch(limit))]
	for k := range due {
		i, _ := m.attemptIndexLocked(due[k].Ref())
		a := &m.attempts[due[k].JobID][i]
		a.State, a.UpdatedAt, a.EndedAt = AttemptExpired, now, &now
		due[k] = *a
	}
	return due, nil
}

func (m *MemoryStore) ListAttempts(_ context.Context, jobID string) ([]Attempt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return append([]Attempt(nil), m.attempts[jobID]...), nil
}
