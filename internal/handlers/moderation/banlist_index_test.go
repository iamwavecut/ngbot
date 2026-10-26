package handlers

import "testing"

func TestBanlistIndexContainsAcrossSources(t *testing.T) {
	t.Parallel()

	index := &banlistIndex{sources: map[string][]int64{"a": {1, 5}, "b": {-3}}}
	for _, tc := range []struct {
		userID int64
		want   bool
	}{
		{userID: 5, want: true},
		{userID: -3, want: true},
		{userID: 2, want: false},
	} {
		if got := index.contains(tc.userID); got != tc.want {
			t.Fatalf("contains(%d) = %v, want %v", tc.userID, got, tc.want)
		}
	}

	replaced := index.with("a", []int64{2})
	if !replaced.contains(2) || replaced.contains(5) || !replaced.contains(-3) {
		t.Fatalf("replaced index has wrong membership: %+v", replaced.sources)
	}
	if index.contains(2) || !index.contains(5) {
		t.Fatalf("original index changed: %+v", index.sources)
	}
	if replaced.count("a") != 1 || index.count("a") != 2 || index.count("missing") != 0 {
		t.Fatalf("counts = %d/%d/%d, want 1/2/0", replaced.count("a"), index.count("a"), index.count("missing"))
	}

	var empty *banlistIndex
	if empty.contains(1) || empty.count("a") != 0 || !empty.with("a", []int64{1}).contains(1) {
		t.Fatal("nil index must behave as empty")
	}
}
