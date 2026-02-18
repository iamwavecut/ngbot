package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type testComponent struct {
	name      string
	startErr  error
	stopErr   error
	events    *[]string
	startCall int
	stopCall  int
}

func (c *testComponent) Start(ctx context.Context) error {
	_ = ctx
	c.startCall++
	if c.events != nil {
		*c.events = append(*c.events, "start:"+c.name)
	}
	return c.startErr
}

func (c *testComponent) Stop(ctx context.Context) error {
	_ = ctx
	c.stopCall++
	if c.events != nil {
		*c.events = append(*c.events, "stop:"+c.name)
	}
	return c.stopErr
}

func TestRuntimeStartStopOrder(t *testing.T) {
	t.Parallel()

	events := make([]string, 0, 6)
	c1 := &testComponent{name: "one", events: &events}
	c2 := &testComponent{name: "two", events: &events}
	c3 := &testComponent{name: "three", events: &events}

	runtime := NewRuntime(c1, c2, c3)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("start runtime: %v", err)
	}
	if err := runtime.Stop(context.Background()); err != nil {
		t.Fatalf("stop runtime: %v", err)
	}

	expected := []string{
		"start:one",
		"start:two",
		"start:three",
		"stop:three",
		"stop:two",
		"stop:one",
	}
	if !reflect.DeepEqual(events, expected) {
		t.Fatalf("unexpected order: got %v want %v", events, expected)
	}
}

func TestRuntimeStartFailureStopsStartedComponents(t *testing.T) {
	t.Parallel()

	events := make([]string, 0, 4)
	startErr := errors.New("boom")
	c1 := &testComponent{name: "one", events: &events}
	c2 := &testComponent{name: "two", events: &events, startErr: startErr}
	c3 := &testComponent{name: "three", events: &events}

	runtime := NewRuntime(c1, c2, c3)
	err := runtime.Start(context.Background())
	if err == nil {
		t.Fatalf("expected start error")
	}
	if !errors.Is(err, startErr) {
		t.Fatalf("unexpected start error: %v", err)
	}

	if c1.stopCall != 1 {
		t.Fatalf("expected started component to be stopped once, got %d", c1.stopCall)
	}
	if c2.stopCall != 0 || c3.stopCall != 0 {
		t.Fatalf("unexpected stop calls: c2=%d c3=%d", c2.stopCall, c3.stopCall)
	}

	expectedPrefix := []string{"start:one", "start:two", "stop:one"}
	if len(events) < len(expectedPrefix) || !reflect.DeepEqual(events[:len(expectedPrefix)], expectedPrefix) {
		t.Fatalf("unexpected events: %v", events)
	}
}
