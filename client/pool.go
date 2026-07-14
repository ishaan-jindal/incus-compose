package client

import (
	"errors"
	"sync"
)

// PoolRunArgs contains options for WorkerPool.Run.
type PoolRunArgs struct {
	// FailFast stops processing on first error (default: true).
	FailFast bool
}

// WorkerPool executes tasks concurrently with a limited number of workers.
type WorkerPool struct {
	workers int
	tasks   []func() error
	mu      sync.Mutex
}

// NewWorkerPool creates a new WorkerPool with the specified number of workers.
func NewWorkerPool(workers int) *WorkerPool {
	if workers < 1 {
		workers = 1
	}
	return &WorkerPool{
		workers: workers,
		tasks:   []func() error{},
	}
}

// Submit adds a task to the pool.
func (p *WorkerPool) Submit(fn func() error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tasks = append(p.tasks, fn)
}

// Run executes all submitted tasks using the worker pool.
// With FailFast it returns the first error without waiting for the remaining
// tasks; otherwise it waits for all tasks and returns their aggregated errors.
func (p *WorkerPool) Run(args PoolRunArgs) error {
	p.mu.Lock()
	tasks := make([]func() error, len(p.tasks))
	copy(tasks, p.tasks)
	p.tasks = nil // Clear for reuse
	p.mu.Unlock()

	if len(tasks) == 0 {
		return nil
	}

	// Channel for distributing work
	taskCh := make(chan func() error, len(tasks))
	for _, task := range tasks {
		taskCh <- task
	}
	close(taskCh)

	// Channel for collecting errors
	errCh := make(chan error, len(tasks))

	// Cancel channel for fail-fast
	cancelCh := make(chan struct{})
	var cancelOnce sync.Once

	// Start workers
	var wg sync.WaitGroup
	for i := 0; i < p.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range taskCh {
				// Check if canceled
				select {
				case <-cancelCh:
					return
				default:
				}

				if err := task(); err != nil {
					errCh <- err
					if args.FailFast {
						cancelOnce.Do(func() { close(cancelCh) })
						return
					}
				}
			}
		}()
	}

	// Close the error channel once every worker has exited.
	go func() {
		wg.Wait()
		close(errCh)
	}()

	if args.FailFast {
		// Fail on the first error without waiting for still-running tasks.
		// A closed channel with no value yields a nil error (success).
		return <-errCh
	}

	// Collect and aggregate errors from all tasks.
	var errs error
	for err := range errCh {
		errs = errors.Join(errs, err)
	}

	return errs
}
