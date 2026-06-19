package queue

import (
	"context"
	"sync"
	"time"
)

type memoryJob struct {
	requestID string
	attempts  int
	max       int
}

type MemoryBackend struct {
	mu          sync.Mutex
	maxAttempts int
	jobs        map[string]*memoryJob
	queue       chan string
	nextID      int64
}

func NewMemoryBackend(buffer, maxAttempts int) *MemoryBackend {
	if maxAttempts < 1 {
		maxAttempts = 5
	}
	if buffer < 1 {
		buffer = 32
	}
	return &MemoryBackend{
		maxAttempts: maxAttempts,
		jobs:        map[string]*memoryJob{},
		queue:       make(chan string, buffer),
	}
}

func (m *MemoryBackend) Enqueue(ctx context.Context, requestID string) error {
	m.mu.Lock()
	if _, ok := m.jobs[requestID]; !ok {
		m.jobs[requestID] = &memoryJob{
			requestID: requestID,
			max:       m.maxAttempts,
		}
	}
	m.mu.Unlock()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case m.queue <- requestID:
		return nil
	default:
		return ErrQueueFull
	}
}

func (m *MemoryBackend) Claim(ctx context.Context, _ string, _ time.Duration) (Job, error) {
	select {
	case <-ctx.Done():
		return Job{}, ctx.Err()
	case requestID := <-m.queue:
		m.mu.Lock()
		defer m.mu.Unlock()
		job := m.jobs[requestID]
		if job == nil {
			return Job{}, ErrNoJob
		}
		job.attempts++
		m.nextID++
		return Job{
			ID:          m.nextID,
			RequestID:   requestID,
			Attempts:    job.attempts,
			MaxAttempts: job.max,
		}, nil
	default:
		return Job{}, ErrNoJob
	}
}

func (m *MemoryBackend) Complete(_ context.Context, job Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, job.RequestID)
	return nil
}

func (m *MemoryBackend) Retry(ctx context.Context, job Job, _ error, backoff time.Duration, terminal bool) error {
	if terminal {
		return m.Complete(ctx, job)
	}
	time.AfterFunc(backoff, func() {
		_ = m.Enqueue(context.Background(), job.RequestID)
	})
	return nil
}

func (m *MemoryBackend) Ready(context.Context) error {
	return nil
}
