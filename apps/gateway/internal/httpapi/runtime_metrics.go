package httpapi

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// processStart approximates process_start_time_seconds (package init runs at
// process start; /proc start-time needs btime arithmetic we do not need).
var processStart = time.Now()

// procClockTicks is USER_HZ. It is 100 on every mainstream Linux build.
// ponytail: hardcoded; read sysconf(_SC_CLK_TCK) via cgo/x/sys if a target differs.
const procClockTicks = 100

// writeRuntimeMetrics emits the standard go_* and process_* series under their
// conventional Prometheus names (not ubag_*) so stock dashboards and the
// alert rules that use process_resident_memory_bytes/process_cpu_seconds_total
// work against the gateway itself. process_* come from /proc and are simply
// omitted where it does not exist (Windows/macOS dev boxes). The body is
// cached for metricsCacheTTL by renderMetrics, which also bounds the brief
// stop-the-world cost of ReadMemStats.
func writeRuntimeMetrics(w io.Writer) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	writeSeries(w, "go_goroutines", "gauge", "Number of goroutines that currently exist.", float64(runtime.NumGoroutine()))
	writeSeries(w, "go_threads", "gauge", "Number of OS threads created.", float64(threadCount()))
	writeSeries(w, "go_memstats_heap_alloc_bytes", "gauge", "Bytes of allocated heap objects.", float64(ms.HeapAlloc))
	writeSeries(w, "go_memstats_heap_inuse_bytes", "gauge", "Bytes in in-use heap spans.", float64(ms.HeapInuse))
	writeSeries(w, "go_memstats_sys_bytes", "gauge", "Bytes of memory obtained from the OS.", float64(ms.Sys))
	writeSeries(w, "go_memstats_alloc_bytes_total", "counter", "Cumulative bytes allocated for heap objects.", float64(ms.TotalAlloc))
	writeSeries(w, "go_gc_cycles_total", "counter", "Completed GC cycles.", float64(ms.NumGC))
	writeSeries(w, "go_gc_pause_seconds_total", "counter", "Cumulative stop-the-world GC pause time.", float64(ms.PauseTotalNs)/1e9)
	writeSeries(w, "go_gc_last_pause_seconds", "gauge", "Most recent GC pause.", lastGCPause(&ms))
	writeSeries(w, "process_start_time_seconds", "gauge", "Start time of the process since unix epoch in seconds.", float64(processStart.UnixNano())/1e9)

	if cpu, ok := procCPUSeconds(); ok {
		writeSeries(w, "process_cpu_seconds_total", "counter", "Total user and system CPU time spent in seconds.", cpu)
	}
	if rss, ok := procRSSBytes(); ok {
		writeSeries(w, "process_resident_memory_bytes", "gauge", "Resident memory size in bytes.", rss)
	}
	if entries, err := os.ReadDir("/proc/self/fd"); err == nil {
		writeSeries(w, "process_open_fds", "gauge", "Number of open file descriptors.", float64(len(entries)))
	}
}

func writeSeries(w io.Writer, name, kind, help string, value float64) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n%s %s\n", name, help, name, kind, name, strconv.FormatFloat(value, 'g', -1, 64))
}

func lastGCPause(ms *runtime.MemStats) float64 {
	if ms.NumGC == 0 {
		return 0
	}
	return float64(ms.PauseNs[(ms.NumGC+255)%256]) / 1e9
}

// threadCount reports OS threads via the runtime's own profile (no /proc).
func threadCount() int {
	n, _ := runtime.ThreadCreateProfile(nil)
	return n
}

// procCPUSeconds parses utime+stime (fields 14/15) from /proc/self/stat. The
// comm field may contain spaces, so fields are counted after the last ')'.
func procCPUSeconds() (float64, bool) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	return parseProcStatCPU(string(raw))
}

func parseProcStatCPU(stat string) (float64, bool) {
	end := strings.LastIndexByte(stat, ')')
	if end < 0 {
		return 0, false
	}
	// After ')' the fields start at field 3 (state); utime/stime are 14/15.
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 13 {
		return 0, false
	}
	utime, err1 := strconv.ParseFloat(fields[11], 64)
	stime, err2 := strconv.ParseFloat(fields[12], 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return (utime + stime) / procClockTicks, true
}

// procRSSBytes reads resident pages (field 2) from /proc/self/statm.
func procRSSBytes() (float64, bool) {
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, false
	}
	return pages * float64(os.Getpagesize()), true
}
