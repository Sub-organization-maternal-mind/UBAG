package helper

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func writeCgroup(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCgroupSamplerReportsTheBudgetAndFailsClosedWhenItCannotRead(t *testing.T) {
	root, proc := t.TempDir(), t.TempDir()
	writeCgroup(t, root, map[string]string{
		"memory.max":     "4294967296\n",
		"memory.current": "2147483648\n",
		"memory.stat":    "anon 1\ninactive_file 1073741824\nactive_file 5\n",
		"cpu.max":        "200000 100000\n",
		"cpu.stat":       "usage_usec 1000000\nuser_usec 5\n",
	})
	s := NewCgroupSampler(root, proc)
	base := time.Unix(1_000_000, 0)
	cur := base
	s.now = func() time.Time { return cur }
	s.lastAt = base

	// No rate exists until a second has passed: the first answer is "not ready",
	// which the service reports as zero totals (worst case), never as idle.
	if _, err := s.Sample(); err != errWarmingUp {
		t.Fatalf("first sample: %v", err)
	}
	// 5 CPU-seconds over 10 s on a 2-core quota is 500 millicores of 2000.
	cur = base.Add(10 * time.Second)
	writeCgroup(t, root, map[string]string{"cpu.stat": "usage_usec 6000000\n"})
	hs, err := s.Sample()
	if err != nil {
		t.Fatal(err)
	}
	want := HostStats{CPUMillisTotal: 2000, CPUMillisUsed: 500, MemoryBytesTotal: 4 << 30, MemoryBytesUsed: 1 << 30}
	if hs != want { // working set: usage minus inactive file cache
		t.Fatalf("got %+v, want %+v", hs, want)
	}
	// A second sample inside one second reuses the rate instead of dividing by noise.
	cur = cur.Add(200 * time.Millisecond)
	if hs2, err := s.Sample(); err != nil || hs2.CPUMillisUsed != 500 {
		t.Fatalf("rapid sample: %+v %v", hs2, err)
	}

	// Page cache larger than usage clamps at zero rather than going negative.
	writeCgroup(t, root, map[string]string{"memory.stat": "inactive_file 9999999999\n"})
	if hs, err = s.Sample(); err != nil || hs.MemoryBytesUsed != 0 {
		t.Fatalf("clamp: %+v %v", hs, err)
	}

	// No limits: the machine totals take over.
	writeCgroup(t, root, map[string]string{"memory.max": "max\n", "cpu.max": "max 100000\n"})
	writeCgroup(t, proc, map[string]string{"meminfo": "MemTotal:        8192000 kB\nMemFree: 1 kB\n"})
	cur = cur.Add(10 * time.Second)
	if hs, err = s.Sample(); err != nil || hs.MemoryBytesTotal != 8192000*1024 || hs.CPUMillisTotal != int32(runtime.NumCPU()*1000) {
		t.Fatalf("unlimited cgroup: %+v %v", hs, err)
	}

	for name, files := range map[string]map[string]string{
		"unusable memory.max": {"memory.max": "0\n"},
		"garbage cpu.max":     {"cpu.max": "wat\n"},
		"zero quota":          {"cpu.max": "0 100000\n"},
		"no usage_usec":       {"cpu.stat": "user_usec 1\n"},
		"memory.max not int":  {"memory.max": "lots\n"},
	} {
		d := t.TempDir()
		writeCgroup(t, d, map[string]string{
			"memory.max": "1000\n", "memory.current": "1\n", "cpu.max": "100000 100000\n", "cpu.stat": "usage_usec 1\n",
		})
		writeCgroup(t, d, files)
		bad := NewCgroupSampler(d, proc)
		bad.now = func() time.Time { return base.Add(time.Hour) }
		bad.lastAt = base
		if _, err := bad.Sample(); err == nil {
			t.Errorf("%s: must be an error", name)
		}
	}
	if _, err := NewCgroupSampler(filepath.Join(t.TempDir(), "absent"), proc).Sample(); err == nil {
		t.Error("a missing cgroup (v1 or non-Linux) must be an error, not zeros that look idle")
	}
}
