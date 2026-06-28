package http

import (
	"net/http/httptest"
	"testing"

	"github.com/zaentrum/katalog-api/internal/store"
)

// parseListOpts is the surface chino-api hits via every browse URL; the
// shape needs to be defensive against malformed query params (browse
// chips can wedge a chino-web reload into a bad URL) without ever 400-ing
// the request.
func TestParseListOpts_GoodValues(t *testing.T) {
	r := httptest.NewRequest("GET",
		"/api/v1/movies?q=blade&year_min=1980&year_max=2000&rating_min=7.5&genre=Sci-Fi&sort=year&limit=25&offset=50",
		nil)
	o := parseListOpts(r)
	if o.Query != "blade" {
		t.Errorf("Query = %q, want blade", o.Query)
	}
	if o.YearMin == nil || *o.YearMin != 1980 {
		t.Errorf("YearMin = %v, want 1980", o.YearMin)
	}
	if o.YearMax == nil || *o.YearMax != 2000 {
		t.Errorf("YearMax = %v, want 2000", o.YearMax)
	}
	if o.RatingMin == nil || *o.RatingMin != 7.5 {
		t.Errorf("RatingMin = %v, want 7.5", o.RatingMin)
	}
	if o.Genre != "Sci-Fi" {
		t.Errorf("Genre = %q, want Sci-Fi", o.Genre)
	}
	if o.Sort != "year" {
		t.Errorf("Sort = %q, want year", o.Sort)
	}
	if o.Limit != 25 {
		t.Errorf("Limit = %d, want 25", o.Limit)
	}
	if o.Offset != 50 {
		t.Errorf("Offset = %d, want 50", o.Offset)
	}
}

func TestParseListOpts_BadValuesSilentlyDropped(t *testing.T) {
	// Every field has a malformed value that should NOT trip a 400 —
	// the store layer falls back to default Limit/Offset and nil
	// pointer for unparseable filters. This is the "browse chips
	// wedged into a bad URL" defence.
	r := httptest.NewRequest("GET",
		"/api/v1/movies?year_min=banana&year_max=999&rating_min=11&limit=oops",
		nil)
	o := parseListOpts(r)
	if o.YearMin != nil {
		t.Errorf("YearMin = %v, want nil for 'banana'", o.YearMin)
	}
	if o.YearMax != nil {
		t.Errorf("YearMax = %v, want nil for out-of-range 999", o.YearMax)
	}
	if o.RatingMin != nil {
		t.Errorf("RatingMin = %v, want nil for out-of-range 11", o.RatingMin)
	}
	if o.Limit != 0 {
		// store.ListItems clamps to default 50 when Limit is 0.
		// parseListOpts itself should NOT pre-clamp; we want
		// "unset" to be visible at the store layer.
		t.Errorf("Limit = %d, want 0 for 'oops' (let store clamp)", o.Limit)
	}
}

func TestParseInclude(t *testing.T) {
	cases := []struct {
		raw  string
		want store.IncludeOpts
	}{
		{"", store.IncludeOpts{}},
		{"genres", store.IncludeOpts{Genres: true}},
		// `cast` is the alias for `people` since chino-api thinks in
		// "cast" but the underlying table is `itempeople`.
		{"cast", store.IncludeOpts{People: true}},
		{"GENRES,people,segments", store.IncludeOpts{Genres: true, People: true, Segments: true}},
		// Whitespace + unknown tokens are tolerated.
		{" genres , bogus , trailers ", store.IncludeOpts{Genres: true, Trailers: true}},
		{"genres,people,subtitles,trailers,segments", store.IncludeOpts{
			Genres: true, People: true, Subtitles: true, Trailers: true, Segments: true,
		}},
	}
	for _, c := range cases {
		got := store.ParseInclude(c.raw)
		if got != c.want {
			t.Errorf("ParseInclude(%q) = %+v, want %+v", c.raw, got, c.want)
		}
	}
}
