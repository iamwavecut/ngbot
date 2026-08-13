package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

type Component interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

type Runtime struct {
	components []Component
	ready      atomic.Bool
}

func NewRuntime(components ...Component) *Runtime {
	return &Runtime{components: components}
}

func (r *Runtime) Register(component Component) {
	if component == nil {
		return
	}
	r.components = append(r.components, component)
}

func (r *Runtime) Start(ctx context.Context) error {
	r.ready.Store(false)
	started := make([]Component, 0, len(r.components))
	for _, component := range r.components {
		if component == nil {
			continue
		}
		if err := component.Start(ctx); err != nil {
			startErr := fmt.Errorf("start component: %w", err)
			return errors.Join(startErr, stopComponents(ctx, started))
		}
		started = append(started, component)
	}
	r.ready.Store(true)
	return nil
}

func (r *Runtime) Stop(ctx context.Context) error {
	r.ready.Store(false)
	return stopComponents(ctx, r.components)
}

func (r *Runtime) Ready() bool {
	return r.ready.Load()
}

func stopComponents(ctx context.Context, components []Component) error {
	var stopErr error
	for i := len(components) - 1; i >= 0; i-- {
		component := components[i]
		if component == nil {
			continue
		}
		if err := component.Stop(ctx); err != nil {
			stopErr = errors.Join(stopErr, fmt.Errorf("stop component: %w", err))
		}
	}
	return stopErr
}
