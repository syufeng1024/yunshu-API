package mw

import (
	"math"
	"net/http"
	"strconv"
	"strings"

	"yunshu.dev/core/internal/apierr"
)

// Pagination limits (T11 hard constraint 11). page_size is capped, never
// trusted: an unbounded list is an unbounded export.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Page is the normalized pagination input shared by every control list
// endpoint.
type Page struct {
	Number int
	Size   int
	Sort   string
	Order  string
}

// Offset returns the SQL OFFSET for the page.
func (p Page) Offset() int { return (p.Number - 1) * p.Size }

// ParsePage reads page/page_size/sort/order. sort must be one of allowedSort
// when it is supplied; an unknown column is rejected rather than silently
// ignored, because ignoring it would make the order unpredictable.
func ParsePage(r *http.Request, allowedSort ...string) (Page, *apierr.APIError) {
	q := r.URL.Query()
	p := Page{Number: 1, Size: DefaultPageSize, Order: "desc"}

	if raw := strings.TrimSpace(q.Get("page")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return Page{}, InvalidQueryField("page")
		}
		p.Number = n
	}
	if raw := strings.TrimSpace(q.Get("page_size")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return Page{}, InvalidQueryField("page_size")
		}
		if n > MaxPageSize {
			return Page{}, InvalidQueryField("page_size")
		}
		p.Size = n
	}
	if raw := strings.TrimSpace(q.Get("sort")); raw != "" {
		ok := false
		for _, name := range allowedSort {
			if raw == name {
				ok = true
				break
			}
		}
		if !ok {
			return Page{}, InvalidQueryField("sort")
		}
		p.Sort = raw
	}
	if raw := strings.ToLower(strings.TrimSpace(q.Get("order"))); raw != "" {
		if raw != "asc" && raw != "desc" {
			return Page{}, InvalidQueryField("order")
		}
		p.Order = raw
	}
	return p, nil
}

// clampInt64 converts a possibly-large OFFSET into an int for database/sql.
func clampInt64(v int) int64 {
	if v < 0 {
		return 0
	}
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int64(v)
}

// OffsetInt64 is Offset in the type database/sql expects.
func (p Page) OffsetInt64() int64 { return clampInt64(p.Offset()) }
