package handlers

import (
	"maps"
	"slices"
)

type banlistIndex struct {
	sources map[string][]int64
}

func (i *banlistIndex) contains(userID int64) bool {
	if i == nil {
		return false
	}
	for _, ids := range i.sources {
		if _, found := slices.BinarySearch(ids, userID); found {
			return true
		}
	}
	return false
}

func (i *banlistIndex) with(source string, ids []int64) *banlistIndex {
	sources := make(map[string][]int64, 1)
	if i != nil {
		sources = maps.Clone(i.sources)
		if sources == nil {
			sources = make(map[string][]int64, 1)
		}
	}
	sources[source] = ids
	return &banlistIndex{sources: sources}
}

func (i *banlistIndex) count(source string) int {
	return len(i.ids(source))
}

func (i *banlistIndex) ids(source string) []int64 {
	if i == nil {
		return nil
	}
	return i.sources[source]
}
