package http

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"

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

// maxVisibleIDs is how many titles one /visible request may ask about.
const maxVisibleIDs = 500

// Visible answers which of the titles a viewer capped at an age may be
// served, for a request a stream token authorizes, which carries the cap but
// no bearer: chino-api asks it before it proxies a capped viewer's playback,
// artwork and subtitle requests, and to filter the lists it holds itself.
// ?ids= (comma-separated, at most maxVisibleIDs) are the titles, ?max_rating=
// the cap, required: without it the answer is 400, and one that is no whole
// number of years is the strictest cap, 0. The answer is {"ids": [...]}: the
// ids asked about, in their order, that name a title the cap allows; an id of
// no title is left out as one the cap leaves out. Like the asset routes it
// takes no bearer: the Service is in-cluster only.
func (h *ItemsHandler) Visible(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if _, ok := q[maxRatingParam]; !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "max_rating is required"})
		return
	}
	var ids []string
	seen := map[string]bool{}
	for _, raw := range q["ids"] {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) > maxVisibleIDs {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "at most 500 ids at a time"})
		return
	}
	age, _ := viewerCap(r)
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ids": []string{}})
		return
	}
	visible, err := h.Store.Visible(store.WithMaxAge(r.Context(), age), ids)
	if writeStoreErr(w, r, "visible items", err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ids": visible})
}
