package util

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewPaging(t *testing.T) {
	tests := []struct {
		name       string
		size, page int
		want       Paging
	}{
		{"zero takes defaults", 0, 0, Paging{Size: DefaultPageSize, Page: 1}},
		{"negative size takes default", -5, 2, Paging{Size: DefaultPageSize, Page: 2}},
		{"size kept", 25, 3, Paging{Size: 25, Page: 3}},
		{"max kept", MaxPageSize, 1, Paging{Size: MaxPageSize, Page: 1}},
		{"above max capped", MaxPageSize + 1, 1, Paging{Size: MaxPageSize, Page: 1}},
		{"negative page is first", 10, -1, Paging{Size: 10, Page: 1}},
		{"huge page is capped", 10, math.MaxInt, Paging{Size: 10, Page: maxPage}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, NewPaging(tt.size, tt.page))
		})
	}
}

// A huge page must never make Offset's multiplication overflow uint64, however large Size is.
func TestNewPagingOffsetNeverOverflows(t *testing.T) {
	p := NewPaging(MaxPageSize, math.MaxInt)

	offset := p.Offset()

	assert.Less(t, offset, uint64(math.MaxInt64))
	assert.Equal(t, uint64(maxPage-1)*uint64(MaxPageSize), offset)
}

func TestPagingLimitOffset(t *testing.T) {
	first := Paging{Size: 10, Page: 1}
	third := Paging{Size: 10, Page: 3}

	assert.Equal(t, uint64(11), first.Limit())
	assert.Equal(t, uint64(0), first.Offset())
	assert.Equal(t, uint64(20), third.Offset())
}

func TestResolvePaging(t *testing.T) {
	p := Paging{Size: 2, Page: 1}

	tests := []struct {
		name     string
		items    []int
		want     []int
		wantNext bool
	}{
		{"fewer than size", []int{1}, []int{1}, false},
		{"exactly size", []int{1, 2}, []int{1, 2}, false},
		{"lookahead row trimmed", []int{1, 2, 3}, []int{1, 2}, true},
		{"empty", nil, nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, next := ResolvePaging(p, tt.items)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantNext, next)
		})
	}
}
