package handlers

import (
	"sync"
	"testing"
)

func TestKnownBannedConcurrentAccess(t *testing.T) {
	t.Parallel()

	svc, ok := NewBanService(nil, nil).(*defaultBanService)
	if !ok {
		t.Fatalf("unexpected service type")
	}

	const (
		writers    = 8
		readers    = 8
		iterations = 2000
	)

	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(offset int64) {
			defer wg.Done()
			for i := range int64(iterations) {
				svc.markKnownBanned(offset*iterations + i)
			}
		}(int64(w + 1))
	}

	for r := range readers {
		wg.Add(1)
		go func(offset int64) {
			defer wg.Done()
			for i := range int64(iterations) {
				_ = svc.IsKnownBanned(offset*iterations + i)
			}
		}(int64(r + 1))
	}

	wg.Go(func() {
		for i := range int64(iterations) {
			svc.setKnownBanned(map[int64]struct{}{i: {}})
		}
	})

	wg.Wait()

	svc.markKnownBanned(42)
	if !svc.IsKnownBanned(42) {
		t.Fatalf("expected marked user to be known banned")
	}
}

func TestSetKnownBannedCopiesInput(t *testing.T) {
	t.Parallel()

	svc, ok := NewBanService(nil, nil).(*defaultBanService)
	if !ok {
		t.Fatalf("unexpected service type")
	}

	input := map[int64]struct{}{1: {}}
	svc.setKnownBanned(input)
	input[2] = struct{}{}

	if svc.IsKnownBanned(2) {
		t.Fatalf("service map should be isolated from caller map")
	}
}
