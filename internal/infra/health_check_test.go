package infra

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMonitorExecutableReturnsInitializationError(t *testing.T) {
	t.Parallel()

	changes, err := monitorExecutable(t.Context(), filepath.Join(t.TempDir(), "missing"), time.Millisecond)
	if err == nil {
		t.Fatal("expected initialization error")
	}
	if changes != nil {
		t.Fatal("expected nil change channel")
	}
}
