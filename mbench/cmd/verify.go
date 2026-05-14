package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/apiclient"
	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/verifier"
)

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Run the conformance suite against a service",
	Long: `Verify that the service at --target correctly implements the benchmark
HTTP API spec (spec/http-api.md). Prints PASS/FAIL for each of the 17
conformance tests and exits non-zero if any test fails.`,
	RunE: runVerify,
}

func runVerify(_ *cobra.Command, _ []string) error {
	c := apiclient.New(target)
	ctx := context.Background()

	// Quick health probe before running the full suite.
	if _, err := c.Health(ctx); err != nil {
		return fmt.Errorf("service not ready at %s: %w", target, err)
	}

	v := verifier.New(c)
	passed, failed := v.Run(ctx, os.Stdout)

	fmt.Printf("\n%d passed, %d failed\n", passed, failed)
	if failed > 0 {
		return fmt.Errorf("%d conformance test(s) failed", failed)
	}
	return nil
}
