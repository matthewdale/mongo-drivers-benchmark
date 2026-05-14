package metrics

import (
	"math"
	"sort"
	"time"
)

// Sample is the wall-clock duration of one HTTP round-trip.
type Sample time.Duration

// Result holds raw samples collected during a workload run.
type Result struct {
	Name         string
	Samples      []Sample
	DatasetBytes int64
	Concurrency  int
}

// Stats holds computed statistics derived from a Result.
type Stats struct {
	Count         int
	Min, Max      time.Duration
	P50, P75      time.Duration
	P90, P95, P99 time.Duration
	ThroughputMBs float64 // datasetBytes / p50_sec; 0 if datasetBytes == 0
	OpsPerSec     float64 // 1 / p50_sec
}

// Compute sorts r.Samples in place and returns derived statistics.
func Compute(r *Result) Stats {
	if len(r.Samples) == 0 {
		return Stats{}
	}
	sort.Slice(r.Samples, func(i, j int) bool { return r.Samples[i] < r.Samples[j] })

	s := Stats{
		Count: len(r.Samples),
		Min:   time.Duration(r.Samples[0]),
		Max:   time.Duration(r.Samples[len(r.Samples)-1]),
		P50:   pct(r.Samples, 50),
		P75:   pct(r.Samples, 75),
		P90:   pct(r.Samples, 90),
		P95:   pct(r.Samples, 95),
		P99:   pct(r.Samples, 99),
	}
	if p50sec := s.P50.Seconds(); p50sec > 0 {
		s.OpsPerSec = 1.0 / p50sec
		if r.DatasetBytes > 0 {
			s.ThroughputMBs = float64(r.DatasetBytes) / p50sec / 1e6
		}
	}
	return s
}

// pct returns the p-th percentile of a sorted slice using the nearest-rank
// method (same as the MongoDB driver benchmarking spec).
func pct(sorted []Sample, p float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	idx := int(math.Ceil(p/100.0*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return time.Duration(sorted[idx])
}
