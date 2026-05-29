package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

const maxSpoolEnvelopeBytes = 256 * 1024

type FileSpoolDispatcher struct {
	root      string
	queueName string
	now       func() time.Time
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
		root:      root,
		queueName: "jobs",
		now:       time.Now,
	}
}

func (d *FileSpoolDispatcher) Ready(context.Context) error {
	if d == nil || d.root == "" {
		return fmt.Errorf("file spool directory is not configured")
	}
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

	payload, err := json.MarshalIndent(EnvelopeFromJob(job), "", "  ")
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
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		jobID := strings.TrimSuffix(entry.Name(), ".json")
		leasedAt := d.now().UTC()
		leaseID := fmt.Sprintf("%d", leasedAt.UnixNano())
		source := filepath.Join(d.pendingDir(), entry.Name())
		destination := filepath.Join(d.leasedDir(), fmt.Sprintf("%s.%s.json", jobID, leaseID))
		if err := os.Rename(source, destination); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return FileSpoolLease{}, false, err
		}

		info, err := os.Stat(destination)
		if err != nil {
			if os.IsNotExist(err) {
				continue
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
				continue
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

	return FileSpoolLease{}, false, nil
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
	return nil
}

func (d *FileSpoolDispatcher) CancelLease(_ context.Context, lease FileSpoolLease) error {
	return d.moveLeasePath(lease.Path, d.cancelledDir())
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
	payload, err := json.MarshalIndent(marker, "", "  ")
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

func renameNoOverwrite(source string, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		_ = os.Remove(source)
		return nil
	} else if !os.IsNotExist(err) {
		return err
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
