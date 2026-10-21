package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zaentrum/katalog-api/internal/store"
	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// searchCatalog is a catalog with katalog-manager's 049: a series and an
// episode of it, two films, and a person credited on one of them.
func searchCatalog(t *testing.T) http.Handler {
	t.Helper()
	db := storetest.Open(t)
	db.Migrate030(t)
	db.Migrate046(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, sorttitle, year, rating, description, parent_id, seasonnumber, episodenumber) VALUES
		('s1', 'series', 'Widow''s Bay', 'widow''s bay', 2026, 7.4, 'An island town.', NULL, NULL, NULL),
		('e1', 'episode', 'The Widow''s Walk', 'the widow''s walk', NULL, NULL, 'A storm.', 's1', 1, 3),
		('m1', 'movie', 'Black Widow', 'black widow', 2021, 6.7, 'A spy.', NULL, NULL, NULL),
		('m2', 'movie', 'Harbour Lights', 'harbour lights', 2001, 6.1, 'A young widow returns home.', NULL, NULL, NULL)`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_people (id, name) VALUES ('p1', 'Scarlett Johansson')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itempeople (id, item_id, person_id, role) VALUES ('c1', 'm1', 'p1', 'actor')`)
	db.Migrate049(t)

	h := &ItemsHandler{Store: &store.Store{Pool: db.Pool}}
	r := chi.NewRouter()
	r.Get("/search", h.Search)
	r.Get("/movies", h.listByType("movie"))
	r.Get("/series", h.listByType("series"))
	return r
}

type searchBody struct {
	Items []struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		Title  string `json:"title"`
		Match  string `json:"match"`
		Season *int   `json:"season_number"`
		Number *int   `json:"episode_number"`
		Series *struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Matched bool   `json:"matched"`
		} `json:"series"`
	} `json:"items"`
	People []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Credits int    `json:"credits"`
		Match   string `json:"match"`
	} `json:"people"`
}

func getSearch(t *testing.T, h http.Handler, path string) (searchBody, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	var body searchBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	return body, raw
}

// One search answers the titles of every type in one ranked list, each with
// how it matched, an episode with its series, and the people apart.
func TestSearchOnTheWire(t *testing.T) {
	h := searchCatalog(t)
	body, raw := getSearch(t, h, "/search?q=widow")
	var got []string
	for _, it := range body.Items {
		got = append(got, it.ID+":"+it.Match)
	}
	if strings.Join(got, " ") != "s1:prefix m1:words e1:words" {
		t.Errorf("titles %q", got)
	}
	ep := body.Items[2]
	if ep.Series == nil || ep.Series.ID != "s1" || ep.Series.Title != "Widow's Bay" || !ep.Series.Matched ||
		ep.Season == nil || *ep.Season != 1 || ep.Number == nil || *ep.Number != 3 {
		t.Errorf("the episode does not name its series, season and number: %+v %+v", ep, ep.Series)
	}
	if body.Items[0].Series != nil {
		t.Error("a series names a series")
	}
	if people, ok := raw["people"].([]any); !ok || len(people) != 0 {
		t.Errorf("people for widow: %v", raw["people"])
	}

	// Films and series only, as the clients ask until they show episodes.
	body, _ = getSearch(t, h, "/search?q=widow&types=movie,series")
	if len(body.Items) != 2 || body.Items[0].ID != "s1" || body.Items[1].ID != "m1" {
		t.Errorf("films and series: %+v", body.Items)
	}
	body, _ = getSearch(t, h, "/search?q=widow&types=series&types=movie&limit=1")
	if len(body.Items) != 1 || body.Items[0].ID != "s1" {
		t.Errorf("one title: %+v", body.Items)
	}

	body, _ = getSearch(t, h, "/search?q=johansson")
	if len(body.People) != 1 || body.People[0].Name != "Scarlett Johansson" || body.People[0].Match != "words" ||
		body.People[0].Credits != 1 || len(body.Items) != 0 {
		t.Errorf("johansson: %+v %+v", body.People, body.Items)
	}
	body, _ = getSearch(t, h, "/search?q=johansson&people=0")
	if len(body.People) != 0 {
		t.Errorf("people=0: %+v", body.People)
	}

	_, raw = getSearch(t, h, "/search?q=%20")
	if items, ok := raw["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("a blank query: %v", raw)
	}
	if people, ok := raw["people"].([]any); !ok || len(people) != 0 {
		t.Errorf("a blank query: %v", raw)
	}
}

// A type's list with a query and no filter is ranked as the search ranks it,
// within the type: the clients in the field ask for films and series apart.
func TestATypesSearchIsRanked(t *testing.T) {
	h := searchCatalog(t)
	body, raw := getSearch(t, h, "/series?q=widows%20bay")
	if len(body.Items) != 1 || body.Items[0].ID != "s1" || body.Items[0].Match != "exact" {
		t.Errorf("series widows bay: %+v", body.Items)
	}
	if raw["total"] != float64(1) || raw["limit"] != float64(50) || raw["offset"] != float64(0) {
		t.Errorf("the envelope: %v", raw)
	}
	// A title matched among the films: the plots are not.
	body, _ = getSearch(t, h, "/movies?q=widow")
	if len(body.Items) != 1 || body.Items[0].ID != "m1" {
		t.Errorf("films widow: %+v", body.Items)
	}
	// None matched: the plots are.
	body, _ = getSearch(t, h, "/movies?q=returns%20home")
	if len(body.Items) != 1 || body.Items[0].ID != "m2" || body.Items[0].Match != "plot" {
		t.Errorf("films returns home: %+v", body.Items)
	}
	body, _ = getSearch(t, h, "/movies?q=widow&offset=1")
	if len(body.Items) != 0 {
		t.Errorf("films widow from 1: %+v", body.Items)
	}
}
