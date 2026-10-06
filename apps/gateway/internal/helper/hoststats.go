package helper

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errWarmingUp = errors.New("cgroup cpu baseline not taken yet")

// CgroupSampler reports this process's cgroup v2 budget (ADR-0009: the helper
// reports its own cgroup use; the primary never measures a helper). Memory is
// the working set (usage minus inactive file cache, like cAdvisor) against
// memory.max, or the machine total when the cgroup has no limit. CPU is the
// usage_usec rate since the previous sample against cpu.max, or all cores.
//
// Anything it cannot read (cgroup v1, a non-Linux box) is an error, which the
// service turns into a zero-total report: worst-case pressure on the primary,
// never an idle-looking node.
type CgroupSampler struct {
	root, procRoot string
	now            func() time.Time

	mu       sync.Mutex
	lastUsec int64
	lastAt   time.Time
	lastUsed int32
	primed   bool // a baseline exists
	measured bool // lastUsed is a real rate, not a placeholder
}

// NewCgroupSampler reads under cgroupRoot (default /sys/fs/cgroup) and procRoot
// (default /proc), taking the CPU baseline now.
func NewCgroupSampler(cgroupRoot, procRoot string) *CgroupSampler {
	if cgroupRoot == "" {
		cgroupRoot = "/sys/fs/cgroup"
	}
	if procRoot == "" {
		procRoot = "/proc"
	}
	c := &CgroupSampler{root: cgroupRoot, procRoot: procRoot, now: time.Now}
	if usec, err := c.cpuUsec(); err == nil {
		c.lastUsec, c.lastAt, c.primed = usec, c.now(), true
	}
	return c
}

// Sample implements HostSampler.
func (c *CgroupSampler) Sample() (HostStats, error) {
	var hs HostStats
	memTotal, err := c.memoryTotal()
	if err != nil {
		return hs, err
	}
	cur, err := c.readInt("memory.current")
	if err != nil {
		return hs, err
	}
	inactive := c.statValue("memory.stat", "inactive_file")
	hs.MemoryBytesTotal, hs.MemoryBytesUsed = memTotal, min(max(cur-inactive, 0), memTotal)

	total, err := c.cpuTotalMillis()
	if err != nil {
		return HostStats{}, err
	}
	used, err := c.cpuUsedMillis()
	if err != nil {
		return HostStats{}, err
	}
	hs.CPUMillisTotal, hs.CPUMillisUsed = total, min(used, total)
	return hs, nil
}

func (c *CgroupSampler) read(rel string) (string, error) {
	b, err := os.ReadFile(filepath.Join(c.root, rel))
	return strings.TrimSpace(string(b)), err
}

func (c *CgroupSampler) readInt(rel string) (int64, error) {
	s, err := c.read(rel)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", rel, err)
	}
	return n, nil
}

// statValue returns one "key value" line of a flat keyed file, 0 if absent.
func (c *CgroupSampler) statValue(rel, key string) int64 {
	s, err := c.read(rel)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(line, " "); ok && k == key {
			n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

func (c *CgroupSampler) memoryTotal() (int64, error) {
	s, err := c.read("memory.max")
	if err != nil {
		return 0, err
	}
	if s != "max" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("memory.max: unusable value %q", s)
		}
		return n, nil
	}
	b, err := os.ReadFile(filepath.Join(c.procRoot, "meminfo"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "MemTotal:"); ok {
			f := strings.Fields(rest)
			if len(f) >= 1 {
				if kb, err := strconv.ParseInt(f[0], 10, 64); err == nil && kb > 0 {
					return kb * 1024, nil
				}
			}
		}
	}
	return 0, errors.New("meminfo: MemTotal not found")
}

func (c *CgroupSampler) cpuTotalMillis() (int32, error) {
	s, err := c.read("cpu.max")
	if err != nil {
		return 0, err
	}
	quota, period, ok := strings.Cut(s, " ")
	if !ok {
		return 0, fmt.Errorf("cpu.max: unusable value %q", s)
	}
	if quota == "max" {
		return int32(runtime.NumCPU() * 1000), nil
	}
	q, err1 := strconv.ParseInt(quota, 10, 64)
	p, err2 := strconv.ParseInt(strings.TrimSpace(period), 10, 64)
	if err1 != nil || err2 != nil || q <= 0 || p <= 0 {
		return 0, fmt.Errorf("cpu.max: unusable value %q", s)
	}
	return int32(q * 1000 / p), nil
}

func (c *CgroupSampler) cpuUsec() (int64, error) {
	s, err := c.read("cpu.stat")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			return strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	return 0, errors.New("cpu.stat: usage_usec not found")
}

// cpuUsedMillis is the rate over the interval since the previous sample (a
// sample inside one second of the last reuses it).
func (c *CgroupSampler) cpuUsedMillis() (int32, error) {
	usec, err := c.cpuUsec()
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !c.primed {
		c.lastUsec, c.lastAt, c.primed = usec, now, true
		return 0, errWarmingUp
	}
	elapsed := now.Sub(c.lastAt)
	if elapsed < time.Second {
		if !c.measured {
			return 0, errWarmingUp
		}
		return c.lastUsed, nil
	}
	used := int32(max(usec-c.lastUsec, 0) * 1000 / elapsed.Microseconds())
	c.lastUsec, c.lastAt, c.lastUsed, c.measured = usec, now, used, true
	return used, nil
}
