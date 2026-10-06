package nodes

// Host-pressure telemetry source (ADR-0009): the helper agent reports its own
// cgroup budget use through ReportCapacity, and may add host-wide readings. The
// primary never measures a helper itself. The manager grant stays authoritative
// for ceilings; these readings only drive Pressure hysteresis.

// SampleFromCapacity turns a ReportCapacity cgroup view (used/total CPU
// millis and memory bytes) into a PressureSample. Unusable input (non-positive
// total, negative use) fails closed to a worst-case sample, so a bad or missing
// report can enter pressure but never recover from it.
func SampleFromCapacity(cpuTotal, cpuUsed int32, memTotal, memUsed int64) PressureSample {
	if cpuTotal <= 0 || cpuUsed < 0 || memTotal <= 0 || memUsed < 0 {
		return PressureSample{CPUPercent: 100, MemAvailPercent: 0}
	}
	return PressureSample{
		CPUPercent:      float64(cpuUsed) / float64(cpuTotal) * 100,
		MemAvailPercent: max(0, float64(memTotal-memUsed)) / float64(memTotal) * 100,
	}
}

// WorstSample combines the cgroup and host-wide readings: the higher CPU and the
// lower available memory win, so contention on either view reduces admission.
func WorstSample(a, b PressureSample) PressureSample {
	return PressureSample{CPUPercent: max(a.CPUPercent, b.CPUPercent), MemAvailPercent: min(a.MemAvailPercent, b.MemAvailPercent)}
}
