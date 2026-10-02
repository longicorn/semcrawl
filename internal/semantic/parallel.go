package semantic

import (
	"context"
	"sync"

	"github.com/longicorn/semcrawl/jev"
)

type evaluationResult struct {
	response *jev.Response
	err      error
}

// evaluateParallel runs independent Jev requests with a small fixed worker
// pool. Results retain input order so callers can aggregate deterministically.
func evaluateParallel(ctx context.Context, evaluator Evaluator, concurrency int, requests []jev.Request) []evaluationResult {
	results := make([]evaluationResult, len(requests))
	jobs := make(chan int)
	workers := min(concurrency, len(requests))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				results[index].response, results[index].err = evaluator.Evaluate(ctx, requests[index])
			}
		}()
	}
	for index := range requests {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	return results
}
