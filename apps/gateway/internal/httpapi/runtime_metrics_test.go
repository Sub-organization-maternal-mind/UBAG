package httpapi

import (
	"bytes"
	"strings"
	"testing"
)

func TestRuntimeMetricsEmitGoSeries(t *testing.T) {
	var buf bytes.Buffer
	writeRuntimeMetrics(&buf)
	out := buf.String()
	for _, want := range []string{
		"go_goroutines ", "go_threads ", "go_memstats_heap_alloc_bytes ",
		"go_gc_pause_seconds_total ", "process_start_time_seconds ",
		"# TYPE go_goroutines gauge",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("runtime metrics missing %q:\n%s", want, out)
		}
	}
}

func TestParseProcStatCPUHandlesSpacesInComm(t *testing.T) {
	// utime=250 stime=50 ticks (fields 14/15), comm contains spaces and ')'.
	stat := "42 (we ird) name) S 1 2 3 4 5 6 7 8 9 10 250 50 0 0 20 0 1 0 1 2 3"
	got, ok := parseProcStatCPU(stat)
	if !ok || got != 3.0 {
		t.Fatalf("parseProcStatCPU = %v, %v; want 3, true", got, ok)
	}
}
