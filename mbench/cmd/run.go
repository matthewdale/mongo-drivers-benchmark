package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/apiclient"
	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/metrics"
	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/output"
	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/runner"
	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/workload"
)

var outputPath string

var runCmd = &cobra.Command{
	Use:   "run <workload.yaml> [workload.yaml ...]",
	Short: "Run benchmark workloads and report metrics",
	Long: `Load one or more workload YAML files, execute them sequentially against
the target service, and print latency/throughput statistics. Optionally write
an Evergreen-format results.json with --output.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runBenchmark,
}

func init() {
	runCmd.Flags().StringVar(&outputPath, "output", "", "write Evergreen results JSON to this path")
}

func runBenchmark(_ *cobra.Command, args []string) error {
	c := apiclient.New(target)
	ctx := context.Background()

	h, err := c.Health(ctx)
	if err != nil {
		return fmt.Errorf("service not ready at %s: %w", target, err)
	}
	fmt.Printf("Connected: %s %s (%s %s)\n", h.Driver, h.DriverVersion, h.Language, h.LanguageVersion)

	var evResults output.EvergreenResults

	for _, path := range args {
		wl, err := workload.Load(path)
		if err != nil {
			return fmt.Errorf("loading %s: %w", path, err)
		}
		fmt.Printf("\nRunning: %s\n", wl.Name)

		result, err := runner.RunWorkload(ctx, c, wl)
		if err != nil {
			return fmt.Errorf("%s: %w", wl.Name, err)
		}

		stats := metrics.Compute(result)
		output.PrintResult(os.Stdout, wl.Name, stats, wl.Command, wl.Collection, wl.Run.Concurrency)
		evResults.Results = append(evResults.Results, output.BuildEvergreenResult(wl.Name, stats))
	}

	if outputPath != "" {
		if err := output.WriteEvergreenFile(outputPath, evResults); err != nil {
			return fmt.Errorf("writing output file: %w", err)
		}
		fmt.Printf("\nResults written to %s\n", outputPath)
	}

	return nil
}
