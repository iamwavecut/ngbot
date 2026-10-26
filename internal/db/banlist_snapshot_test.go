package db

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

func randomSortedIDs(t testing.TB, n int) []int64 {
	t.Helper()
	random := rand.New(rand.NewPCG(1, 2))
	ids := make([]int64, n)
	next := int64(100_000_000)
	for i := range ids {
		next += 1 + random.Int64N(3_500)
		ids[i] = next
	}
	return ids
}

func TestBanlistCodecRoundTrip(t *testing.T) {
	t.Parallel()
	cases := map[string][]int64{
		"empty":    {},
		"single":   {42},
		"extremes": {math.MinInt64, -1004499987047, 0, 1, 6267307735356803662, math.MaxInt64},
		"random":   randomSortedIDs(t, 100_000),
	}
	for name, ids := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			payload, err := EncodeBanlistIDs(ids)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := DecodeBanlistIDs(payload, len(ids))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !slices.Equal(ids, got) {
				t.Fatalf("round trip mismatch: got %d IDs, want %d", len(got), len(ids))
			}
		})
	}
}

func TestEncodeBanlistIDsRejectsUnsortedInput(t *testing.T) {
	t.Parallel()
	for _, ids := range [][]int64{{5, 5}, {5, 4}} {
		if _, err := EncodeBanlistIDs(ids); !errors.Is(err, ErrCorruptBanlistPayload) {
			t.Fatalf("EncodeBanlistIDs(%v) error = %v, want ErrCorruptBanlistPayload", ids, err)
		}
	}
}

func TestDecodeBanlistIDsRejectsCorruptPayload(t *testing.T) {
	t.Parallel()
	payload, err := EncodeBanlistIDs([]int64{1, 2, 3})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	cases := map[string]struct {
		payload []byte
		count   int
	}{
		"count too high":   {payload, 4},
		"count too low":    {payload, 2},
		"truncated":        {payload[:len(payload)-1], 3},
		"zero delta":       {append(slices.Clone(payload[:1]), 0x00), 2},
		"trailing garbage": {append(slices.Clone(payload), 0x01), 3},
		"negative count":   {payload, -1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeBanlistIDs(tc.payload, tc.count); !errors.Is(err, ErrCorruptBanlistPayload) {
				t.Fatalf("error = %v, want ErrCorruptBanlistPayload", err)
			}
		})
	}
}

func TestNormalizeBanlistIDs(t *testing.T) {
	t.Parallel()
	if got := NormalizeBanlistIDs([]int64{3, 1, -5, 3, 1}); !slices.Equal(got, []int64{-5, 1, 3}) {
		t.Fatalf("NormalizeBanlistIDs = %v, want [-5 1 3]", got)
	}
}

func BenchmarkBanlistCodec5M(b *testing.B) {
	ids := randomSortedIDs(b, 5_000_000)
	var payload []byte
	for b.Loop() {
		var err error
		payload, err = EncodeBanlistIDs(ids)
		if err != nil {
			b.Fatalf("encode: %v", err)
		}
		if _, err := DecodeBanlistIDs(payload, len(ids)); err != nil {
			b.Fatalf("decode: %v", err)
		}
	}
	b.ReportMetric(float64(len(payload))/float64(len(ids)), "bytes/id")
}
