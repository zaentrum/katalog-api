package http

import (
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zaentrum/katalog-api/internal/store"
	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// An item on the wire says the identities it carries outside the catalog in
// externalIds, every source keyed as stored: by id, in the lists and in a
// series' episodes. An item the catalog keeps none of omits it, and is sent as
// before.
func TestIdentitiesOnTheWire(t *testing.T) {
	db := storetest.Open(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, sorttitle, parent_id, seasonnumber, episodenumber) VALUES
		('m1', 'movie', 'A Film', 'A Film', NULL, NULL, NULL), ('m2', 'movie', 'Another Film', 'Another Film', NULL, NULL, NULL),
		('s1', 'series', 'A Show', 'A Show', NULL, NULL, NULL), ('e1', 'episode', 'Pilot', 'Pilot', 's1', 1, 1)`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemexternalids (id, item_id, source, externalid) VALUES
		('x1', 'm1', 'tmdb', '4711'), ('x2', 'm1', 'imdb', 'tt0004711'),
		('x3', 's1', 'tmdb', '97546'), ('x4', 's1', 'tvdb', '362392'), ('x5', 's1', 'imdb', 'tt9208876'),
		('x6', 'e1', 'tmdb-episode', '1984432')`)
	hd := &ItemsHandler{Store: &store.Store{Pool: db.Pool}}
	r := chi.NewRouter()
	r.Get("/items/{id}", hd.Get)
	r.Get("/movies", hd.listByType("movie"))
	r.Get("/series", hd.listByType("series"))
	r.Get("/series/{id}/episodes", hd.SeriesEpisodes)

	want := map[string]any{
		"m1": map[string]any{"tmdb": "4711", "imdb": "tt0004711"},
		"s1": map[string]any{"tmdb": "97546", "tvdb": "362392", "imdb": "tt9208876"},
		"e1": map[string]any{"tmdb-episode": "1984432"},
	}
	check := func(where string, item map[string]any) {
		t.Helper()
		id, _ := item["id"].(string)
		got, sent := item["externalIds"]
		if w, ok := want[id]; sent != ok || !reflect.DeepEqual(got, w) {
			t.Errorf("%s, %s: externalIds %v (sent %v), want %v", where, id, got, sent, w)
		}
	}
	for _, id := range []string{"m1", "m2", "s1", "e1"} {
		body, _ := get(t, r, "/items/"+id, nil)
		check("by id", body)
	}
	for _, path := range []string{"/movies", "/series", "/series/s1/episodes"} {
		body, _ := get(t, r, path, nil)
		items, _ := body["items"].([]any)
		if len(items) == 0 {
			t.Errorf("%s: no items", path)
		}
		for _, it := range items {
			check(path, it.(map[string]any))
		}
	}
}
