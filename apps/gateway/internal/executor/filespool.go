package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

const maxSpoolEnvelopeBytes = 256 * 1024

// Default terminal-state spool retention bounds. done/failed/cancelled
// envelopes are NEVER read again (a terminal job is never re-enqueued: the
// job store, not the spool, is authoritative), so unbounded retention only
// grows the spool directory and slows every Stats() ReadDir. The bounds are
// env-overridable at the wiring layer (serve.go).
const (
	// DefaultSpoolRetentionTTL is the default terminal-envelope retention
	// window (7 days).
	DefaultSpoolRetentionTTL = 7 * 24 * time.Hour
	// DefaultSpoolRetentionMax caps how many terminal envelopes are kept;
	// the oldest are evicted first.
	DefaultSpoolRetentionMax = 10000
	// defaultSpoolRetentionInterval is the sweep cadence when the TTL is
	// disabled (max-count-only retention).
	defaultSpoolRetentionInterval = time.Hour
)

type FileSpoolDispatcher struct {
	root      string
	queueName string
	now       func() time.Time
	// readyMu/readyOK cache the MkdirAll + writability probe: Ready() runs
	// on every enqueue and every poll, and the probe (CreateTemp + Remove)
	// is pure overhead once the spool dirs exist. Failures are NOT latched
	// so a transient error self-heals on the next call.
	readyMu sync.Mutex
	readyOK bool
	// enqueueNotifyCh wakes the lease loop the moment an envelope lands in
	// pending/. Cap 1 with a non-blocking send: a wakeup is a hint, never a
	// delivery guarantee, and the polling ticker remains the correctness
	// fallback (an unobserved token is simply overwritten by the next send).
	enqueueNotifyCh chan struct{}
	// Terminal-state retention bounds (see SpoolRetentionConfig). Guarded by
	// retentionMu; SweepRetention reads them, SetRetention writes them.
	retentionMu  sync.Mutex
	retentionTTL time.Duration
	retentionMax int
}

// SetRetention configures the terminal-state retention bounds. Call it before
// starting RunRetentionSweeper (which stores the config it is given).
func (d *FileSpoolDispatcher) SetRetention(cfg SpoolRetentionConfig) {
	if d == nil {
		return
	}
	d.retentionMu.Lock()
	d.retentionTTL = cfg.TTL
	d.retentionMax = cfg.MaxCount
	d.retentionMu.Unlock()
}

func (d *FileSpoolDispatcher) retentionBounds() (time.Duration, int) {
	d.retentionMu.Lock()
	defer d.retentionMu.Unlock()
	return d.retentionTTL, d.retentionMax
}

func (d *FileSpoolDispatcher) retentionActive() bool {
	ttl, max := d.retentionBounds()
	return ttl > 0 || max > 0
}

type FileSpoolLease struct {
	JobID     string
	LeaseID   string
	Path      string
	Envelope  DispatchEnvelope
	LeasedAt  time.Time
	QueueName string
}

func NewFileSpoolDispatcher(root string) *FileSpoolDispatcher {
	return &FileSpoolDispatcher{
		root:            root,
		queueName:       "jobs",
		now:             time.Now,
		enqueueNotifyCh: make(chan struct{}, 1),
	}
}

// notifyEnqueue drops a wake token for the lease loop without ever blocking
// the enqueue path.
func (d *FileSpoolDispatcher) notifyEnqueue() {
	if d.enqueueNotifyCh == nil {
		return
	}
	select {
	case d.enqueueNotifyCh <- struct{}{}:
	default:
	}
}

// EnqueueNotify returns the channel the lease loop can select on to pick up
// freshly enqueued envelopes immediately. Nil-safe (nil channel blocks
// forever, which degrades to the polling fallback).
func (d *FileSpoolDispatcher) EnqueueNotify() <-chan struct{} {
	if d == nil {
		return nil
	}
	return d.enqueueNotifyCh
}

func (d *FileSpoolDispatcher) Ready(context.Context) error {
	if d == nil || d.root == "" {
		return fmt.Errorf("file spool directory is not configured")
	}
	d.readyMu.Lock()
	defer d.readyMu.Unlock()
	if d.readyOK {
		return nil
	}
	if err := d.probe(); err != nil {
		return err
	}
	d.readyOK = true
	return nil
}

func (d *FileSpoolDispatcher) probe() error {
	for _, dir := range d.stateDirs() {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	probe, err := os.CreateTemp(d.pendingDir(), ".ready-*.tmp")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func (d *FileSpoolDispatcher) EnqueueJob(ctx context.Context, job jobstore.Job) (Receipt, error) {
	if err := d.Ready(ctx); err != nil {
		return Receipt{}, err
	}
	if jobstore.TerminalStatus(job.Status) || d.jobExistsInAnyState(job.ID) {
		return d.receipt(job.ID), nil
	}

	finalPath := d.envelopePath(job.ID)
	if _, err := os.Stat(finalPath); err == nil {
		return d.receipt(job.ID), nil
	} else if !os.IsNotExist(err) {
		return Receipt{}, err
	}

	payload, err := json.Marshal(EnvelopeFromJob(job))
	if err != nil {
		return Receipt{}, err
	}
	payload = append(payload, '\n')

	tmp, err := os.CreateTemp(d.pendingDir(), job.ID+"-*.tmp")
	if err != nil {
		return Receipt{}, err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return Receipt{}, err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return Receipt{}, err
	}
	if err := renameNoOverwrite(tmpName, finalPath); err != nil {
		_ = os.Remove(tmpName)
		return Receipt{}, err
	}
	d.notifyEnqueue()

	return d.receipt(job.ID), nil
}

func (d *FileSpoolDispatcher) CancelJob(ctx context.Context, job jobstore.Job, reason string) error {
	if d == nil || d.root == "" {
		return fmt.Errorf("file spool directory is not configured")
	}
	if err := d.Ready(ctx); err != nil {
		return err
	}

	moved, err := d.movePendingToCancelled(job.ID)
	if err != nil {
		return err
	}
	leasedMoved, err := d.moveLeasedToCancelled(job.ID)
	if err != nil {
		return err
	}
	if moved || leasedMoved {
		return nil
	}
	return d.writeCancellationMarker(job, reason)
}

func (d *FileSpoolDispatcher) Stats(ctx context.Context) (Stats, error) {
	if err := d.Ready(ctx); err != nil {
		return Stats{}, err
	}

	stats := Stats{
		QueueName:        d.queueName,
		DepthByState:     map[string]int{"queued": 0, "assigned": 0, "completed": 0, "failed": 0, "cancelled": 0},
		OldestAgeByState: map[string]time.Duration{"queued": 0, "assigned": 0, "completed": 0, "failed": 0, "cancelled": 0},
	}
	for state, dir := range map[string]string{
		"queued":    d.pendingDir(),
		"assigned":  d.leasedDir(),
		"completed": d.doneDir(),
		"failed":    d.failedDir(),
		"cancelled": d.cancelledDir(),
	} {
		count, oldest, err := countJSONFiles(dir)
		if err != nil {
			return Stats{}, err
		}
		stats.DepthByState[state] = count
		if !oldest.IsZero() {
			stats.OldestAgeByState[state] = d.now().UTC().Sub(oldest.UTC())
		}
	}
	// LiveDepth counts only work a worker could still pick up; TotalDepth is
	// the full DepthByState sum including terminal spool states (done/failed/
	// cancelled keep their envelopes until the retention sweeper deletes them,
	// so the raw sum overstates the live queue forever). DepthByState is kept
	// as-is for compatibility — consumers sum queued+assigned themselves.
	for _, count := range stats.DepthByState {
		stats.TotalDepth += count
	}
	stats.LiveDepth = stats.DepthByState["queued"] + stats.DepthByState["assigned"]
	return stats, nil
}

func (d *FileSpoolDispatcher) LeaseNext(ctx context.Context) (FileSpoolLease, bool, error) {
	if err := d.Ready(ctx); err != nil {
		return FileSpoolLease{}, false, err
	}

	entries, err := os.ReadDir(d.pendingDir())
	if err != nil {
		return FileSpoolLease{}, false, err
	}
	// Single-pass oldest-first: job files are `<jobID>.json` with zero-padded
	// sequential IDs (job_%012d), so lexicographic minimum IS the oldest —
	// no O(n log n) sort on every poll.
	oldest := ""
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if oldest == "" || entry.Name() < oldest {
			oldest = entry.Name()
		}
	}
	if oldest == "" {
		return FileSpoolLease{}, false, nil
	}

	name := oldest
	jobID := strings.TrimSuffix(name, ".json")
	leasedAt := d.now().UTC()
	leaseID := fmt.Sprintf("%d", leasedAt.UnixNano())
	source := filepath.Join(d.pendingDir(), name)
	destination := filepath.Join(d.leasedDir(), fmt.Sprintf("%s.%s.json", jobID, leaseID))
	if err := os.Rename(source, destination); err != nil {
		if os.IsNotExist(err) {
			// Lost the race with another worker — report empty, not an error.
			return FileSpoolLease{}, false, nil
		}
		return FileSpoolLease{}, false, err
	}

	info, err := os.Stat(destination)
	if err != nil {
		if os.IsNotExist(err) {
			return FileSpoolLease{}, false, nil
		}
		return FileSpoolLease{}, false, err
	}
	if info.Size() > maxSpoolEnvelopeBytes {
		_ = d.moveLeasePath(destination, d.failedDir())
		return FileSpoolLease{}, false, fmt.Errorf("spool envelope %s exceeds %d bytes", filepath.Base(destination), maxSpoolEnvelopeBytes)
	}
	raw, err := os.ReadFile(destination)
	if err != nil {
		if os.IsNotExist(err) {
			return FileSpoolLease{}, false, nil
		}
		return FileSpoolLease{}, false, err
	}
	var envelope DispatchEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		_ = d.moveLeasePath(destination, d.failedDir())
		return FileSpoolLease{}, false, err
	}
	if envelope.JobID == "" {
		envelope.JobID = jobID
	}
	if envelope.JobID != jobID {
		_ = d.moveLeasePath(destination, d.failedDir())
		return FileSpoolLease{}, false, fmt.Errorf("spool envelope job_id %q does not match file job_id %q", envelope.JobID, jobID)
	}
	return FileSpoolLease{
		JobID:     envelope.JobID,
		LeaseID:   leaseID,
		Path:      destination,
		Envelope:  envelope,
		LeasedAt:  leasedAt,
		QueueName: d.queueName,
	}, true, nil
}

func (d *FileSpoolDispatcher) CompleteLease(_ context.Context, lease FileSpoolLease) error {
	return d.moveLeasePath(lease.Path, d.doneDir())
}

func (d *FileSpoolDispatcher) FailLease(_ context.Context, lease FileSpoolLease) error {
	return d.moveLeasePath(lease.Path, d.failedDir())
}

func (d *FileSpoolDispatcher) RetryLease(_ context.Context, lease FileSpoolLease) error {
	if lease.Path == "" {
		return fmt.Errorf("lease path is empty")
	}
	if err := os.MkdirAll(d.pendingDir(), 0o700); err != nil {
		return err
	}
	if err := renameNoOverwrite(lease.Path, d.envelopePath(lease.JobID)); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	d.notifyEnqueue()
	return nil
}

func (d *FileSpoolDispatcher) CancelLease(_ context.Context, lease FileSpoolLease) error {
	return d.moveLeasePath(lease.Path, d.cancelledDir())
}

// RecoverOrphanLeases moves lease files stranded in leased/ back to pending so
// their jobs re-run. A gateway restart or crash mid-RunOnce otherwise strands
// the lease forever: nothing else ever touches leased/, the stale-job reaper
// does not know the spool, and the job stays non-terminal while the queue
// grows behind it (2026-09-26/27 incident). Returns the number of envelopes
// recovered.
func (d *FileSpoolDispatcher) RecoverOrphanLeases() (int, error) {
	entries, err := os.ReadDir(d.leasedDir())
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	recovered := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		source := filepath.Join(d.leasedDir(), entry.Name())
		jobID := strings.TrimSuffix(entry.Name(), ".json")
		// Lease files are "<jobID>.<leaseID>.json"; the pending envelope is
		// "<jobID>.json".
		if idx := strings.LastIndex(jobID, "."); idx > 0 {
			jobID = jobID[:idx]
		}
		if err := os.MkdirAll(d.pendingDir(), 0o700); err != nil {
			return recovered, err
		}
		target := d.envelopePath(jobID)
		if _, err := os.Stat(target); err == nil {
			// A pending envelope for this job already exists — the stranded
			// lease is a stale duplicate; park it in cancelled/ instead of
			// overwriting the live envelope.
			_ = d.moveLeasePath(source, d.cancelledDir())
			continue
		} else if !os.IsNotExist(err) {
			return recovered, err
		}
		if err := os.Rename(source, target); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return recovered, err
		}
		recovered++
	}
	if recovered > 0 {
		d.notifyEnqueue()
	}
	return recovered, nil
}

// SpoolRetentionConfig bounds terminal-state (done/failed/cancelled) spool
// growth. TTL deletes envelopes older than the retention window; MaxCount
// caps how many terminal envelopes are kept, oldest evicted first. A zero or
// negative TTL or MaxCount disables that bound; disabling BOTH disables the
// sweeper entirely.
type SpoolRetentionConfig struct {
	TTL      time.Duration
	MaxCount int
	// Interval is the sweep cadence. When unset (<=0) it defaults to
	// TTL/4 clamped to [1m, 1h] (or defaultSpoolRetentionInterval when only
	// the max-count bound is active).
	Interval time.Duration
}

// RetentionEnabled reports whether any retention bound is active.
func (c SpoolRetentionConfig) RetentionEnabled() bool {
	return c.TTL > 0 || c.MaxCount > 0
}

// RunRetentionSweeper deletes terminal-state spool envelopes on a ticker
// until ctx is done, honoring the configured TTL and max-count bounds. It is
// a no-op when both bounds are disabled. Errors from individual deletions are
// logged and skipped so one stuck file cannot stall the sweep; the returned
// error is only the context cancellation.
func (d *FileSpoolDispatcher) RunRetentionSweeper(ctx context.Context, cfg SpoolRetentionConfig) error {
	if d == nil || d.root == "" || !cfg.RetentionEnabled() {
		return nil
	}
	d.SetRetention(cfg)
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultSpoolRetentionInterval
		if cfg.TTL > 0 {
			interval = cfg.TTL / 4
			if interval > defaultSpoolRetentionInterval {
				interval = defaultSpoolRetentionInterval
			}
		}
		if interval < time.Minute {
			interval = time.Minute
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			removed, err := d.SweepRetention(d.now())
			if err != nil {
				slog.Warn("spool retention sweep failed", "error", err)
				continue
			}
			if removed > 0 {
				slog.Info("swept terminal-state spool envelopes", "removed", removed)
			}
		}
	}
}

// SweepRetention performs one retention pass: terminal-state envelopes
// (done/failed/cancelled) older than the TTL, or the oldest beyond the
// max-count cap, are deleted oldest-first. Age is the envelope file's
// modification time — renames between state dirs preserve it, so it is the
// envelope's age in the spool (consistent with OldestAgeByState in Stats).
// Returns the number of deleted files.
func (d *FileSpoolDispatcher) SweepRetention(now time.Time) (int, error) {
	if d == nil || d.root == "" {
		return 0, fmt.Errorf("file spool directory is not configured")
	}
	if !d.retentionActive() {
		return 0, nil
	}
	ttl, maxCount := d.retentionBounds()
	type spoolEntry struct {
		path     string
		modified time.Time
	}
	var entries []spoolEntry
	for _, dir := range []string{d.doneDir(), d.failedDir(), d.cancelledDir()} {
		dirEntries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		for _, entry := range dirEntries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				if os.IsNotExist(err) {
					continue // raced a concurrent move/delete
				}
				return 0, err
			}
			entries = append(entries, spoolEntry{
				path:     filepath.Join(dir, entry.Name()),
				modified: info.ModTime(),
			})
		}
	}
	if len(entries) == 0 {
		return 0, nil
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].modified.Equal(entries[j].modified) {
			return entries[i].path < entries[j].path
		}
		return entries[i].modified.Before(entries[j].modified)
	})
	removed := 0
	for index, entry := range entries {
		// entries are sorted oldest-first, so both conditions hold for a
		// prefix: once neither holds, nothing further can qualify.
		overTTL := ttl > 0 && now.Sub(entry.modified) > ttl
		overMax := maxCount > 0 && len(entries)-index > maxCount
		if !overTTL && !overMax {
			break
		}
		if err := os.Remove(entry.path); err != nil && !os.IsNotExist(err) {
			slog.Warn("spool retention delete failed", "path", entry.path, "error", err)
			continue
		}
		removed++
	}
	return removed, nil
}

func (d *FileSpoolDispatcher) pendingDir() string {
	return filepath.Join(d.root, "pending")
}

func (d *FileSpoolDispatcher) leasedDir() string {
	return filepath.Join(d.root, "leased")
}

func (d *FileSpoolDispatcher) doneDir() string {
	return filepath.Join(d.root, "done")
}

func (d *FileSpoolDispatcher) failedDir() string {
	return filepath.Join(d.root, "failed")
}

func (d *FileSpoolDispatcher) cancelledDir() string {
	return filepath.Join(d.root, "cancelled")
}

func (d *FileSpoolDispatcher) stateDirs() []string {
	return []string{d.pendingDir(), d.leasedDir(), d.doneDir(), d.failedDir(), d.cancelledDir()}
}

func (d *FileSpoolDispatcher) envelopePath(jobID string) string {
	return filepath.Join(d.pendingDir(), jobID+".json")
}

func (d *FileSpoolDispatcher) receipt(jobID string) Receipt {
	return Receipt{
		Backend:    "file",
		QueueName:  d.queueName,
		MessageID:  jobID,
		EnqueuedAt: d.now().UTC(),
	}
}

func (d *FileSpoolDispatcher) movePendingToCancelled(jobID string) (bool, error) {
	source := d.envelopePath(jobID)
	destination := filepath.Join(d.cancelledDir(), filepath.Base(source))
	if err := renameNoOverwrite(source, destination); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (d *FileSpoolDispatcher) moveLeasedToCancelled(jobID string) (bool, error) {
	matches, err := filepath.Glob(filepath.Join(d.leasedDir(), jobID+".*.json"))
	if err != nil {
		return false, err
	}
	moved := false
	for _, source := range matches {
		if err := d.moveLeasePath(source, d.cancelledDir()); err != nil {
			return moved, err
		}
		moved = true
	}
	return moved, nil
}

func (d *FileSpoolDispatcher) writeCancellationMarker(job jobstore.Job, reason string) error {
	if d.jobExistsInTerminalState(job.ID) {
		return nil
	}
	marker := map[string]any{
		"job_id":       job.ID,
		"api_version":  job.APIVersion,
		"tenant_id":    job.TenantID,
		"app_id":       job.AppID,
		"reason":       strings.TrimSpace(reason),
		"cancelled_at": d.now().UTC().Format(time.RFC3339Nano),
	}
	payload, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return writeFileExclusive(filepath.Join(d.cancelledDir(), job.ID+".json"), payload)
}

func (d *FileSpoolDispatcher) moveLeasePath(source string, destinationDir string) error {
	if source == "" {
		return fmt.Errorf("lease path is empty")
	}
	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		return err
	}
	destination := filepath.Join(destinationDir, filepath.Base(source))
	if err := renameNoOverwrite(source, destination); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

func countJSONFiles(dir string) (int, time.Time, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, time.Time{}, err
	}
	count := 0
	var oldest time.Time
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, time.Time{}, err
		}
		count++
		modified := info.ModTime()
		if oldest.IsZero() || modified.Before(oldest) {
			oldest = modified
		}
	}
	return count, oldest, nil
}

func (d *FileSpoolDispatcher) jobExistsInAnyState(jobID string) bool {
	if _, ok := d.findJobPath(jobID, d.pendingDir()); ok {
		return true
	}
	if _, ok := d.findJobPath(jobID, d.leasedDir()); ok {
		return true
	}
	return d.jobExistsInTerminalState(jobID)
}

func (d *FileSpoolDispatcher) jobExistsInTerminalState(jobID string) bool {
	for _, dir := range []string{d.doneDir(), d.failedDir(), d.cancelledDir()} {
		if _, ok := d.findJobPath(jobID, dir); ok {
			return true
		}
	}
	return false
}

func (d *FileSpoolDispatcher) findJobPath(jobID string, dir string) (string, bool) {
	exact := filepath.Join(dir, jobID+".json")
	if _, err := os.Stat(exact); err == nil {
		return exact, true
	}
	matches, err := filepath.Glob(filepath.Join(dir, jobID+".*.json"))
	if err != nil || len(matches) == 0 {
		return "", false
	}
	return matches[0], true
}

// renameNoOverwrite moves source to destination, refusing to clobber an
// existing destination (a duplicate is dropped: source removed, nil returned).
//
// os.Rename silently overwrites on POSIX, so the old Stat-then-Rename pair was
// a TOCTOU race: two concurrent movers could both pass the Stat and the second
// rename would overwrite the first. os.Link is the real CAS: it fails with
// EEXIST when the destination exists, atomically, on the same filesystem.
// os.Link is unsupported on some filesystems/platforms, so a Link error other
// than EEXIST falls back to the legacy rename behavior (pre-check + IsExist
// handling keeps that window minimal and its outcome identical).
func renameNoOverwrite(source string, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		_ = os.Remove(source)
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Link(source, destination); err == nil {
		return os.Remove(source)
	} else if os.IsExist(err) {
		_ = os.Remove(source)
		return nil
	}
	if err := os.Rename(source, destination); err != nil {
		if os.IsExist(err) {
			_ = os.Remove(source)
			return nil
		}
		return err
	}
	return nil
}

func writeFileExclusive(path string, payload []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	_, err = file.Write(payload)
	return err
}
