package http

import (
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

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
