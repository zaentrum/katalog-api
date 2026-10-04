package http

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zaentrum/katalog-api/internal/auth"
	"github.com/zaentrum/katalog-api/internal/store"
	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// An item on the wire says what it is rated: min_age (0 sent), and the
// certification it comes from with its country; nothing of a rating when
// nothing rates it.
func TestRatingOnTheWire(t *testing.T) {
	db := storetest.Open(t)
	db.Migrate036(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, min_age, certification, certification_country, min_age_override) VALUES
		('m0', 'movie', 'For All', 0, '0', 'DE', NULL), ('m13', 'movie', 'Teen', 13, 'PG-13', 'US', NULL),
		('mo', 'movie', 'By Hand', 18, '18', 'DE', 12), ('mu', 'movie', 'Unrated', NULL, NULL, NULL, NULL)`)
	h := &ItemsHandler{Store: &store.Store{Pool: db.Pool}}
	r := chi.NewRouter()
	r.Get("/items/{id}", h.Get)

	for id, want := range map[string]map[string]any{
		"m0":  {"min_age": 0.0, "certification": "0", "certification_country": "DE"},
		"m13": {"min_age": 13.0, "certification": "PG-13", "certification_country": "US"},
		"mo":  {"min_age": 12.0},
		"mu":  {},
	} {
		body, _ := get(t, r, "/items/"+id, http.Header{})
		for _, k := range []string{"min_age", "certification", "certification_country"} {
			if got, ok := body[k]; ok != (want[k] != nil) || got != want[k] {
				t.Errorf("%s: %s %v (sent %v), want %v", id, k, got, ok, want[k])
			}
		}
	}
}

// The cap a request is served at is the stricter of the bearer's max_rating
// and every max_rating parameter; a parameter that is no whole number of
// years is the strictest cap, 0; neither, and it is not capped.
func TestTheViewersCap(t *testing.T) {
	for _, tc := range []struct {
		claim *int
		query string
		want  string
	}{
		{nil, "", "uncapped"},
		{nil, "?max_rating=12", "12"},
		{ptr(16), "", "16"},
		{ptr(16), "?max_rating=12", "12"},
		{ptr(12), "?max_rating=16", "12"}, // a parameter never lifts the bearer's cap
		{nil, "?max_rating=16&max_rating=6", "6"},
		{nil, "?max_rating=0", "0"},
		{nil, "?max_rating=", "0"},
		{nil, "?max_rating=-1", "0"},
		{nil, "?max_rating=%2B12", "0"},
		{nil, "?max_rating=12.5", "0"},
		{nil, "?max_rating=twelve", "0"},
		{nil, "?max_rating=99999999999", "0"},
		{ptr(18), "?max_rating=adult", "0"},
		{nil, "?other=1", "uncapped"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/movies"+tc.query, nil)
		if tc.claim != nil {
			r = r.WithContext(auth.WithMaxRating(r.Context(), *tc.claim))
		}
		got := "uncapped"
		if age, ok := viewerCap(r); ok {
			got = strconv.Itoa(age)
		}
		if got != tc.want {
			t.Errorf("claim %v, %q: %s, want %s", tc.claim, tc.query, got, tc.want)
		}
	}
}

func ptr(n int) *int { return &n }

// cappedCatalog serves the catalog's routes as the router does, behind
// capped, each request's bearer cap the one claim says ("" for none).
func cappedCatalog(t *testing.T) func(claim, path string) (int, string) {
	t.Helper()
	db := storetest.Open(t)
	db.Migrate030(t)
	db.Migrate032(t)
	db.Migrate036(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, sorttitle, parent_id, min_age, certification, certification_country) VALUES
		('m6', 'movie', 'Six', 'Six', NULL, 6, '6', 'DE'), ('m16', 'movie', 'Sixteen', 'Sixteen', NULL, 16, '16', 'DE'),
		('mu', 'movie', 'Unrated', 'Unrated', NULL, NULL, NULL, NULL),
		('s16', 'series', 'Show', 'Show', NULL, 16, '16', 'DE'), ('e1', 'episode', 'Pilot', 'Pilot', 's16', NULL, NULL, NULL)`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_people (id, name) VALUES ('p1', 'Ada Example')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itempeople (id, item_id, person_id, role) VALUES
		('c1', 'm6', 'p1', 'actor'), ('c2', 'm16', 'p1', 'actor')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_genres (id, name) VALUES ('g', 'Drama')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemgenres (id, item_id, genre_id) VALUES ('ig1', 'm6', 'g'), ('ig2', 'm16', 'g')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_mediasegments (id, item_id, kind, startms, endms, source) VALUES
		('sg', 'm16', 'intro', 0, 1, 'manual')`)
	h := &ItemsHandler{Store: &store.Store{Pool: db.Pool}}
	r := chi.NewRouter()
	r.Use(capped)
	r.Get("/movies", h.listByType("movie"))
	r.Get("/items", h.List)
	r.Get("/items/{id}", h.Get)
	r.Get("/items/{id}/segments", h.Segments)
	r.Get("/items/{id}/similar", h.Similar)
	r.Get("/series/{id}/episodes", h.SeriesEpisodes)
	r.Get("/people", h.People)
	r.Get("/people/{id}", h.Person)
	return func(claim, path string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if claim != "" {
			n, _ := strconv.Atoi(claim)
			req = req.WithContext(auth.WithMaxRating(req.Context(), n))
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
}

// Every route of the catalog serves a capped viewer what the cap allows: the
// lists and their totals, a person's filmography and search; and answers a
// title it may not be served, by id, as a title there is not: the same 404,
// for the item, its segments, more like it and a series' episodes.
func TestEveryRouteServesTheCap(t *testing.T) {
	serve := cappedCatalog(t)
	notThere := func(path string) (int, string) { return serve("", strings.Replace(path, "{id}", "no-such-id", 1)) }
	for _, path := range []string{"/items/{id}", "/items/{id}/segments", "/items/{id}/similar", "/series/{id}/episodes"} {
		code, body := notThere(path)
		if code != http.StatusNotFound {
			t.Fatalf("%s of an id there is not: %d %q", path, code, body)
		}
		for _, tc := range []struct{ claim, id string }{{"12", "m16"}, {"12", "s16"}, {"12", "mu"}, {"0", "m6"}} {
			p := strings.Replace(path, "{id}", tc.id, 1)
			if path == "/series/{id}/episodes" && tc.id != "s16" {
				continue
			}
			if c, b := serve(tc.claim, p); c != code || b != body {
				t.Errorf("%s capped at %s: %d %q, want %d %q as for an id there is not", p, tc.claim, c, b, code, body)
			}
			if c, _ := serve("", p); c != http.StatusOK {
				t.Errorf("%s uncapped: %d", p, c)
			}
		}
	}

	for _, tc := range []struct{ claim, path, want string }{
		{"12", "/movies", `"items":[{"id":"m6"`},
		{"12", "/movies", `"total":1`},
		{"16", "/movies", `"total":2`},
		{"", "/movies", `"total":3`},
		{"16", "/items?max_rating=6", `"total":1`},
		{"16", "/items", `"total":4`},
		{"12", "/people?q=ada", `"credits":1`},
		{"5", "/people?q=ada", `"people":[]`},
		{"12", "/people/p1", `"items":[{"id":"m6"`},
		{"12", "/items/m6/similar", `"items":[]`},
		{"16", "/items/m6/similar", `"items":[{"id":"m16"`},
		{"16", "/series/s16/episodes", `"items":[{"id":"e1"`},
		{"16", "/items/e1", `"min_age":16`},
	} {
		code, body := serve(tc.claim, tc.path)
		if code != http.StatusOK || !strings.Contains(body, tc.want) {
			t.Errorf("%s capped at %q: %d %s, want %s", tc.path, tc.claim, code, body, tc.want)
		}
	}
}
