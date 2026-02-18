package lifecycle

import (
	"context"
	"errors"
	"fmt"
)

type Component interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

type Runtime struct {
	components []Component
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
	started := make([]Component, 0, len(r.components))
	for _, component := range r.components {
		if component == nil {
			continue
		}
		if err := component.Start(ctx); err != nil {
			_ = stopComponents(ctx, started)
			return fmt.Errorf("start component: %w", err)
		}
		started = append(started, component)
	}
	return nil
}

func (r *Runtime) Stop(ctx context.Context) error {
	return stopComponents(ctx, r.components)
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
