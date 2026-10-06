// Package nodes holds side-effect-free Helper Node placement logic (ADR-0006):
// eligibility, pressure hysteresis and the static ceiling table. Nothing here
// does I/O, reads a wall clock or recomputes the Fleet Manager's reservations;
// callers inject time and feed the manager's grant as-is.
package nodes

// NewHelperWorkloads is how many browser workloads a helper with no history
// may run before the caller ramps it up.
const NewHelperWorkloads = 1

// Ceiling is the UBAG-side defence-in-depth cap for a host. The manager owns
// the real limits; this only stops a bad grant from exceeding the table.
type Ceiling struct {
	CPUMillis   int
	MemoryBytes int64
}

// CeilingFor maps host size to the ceiling table: 2c/4G -> 1.5 CPU/2.5 GiB,
// 4c/8G -> 3 CPU/5 GiB, otherwise 75% of cores and 62.5% of memory (the two
// named rows are that same rule). Unknown host size fails closed to zero.
func CeilingFor(hostCores int, hostMemoryBytes int64) Ceiling {
	if hostCores <= 0 || hostMemoryBytes <= 0 {
		return Ceiling{}
	}
	return Ceiling{CPUMillis: hostCores * 750, MemoryBytes: hostMemoryBytes / 8 * 5}
}

// EffectiveCap is min(manager grant, ceiling). The grant is trusted as already
// net of manager reservations and is only ever clamped down, never adjusted.
func EffectiveCap(grantCPUMillis int, grantMemoryBytes int64, c Ceiling) (cpuMillis int, memoryBytes int64) {
	return min(max(grantCPUMillis, 0), c.CPUMillis), min(max(grantMemoryBytes, 0), c.MemoryBytes)
}
