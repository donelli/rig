package engine

import "context"

// semaphore is a minimal counting semaphore (buffered-channel based) used to
// cap how many services may be initializing/building at once, mirroring
// Tilt's max_parallel_updates. Kept dependency-free on purpose.
type semaphore chan struct{}

func newSemaphore(n int) semaphore {
	if n <= 0 {
		n = 1
	}
	return make(semaphore, n)
}

func (s semaphore) Acquire(ctx context.Context) error {
	select {
	case s <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s semaphore) Release() {
	select {
	case <-s:
	default:
	}
}
