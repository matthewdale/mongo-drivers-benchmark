package runner

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/apiclient"
	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/metrics"
	"github.com/mongodb/mongo-drivers-benchmark/mbench/internal/workload"
)

// RunWorkload executes a workload's setup steps once, then runs the timed
// command across a pool of concurrent goroutines until both the minimum
// iteration count and minimum duration have been satisfied.
func RunWorkload(ctx context.Context, c *apiclient.Client, wl *workload.Workload) (*metrics.Result, error) {
	// Build the timed request body once (same payload each iteration).
	timedBody, err := workload.BuildBody(wl)
	if err != nil {
		return nil, fmt.Errorf("building request body: %w", err)
	}

	// Run one-time setup steps.
	for i, step := range wl.Setup {
		stepBody, err := workload.BuildStepBody(&step, wl)
		if err != nil {
			return nil, fmt.Errorf("building setup[%d] body: %w", i, err)
		}
		_, status, err := c.Command(ctx, step.Command, stepBody)
		if err != nil {
			return nil, fmt.Errorf("setup[%d] (%s): %w", i, step.Command, err)
		}
		if status >= 400 {
			return nil, fmt.Errorf("setup[%d] (%s): HTTP %d", i, step.Command, status)
		}
	}

	// Prepare per-iteration setup bodies upfront.
	perIterBodies := make([][]byte, len(wl.SetupPerIter))
	for i, step := range wl.SetupPerIter {
		b, err := workload.BuildStepBody(&step, wl)
		if err != nil {
			return nil, fmt.Errorf("building setupPerIteration[%d] body: %w", i, err)
		}
		perIterBodies[i] = b
	}

	// Stopping condition: BOTH minIterations reached AND minDuration elapsed.
	minIter := int64(wl.Run.Iterations)
	deadline := time.Now().Add(time.Duration(wl.Run.MinDurationSecs) * time.Second)
	concurrency := wl.Run.Concurrency

	var (
		count    atomic.Int64
		sampleCh = make(chan metrics.Sample, concurrency*256)
		errCh    = make(chan error, concurrency)
	)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				// Check stopping condition before starting a new iteration.
				if count.Load() >= minIter && time.Now().After(deadline) {
					return
				}

				// Per-iteration setup (not timed).
				for j, step := range wl.SetupPerIter {
					_, status, err := c.Command(ctx, step.Command, perIterBodies[j])
					if err != nil {
						errCh <- fmt.Errorf("setupPerIteration[%d] (%s): %w", j, step.Command, err)
						cancel()
						return
					}
					if status >= 400 {
						errCh <- fmt.Errorf("setupPerIteration[%d] (%s): HTTP %d", j, step.Command, status)
						cancel()
						return
					}
				}

				// Timed command. Exec drains the response body without buffering
				// it, saving an allocation per iteration on the hot path.
				start := time.Now()
				status, err := c.Exec(ctx, wl.Command, timedBody)
				elapsed := time.Since(start)
				if err != nil {
					// Context cancellation from a sibling error is expected; ignore it.
					if ctx.Err() != nil {
						return
					}
					errCh <- fmt.Errorf("%s: %w", wl.Command, err)
					cancel()
					return
				}
				if status >= 400 {
					errCh <- fmt.Errorf("%s: HTTP %d", wl.Command, status)
					cancel()
					return
				}

				sampleCh <- metrics.Sample(elapsed)
				count.Add(1)
			}
		}()
	}

	// Close sampleCh once all workers have exited.
	go func() {
		wg.Wait()
		close(sampleCh)
	}()

	// Collect samples until the channel is closed.
	var samples []metrics.Sample
	for s := range sampleCh {
		samples = append(samples, s)
	}

	// Surface the first worker error, if any.
	select {
	case err := <-errCh:
		return nil, err
	default:
	}

	return &metrics.Result{
		Name:         wl.Name,
		Samples:      samples,
		DatasetBytes: wl.DatasetBytes,
		Concurrency:  concurrency,
	}, nil
}
