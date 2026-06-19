package store

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/mcmoney/platform-control-plane/internal/domain"
)

var ErrNotFound = errors.New("resource not found")

type Repository interface {
	Close() error
	UpsertClasses(context.Context, []domain.EnvironmentClass) error
	ListClasses(context.Context) ([]domain.EnvironmentClass, error)
	GetClass(context.Context, string) (domain.EnvironmentClass, error)
	CreateRequest(context.Context, domain.EnvironmentRequest) (domain.EnvironmentRequest, error)
	UpdateRequest(context.Context, domain.EnvironmentRequest) (domain.EnvironmentRequest, error)
	GetRequest(context.Context, string) (domain.EnvironmentRequest, error)
	ListRequests(context.Context) ([]domain.EnvironmentRequest, error)
}

type MemoryRepository struct {
	mu       sync.RWMutex
	classes  map[string]domain.EnvironmentClass
	requests map[string]domain.EnvironmentRequest
}

func NewMemoryRepository(classes []domain.EnvironmentClass) *MemoryRepository {
	classMap := make(map[string]domain.EnvironmentClass, len(classes))
	for _, class := range classes {
		classMap[class.Name] = class
	}

	return &MemoryRepository{
		classes:  classMap,
		requests: map[string]domain.EnvironmentRequest{},
	}
}

func (r *MemoryRepository) Close() error {
	return nil
}

func (r *MemoryRepository) UpsertClasses(_ context.Context, classes []domain.EnvironmentClass) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, class := range classes {
		r.classes[class.Name] = class
	}

	return nil
}

func (r *MemoryRepository) ListClasses(context.Context) ([]domain.EnvironmentClass, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	items := make([]domain.EnvironmentClass, 0, len(r.classes))
	for _, class := range r.classes {
		items = append(items, class)
	}
	slices.SortFunc(items, func(a, b domain.EnvironmentClass) int {
		switch {
		case a.Name < b.Name:
			return -1
		case a.Name > b.Name:
			return 1
		default:
			return 0
		}
	})

	return items, nil
}

func (r *MemoryRepository) GetClass(_ context.Context, name string) (domain.EnvironmentClass, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	class, ok := r.classes[name]
	if !ok {
		return domain.EnvironmentClass{}, ErrNotFound
	}

	return class, nil
}

func (r *MemoryRepository) CreateRequest(_ context.Context, req domain.EnvironmentRequest) (domain.EnvironmentRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	req.Version = 1
	r.requests[req.ID] = req
	return req, nil
}

func (r *MemoryRepository) UpdateRequest(_ context.Context, req domain.EnvironmentRequest) (domain.EnvironmentRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.requests[req.ID]; !ok {
		return domain.EnvironmentRequest{}, ErrNotFound
	}

	req.Version++
	r.requests[req.ID] = req
	return req, nil
}

func (r *MemoryRepository) GetRequest(_ context.Context, id string) (domain.EnvironmentRequest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	req, ok := r.requests[id]
	if !ok {
		return domain.EnvironmentRequest{}, ErrNotFound
	}

	return req, nil
}

func (r *MemoryRepository) ListRequests(context.Context) ([]domain.EnvironmentRequest, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	items := make([]domain.EnvironmentRequest, 0, len(r.requests))
	for _, req := range r.requests {
		items = append(items, req)
	}
	slices.SortFunc(items, func(a, b domain.EnvironmentRequest) int {
		switch {
		case a.CreatedAt.Before(b.CreatedAt):
			return -1
		case a.CreatedAt.After(b.CreatedAt):
			return 1
		default:
			return 0
		}
	})

	return items, nil
}
