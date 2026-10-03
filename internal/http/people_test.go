package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zaentrum/katalog-api/internal/store"
	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// catalog is a test schema with 030 and 032 and one credited, described,
// portrayed person.
func catalog(t *testing.T) http.Handler {
	t.Helper()
	db := storetest.Open(t)
	db.Migrate030(t)
	db.Migrate032(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, sorttitle, year, rating) VALUES
		('m1', 'movie', 'A Film', 'Film, A', 2001, 7.5), ('s1', 'series', 'A Show', 'Show, A', 2010, 8)`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_people (id, name, sortname, alsoknownas, birthdate, birthplace,
			biography, tmdbpersonid, imdbid, knownfordepartment) VALUES
		('p1', 'Ada Example', 'Example, Ada', '["A. Example"]', '1950-03-01', 'Bern, Switzerland',
		 '{"en": "Ada Example is an actor.", "de": "Ada Example ist Schauspielerin."}', '12345', 'nm0000123', 'Acting'),
		('p2', 'Bo Writer', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL)`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itempeople (id, item_id, person_id, role, job, charactername, ordinal, episodecount) VALUES
		('c1', 'm1', 'p1', 'actor', NULL, 'Lead', 0, NULL),
		('c2', 'm1', 'p1', 'director', 'Director', NULL, NULL, NULL),
		('c3', 'm1', 'p2', 'writer', 'Screenplay', NULL, 1, NULL),
		('c4', 's1', 'p1', 'actor', NULL, 'Herself', 0, 12)`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_personartwork (id, person_id, kind, contenttype, bytes, sha256, isprimary)
		VALUES ('a1', 'p1', 'profile', 'image/jpeg', '\xffd8ffe0'::bytea, repeat('a', 64), true)`)

	h := &ItemsHandler{Store: &store.Store{Pool: db.Pool}}
	r := chi.NewRouter()
	r.Get("/items/{id}", h.Get)
	r.Get("/people", h.People)
	r.Get("/people/{id}", h.Person)
	return r
}

// get serves path and decodes the JSON object it answers.
func get(t *testing.T, h http.Handler, path string, header http.Header) (map[string]any, http.Header) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	t.Logf("GET %s\n%s", path, rec.Body)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return body, rec.Header()
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// A cast entry sends what it knows of a credit and omits the rest; top
// billing, 0, is sent.
func TestItemCastOnTheWire(t *testing.T) {
	h := catalog(t)
	body, _ := get(t, h, "/items/m1?include=people", nil)
	want := []any{
		map[string]any{"person_id": "p1", "name": "Ada Example", "role": "actor", "character": "Lead", "order": 0.0},
		map[string]any{"person_id": "p1", "name": "Ada Example", "role": "director", "job": "Director"},
		map[string]any{"person_id": "p2", "name": "Bo Writer", "role": "writer", "job": "Screenplay", "order": 1.0},
	}
	if !reflect.DeepEqual(body["cast"], want) {
		t.Errorf("cast %v, want %v", body["cast"], want)
	}
	body, _ = get(t, h, "/items/s1?include=cast", nil)
	if c := body["cast"].([]any)[0].(map[string]any); c["episode_count"] != 12.0 || c["character"] != "Herself" {
		t.Errorf("series cast %v, want 12 episodes as Herself", c)
	}
}

// A person sends their details, the biography in the language asked for,
// has_profile, and their roles on each title; one unknown is omitted.
func TestPersonOnTheWire(t *testing.T) {
	h := catalog(t)
	body, hdr := get(t, h, "/people/p1", http.Header{"Accept-Language": {"fr-CH, de;q=0.9, en;q=0.8"}})
	wantKeys := []string{"also_known_as", "biography", "biography_lang", "birth_date", "birthplace",
		"has_profile", "id", "imdb_id", "items", "known_for_department", "name", "sort_name", "tmdb_person_id"}
	if got := keys(body); !reflect.DeepEqual(got, wantKeys) {
		t.Errorf("keys %q, want %q (death_date unknown, so omitted)", got, wantKeys)
	}
	if body["biography"] != "Ada Example ist Schauspielerin." || body["biography_lang"] != "de" {
		t.Errorf("biography %v (%v), want the German one: fr has none, de is next", body["biography"], body["biography_lang"])
	}
	if body["has_profile"] != true || body["birth_date"] != "1950-03-01" || body["tmdb_person_id"] != "12345" {
		t.Errorf("person %v", body)
	}
	if v := hdr.Values("Vary"); !reflect.DeepEqual(v, []string{"Accept-Language"}) {
		t.Errorf("Vary %q, want Accept-Language", v)
	}
	items := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items %v, want the show and the film", items)
	}
	show, film := items[0].(map[string]any), items[1].(map[string]any)
	if show["id"] != "s1" || !reflect.DeepEqual(show["roles"], []any{"actor"}) ||
		film["id"] != "m1" || !reflect.DeepEqual(film["roles"], []any{"actor", "director"}) {
		t.Errorf("items %v, want s1 [actor] then m1 [actor director]", items)
	}

	// ?lang= wins over the header.
	if body, _ = get(t, h, "/people/p1?lang=en", http.Header{"Accept-Language": {"de"}}); body["biography_lang"] != "en" {
		t.Errorf("?lang=en: biography_lang %v", body["biography_lang"])
	}
	// A person the catalog knows little about is an id, a name, has_profile
	// and items.
	body, _ = get(t, h, "/people/p2", nil)
	if got, want := keys(body), []string{"has_profile", "id", "items", "name"}; !reflect.DeepEqual(got, want) || body["has_profile"] != false {
		t.Errorf("p2: %v, want keys %q and no portrait", body, want)
	}
}

func TestPeopleSearchOnTheWire(t *testing.T) {
	h := catalog(t)
	body, _ := get(t, h, "/people?q=example", nil)
	want := []any{map[string]any{"id": "p1", "name": "Ada Example", "credits": 2.0, "has_profile": true}}
	if !reflect.DeepEqual(body["people"], want) {
		t.Errorf("people %v, want %v", body["people"], want)
	}
}

func TestUnknownPersonIs404(t *testing.T) {
	h := catalog(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/people/nobody", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown person: %d, want 404", rec.Code)
	}
}
