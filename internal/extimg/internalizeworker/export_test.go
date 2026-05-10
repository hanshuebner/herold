package internalizeworker

import "context"

// RunBatchForTest exposes runBatch to tests in the
// internalizeworker_test package. Returns true when the batch was
// empty (the same contract as runBatch).
//
// Test-only surface; not part of the public API.
func (w *Worker) RunBatchForTest(ctx context.Context) bool {
	return w.runBatch(ctx)
}
