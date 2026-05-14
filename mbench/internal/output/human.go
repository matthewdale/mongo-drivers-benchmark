package output

import (
	"fmt"
	"io"
	"time"

	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/metrics"
)

// PrintResult writes a human-readable summary of one workload's statistics to w.
func PrintResult(w io.Writer, name string, stats metrics.Stats, command, collection string, concurrency int) {
	coll := collection
	if coll == "" {
		coll = "(multi-namespace)"
	}
	fmt.Fprintf(w, "\n=== %s ===\n", name)
	fmt.Fprintf(w, "  %s · %s · %d iterations · %d goroutine(s)\n",
		command, coll, stats.Count, concurrency)
	fmt.Fprintf(w, "  Min   %-10s  Max  %s\n", fmtDur(stats.Min), fmtDur(stats.Max))
	fmt.Fprintf(w, "  P50   %-10s  P75  %s\n", fmtDur(stats.P50), fmtDur(stats.P75))
	fmt.Fprintf(w, "  P90   %-10s  P95  %-10s  P99  %s\n",
		fmtDur(stats.P90), fmtDur(stats.P95), fmtDur(stats.P99))
	if stats.ThroughputMBs > 0 {
		fmt.Fprintf(w, "  Throughput  %.2f MB/s   Ops/sec  %.0f\n",
			stats.ThroughputMBs, stats.OpsPerSec)
	} else {
		fmt.Fprintf(w, "  Ops/sec  %.0f\n", stats.OpsPerSec)
	}
}

func fmtDur(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%.2fµs", float64(d)/float64(time.Microsecond))
	}
	return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
}
