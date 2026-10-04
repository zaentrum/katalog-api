package http

import (
	"net/http"
	"regexp"
	"strconv"

	"github.com/zaentrum/katalog-api/internal/auth"
	"github.com/zaentrum/katalog-api/internal/store"
)

// maxRatingParam is the query parameter a BFF passes its viewer's rating cap
// in, a whole number of years; chino-api passes it on every catalog request.
const maxRatingParam = "max_rating"

var wholeYears = regexp.MustCompile(`^[0-9]{1,9}$`)

// viewerCap is the rating cap a request is served at: the stricter of the
// verified bearer's max_rating claim (auth.MaxRating) and every max_rating
// parameter of it. A parameter that is no whole number of years (empty,
// signed, a fraction, a word) is the strictest cap, 0. ok is false when
// neither caps it: the request is served as before.
func viewerCap(r *http.Request) (age int, ok bool) {
	age, ok = auth.MaxRating(r.Context())
	for _, v := range r.URL.Query()[maxRatingParam] {
		n := 0
		if wholeYears.MatchString(v) {
			n, _ = strconv.Atoi(v)
		}
		if !ok || n < age {
			age, ok = n, true
		}
	}
	return age, ok
}

// capped puts the request's rating cap (viewerCap) on its context, so that
// every read of the store it makes leaves out the titles the cap does not
// allow, as if the catalog did not hold them (store.WithMaxAge): a list does
// not count them, and an item by id is 404, as one there is not.
func capped(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if age, ok := viewerCap(r); ok {
			r = r.WithContext(store.WithMaxAge(r.Context(), age))
		}
		next.ServeHTTP(w, r)
	})
}
