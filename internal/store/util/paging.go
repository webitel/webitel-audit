package util

import "math"

const (
	DefaultPageSize = 10 //applies when the request has no positive size.

	MaxPageSize = 5000 // caps the requested size (matches webitel-kb).

	// maxPage keeps Page*Size within range for the largest possible Size (MaxPageSize), so
	// Offset's multiplication never overflows, however large a caller-supplied page is.
	maxPage = math.MaxInt / MaxPageSize
)

// Paging is a page request after defaults and the cap.
type Paging struct {
	Size int // rows per page, 1..MaxPageSize
	Page int // 1-based
}

func NewPaging(size, page int) Paging {
	if size <= 0 {
		size = DefaultPageSize
	}

	return Paging{Size: min(size, MaxPageSize), Page: min(max(page, 1), maxPage)}
}

// Limit is one row more than Size: the extra row tells ResolvePaging that a next page exists.
func (p Paging) Limit() uint64 { return uint64(p.Size) + 1 }

// Offset skips the rows of the previous pages.
func (p Paging) Offset() uint64 { return uint64(p.Page-1) * uint64(p.Size) }

// ResolvePaging drops the lookahead row fetched by Limit and reports whether a next page exists.
func ResolvePaging[T any](p Paging, items []T) ([]T, bool) {
	if len(items) > p.Size {
		return items[:p.Size], true
	}

	return items, false
}
