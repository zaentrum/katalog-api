package http

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/zaentrum/katalog-api/internal/store"
)

// The holdings say what the library holds, by the identities titles have
// outside it (ADR-0012): GET /api/v1/holdings?type=series&tmdb= (or tvdb=,
// imdb=) answers a series season by season, ?type=movie&tmdb= (or imdb=) a
// movie, and ?type=series or ?type=movie alone a page of every title of the
// type. They are for a service that thinks in those identities and holds no
// catalog ids, such as an addon's service account, and for a signed-in
// person: any bearer the verifier admits, as on every other read. They are
// the library's, unfiltered by any rating, so they are never served to a
// request capped at an age: a capped viewer is served nothing above the cap,
// and holdings that left titles out would say the library does not hold them.

// The size of a page of holdings when none is asked, and the largest one.
const (
	defaultHoldingsPageSize = 200
	maxHoldingsPageSize     = 1000
)

// holdingsParams are the parameters the holdings take; any other is refused,
// so that a misspelt identity is not taken for a request for the listing.
var holdingsParams = []string{"type", "tmdb", "tvdb", "imdb", "page", "pageSize"}

// holdingsSources are the sources a title of each type is looked up by, in the
// order a lookup names them.
var holdingsSources = map[string][]string{
	"series": {"tmdb", "tvdb", "imdb"},
	"movie":  {"tmdb", "imdb"},
}

// What an identity of each source is, as the catalog stores it (a TMDB or
// TVDB id is a number, an IMDb title id tt and a number), and a page number.
var (
	numberID    = regexp.MustCompile(`^[0-9]{1,20}$`)
	imdbID      = regexp.MustCompile(`^tt[0-9]{1,20}$`)
	wholeNumber = regexp.MustCompile(`^[0-9]{1,9}$`)
)

// Holdings answers what the library holds: GET /api/v1/holdings. With an
// identity it answers the series or the movie that carries it, or 404 when
// none does; without, a page of every title of the type. A bad parameter is
// 400 with {"error": "..."} saying which, and so are identities that name
// different titles and a page past the last. A request capped at an age, by
// its bearer's max_rating or a max_rating parameter (viewerCap), is 403.
func (h *ItemsHandler) Holdings(w http.ResponseWriter, r *http.Request) {
	if _, capped := viewerCap(r); capped {
		holdingsError(w, http.StatusForbidden, "holdings are the library's, unfiltered by any rating: "+
			"a request capped at an age (the bearer's max_rating, or a max_rating parameter) is not served them")
		return
	}
	q, err := parseHoldings(r.URL.Query())
	if err != nil {
		holdingsError(w, http.StatusBadRequest, err.Error())
		return
	}
	var answer any
	switch {
	case len(q.ids) == 0:
		answer, err = h.Store.ListHoldings(r.Context(), q.typ, q.page, q.pageSize)
	case q.typ == "series":
		answer, err = h.Store.SeriesHoldings(r.Context(), q.ids)
	default:
		answer, err = h.Store.MovieHolding(r.Context(), q.ids)
	}
	var (
		disagree *store.Disagreement
		past     *store.PastTheLastPage
	)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, answer)
	case errors.Is(err, store.ErrNotFound):
		holdingsError(w, http.StatusNotFound, "no "+q.typ+" has "+anyOf(q.ids))
	case errors.As(err, &disagree), errors.As(err, &past):
		holdingsError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNoIdentities):
		slog.Warn("holdings unavailable", "path", r.URL.Path, "err", err)
		holdingsError(w, http.StatusServiceUnavailable, err.Error())
	default:
		writeStoreErr(w, r, "holdings", err)
	}
}

// holdingsQuery is what a request for holdings asks: the type, and the
// identities to look a title up by, or else the page of the listing.
type holdingsQuery struct {
	typ            string
	ids            []store.Identity
	page, pageSize int
}

// parseHoldings reads a request for holdings, or says what is wrong with it.
func parseHoldings(q url.Values) (holdingsQuery, error) {
	var hq holdingsQuery
	var unknown []string
	for name := range q {
		if !slices.Contains(holdingsParams, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return hq, fmt.Errorf("holdings take type, tmdb, tvdb, imdb, page and pageSize, not %s", strings.Join(unknown, ", "))
	}
	typ, _, err := single(q, "type")
	if err != nil {
		return hq, err
	}
	sources, ok := holdingsSources[typ]
	switch {
	case typ == "":
		return hq, errors.New("type is required: series or movie")
	case !ok:
		return hq, fmt.Errorf("type is series or movie, not %s", typ)
	}
	hq.typ = typ
	for _, source := range []string{"tmdb", "tvdb", "imdb"} {
		id, given, err := single(q, source)
		switch {
		case err != nil:
			return hq, err
		case !given:
			continue
		case !slices.Contains(sources, source):
			return hq, fmt.Errorf("a %s is looked up by %s, not %s", typ, strings.Join(sources, " or "), source)
		case id == "":
			return hq, fmt.Errorf("%s is empty", source)
		case source == "imdb" && !imdbID.MatchString(id):
			return hq, fmt.Errorf("imdb is tt and a number, not %s", id)
		case source != "imdb" && !numberID.MatchString(id):
			return hq, fmt.Errorf("%s is a number, not %s", source, id)
		}
		hq.ids = append(hq.ids, store.Identity{Source: source, ID: id})
	}

	pg, pageGiven, err := single(q, "page")
	if err != nil {
		return hq, err
	}
	size, sizeGiven, err := single(q, "pageSize")
	if err != nil {
		return hq, err
	}
	if len(hq.ids) > 0 {
		if pageGiven || sizeGiven {
			return hq, errors.New("page and pageSize page the listing; a lookup by identity takes neither")
		}
		return hq, nil
	}
	hq.page, hq.pageSize = 1, defaultHoldingsPageSize
	if pageGiven {
		if hq.page, err = strconv.Atoi(pg); err != nil || !wholeNumber.MatchString(pg) || hq.page < 1 {
			return hq, fmt.Errorf("page is a whole number from 1, not %s", pg)
		}
	}
	if sizeGiven {
		hq.pageSize, err = strconv.Atoi(size)
		if err != nil || !wholeNumber.MatchString(size) || hq.pageSize < 1 || hq.pageSize > maxHoldingsPageSize {
			return hq, fmt.Errorf("pageSize is a whole number from 1 to %d, not %s", maxHoldingsPageSize, size)
		}
	}
	return hq, nil
}

// single is the value of the parameter name of q, its spaces trimmed; given is
// false when q does not have it. A parameter given twice with values that
// differ is an error.
func single(q url.Values, name string) (v string, given bool, err error) {
	vals, given := q[name]
	if !given {
		return "", false, nil
	}
	v = strings.TrimSpace(vals[0])
	for _, other := range vals[1:] {
		if other = strings.TrimSpace(other); other != v {
			return "", true, fmt.Errorf("%s is given twice: %s and %s", name, v, other)
		}
	}
	return v, true, nil
}

// anyOf names ids as alternatives: "tmdb 1", "tmdb 1 or imdb tt2", "tmdb 1,
// tvdb 2 or imdb tt3".
func anyOf(ids []store.Identity) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = id.String()
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// holdingsError answers status with {"error": message}.
func holdingsError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
