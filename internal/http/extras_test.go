package http

import (
	"net/http"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zaentrum/katalog-api/internal/store"
	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// An item's extras on the wire, with include=extras: id, kind and title
// always, language and duration_ms when known, and season_number on a series'
// extra that belongs to a season, the specials' 0 sent. Without the include
// there is no extras field, and an item without extras that play has none
// either.
func TestExtrasOnTheWire(t *testing.T) {
	db := storetest.Open(t)
	db.Migrate039(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title) VALUES
		('m1', 'movie', 'A Film'), ('s1', 'series', 'A Show'), ('m2', 'movie', 'Nothing That Plays')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras (id, item_id, kind, title, label, language, durationms,
			seasonnumber, hidden, registeredby, state, packagedat, createdat) VALUES
		('1b5c2a8e-6f0d-4c3e-9a51-2d7f0c4b8e01', 'm1', 'trailer',    'trailer.mov',     'Trailer', 'en', 33000,
		 NULL, false, 'api', 'ready', now(), '2026-10-05 08:00:00+00'),
		('1b5c2a8e-6f0d-4c3e-9a51-2d7f0c4b8e02', 'm1', 'featurette', 'Making the Film', NULL,      NULL, NULL,
		 NULL, false, 'api', 'ready', now(), '2026-10-05 08:01:00+00'),
		('1b5c2a8e-6f0d-4c3e-9a51-2d7f0c4b8e03', 's1', 'trailer',    'Series Trailer',  NULL,      'de', 95000,
		 NULL, false, 'api', 'ready', now(), '2026-10-05 08:00:00+00'),
		('1b5c2a8e-6f0d-4c3e-9a51-2d7f0c4b8e04', 's1', 'featurette', 'Specials',        NULL,      NULL, 61000,
		 0,    false, 'api', 'ready', now(), '2026-10-05 08:01:00+00'),
		('1b5c2a8e-6f0d-4c3e-9a51-2d7f0c4b8e05', 'm2', 'trailer',    'Hidden',          NULL,      NULL, NULL,
		 NULL, true,  'api', 'ready', now(), '2026-10-05 08:00:00+00')`)
	h := &ItemsHandler{Store: &store.Store{Pool: db.Pool}}
	r := chi.NewRouter()
	r.Get("/items/{id}", h.Get)

	for path, want := range map[string][]any{
		"/items/m1?include=extras": {
			map[string]any{"id": "1b5c2a8e-6f0d-4c3e-9a51-2d7f0c4b8e01", "kind": "trailer", "title": "Trailer",
				"language": "en", "duration_ms": 33000.0},
			map[string]any{"id": "1b5c2a8e-6f0d-4c3e-9a51-2d7f0c4b8e02", "kind": "featurette", "title": "Making the Film"},
		},
		"/items/s1?include=genres,extras": {
			map[string]any{"id": "1b5c2a8e-6f0d-4c3e-9a51-2d7f0c4b8e03", "kind": "trailer", "title": "Series Trailer",
				"language": "de", "duration_ms": 95000.0},
			map[string]any{"id": "1b5c2a8e-6f0d-4c3e-9a51-2d7f0c4b8e04", "kind": "featurette", "title": "Specials",
				"duration_ms": 61000.0, "season_number": 0.0},
		},
	} {
		body, _ := get(t, r, path, nil)
		if !reflect.DeepEqual(body["extras"], want) {
			t.Errorf("%s: extras %v, want %v", path, body["extras"], want)
		}
	}
	for _, path := range []string{"/items/m1", "/items/m1?include=genres,segments", "/items/m2?include=extras"} {
		if body, _ := get(t, r, path, nil); body["extras"] != nil {
			t.Errorf("%s: extras %v, want no extras field", path, body["extras"])
		}
	}
}

// A catalog without migration 039 answers an item asked for with its extras as
// one that has none: the item and what else was asked for, and no extras
// field.
func TestExtrasOnTheWireWithoutTheTable(t *testing.T) {
	db := storetest.Open(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title) VALUES ('m1', 'movie', 'A Film')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_genres (id, name) VALUES ('g1', 'Drama')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemgenres (id, item_id, genre_id) VALUES ('ig1', 'm1', 'g1')`)
	h := &ItemsHandler{Store: &store.Store{Pool: db.Pool}}
	r := chi.NewRouter()
	r.Get("/items/{id}", h.Get)

	body, _ := get(t, r, "/items/m1?include=genres,extras", http.Header{})
	if _, ok := body["extras"]; ok || !reflect.DeepEqual(body["genres"], []any{"Drama"}) {
		t.Errorf("without 039: %v, want the genre and no extras field", body)
	}
}
