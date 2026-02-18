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
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(offset int64) {
			defer wg.Done()
			for i := int64(0); i < iterations; i++ {
				svc.markKnownBanned(offset*iterations + i)
			}
		}(int64(w + 1))
	}

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(offset int64) {
			defer wg.Done()
			for i := int64(0); i < iterations; i++ {
				_ = svc.IsKnownBanned(offset*iterations + i)
			}
		}(int64(r + 1))
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := int64(0); i < iterations; i++ {
			svc.setKnownBanned(map[int64]struct{}{i: {}})
		}
	}()

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
