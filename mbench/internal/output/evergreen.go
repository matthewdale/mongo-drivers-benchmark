package output

import (
	"encoding/json"
	"os"
	"time"

	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/metrics"
)

// EvergreenResults is the top-level JSON object written to the output file.
// The schema is designed to match the Evergreen performance plugin's
// results.json format; exact field names may need adjustment once an
// Evergreen project config is in place.
type EvergreenResults struct {
	Results []EvergreenResult `json:"results"`
}

// EvergreenResult holds the statistics for one workload.
type EvergreenResult struct {
	Name    string                     `json:"name"`
	Results map[string]EvergreenMetric `json:"results"`
}

// EvergreenMetric is a single named measurement.
type EvergreenMetric struct {
	Value float64 `json:"value"`
}

// BuildEvergreenResult converts a Stats value into the Evergreen result shape.
func BuildEvergreenResult(name string, stats metrics.Stats) EvergreenResult {
	r := EvergreenResult{
		Name: name,
		Results: map[string]EvergreenMetric{
			"latency_p50_ms": {Value: msec(stats.P50)},
			"latency_p75_ms": {Value: msec(stats.P75)},
			"latency_p90_ms": {Value: msec(stats.P90)},
			"latency_p95_ms": {Value: msec(stats.P95)},
			"latency_p99_ms": {Value: msec(stats.P99)},
			"ops_per_sec":    {Value: stats.OpsPerSec},
		},
	}
	if stats.ThroughputMBs > 0 {
		r.Results["throughput_mbs"] = EvergreenMetric{Value: stats.ThroughputMBs}
	}
	return r
}

// WriteEvergreenFile serialises results to path as indented JSON.
func WriteEvergreenFile(path string, results EvergreenResults) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func msec(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}
