package cmd

import "github.com/spf13/cobra"

var target string

var rootCmd = &cobra.Command{
	Use:   "mbench",
	Short: "MongoDB driver benchmark runner and spec verifier",
	Long: `mbench drives benchmark workloads against a MongoDB driver benchmark service
and verifies that the service correctly implements the HTTP API spec.

Subcommands:
  run     Run one or more workload files and report latency/throughput metrics.
  verify  Run the conformance suite against a service.`,
}

// Execute runs the root command.
func Execute() error { return rootCmd.Execute() }

func init() {
	rootCmd.PersistentFlags().StringVar(&target, "target", "http://localhost:8080",
		"base URL of the benchmark service")
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(verifyCmd)
}
