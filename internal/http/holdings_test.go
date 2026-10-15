package http

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/zaentrum/katalog-api/internal/auth"
	"github.com/zaentrum/katalog-api/internal/auth/authtest"
	"github.com/zaentrum/katalog-api/internal/config"
	"github.com/zaentrum/katalog-api/internal/store"
)

// The ids of the titles the holdings tests add to finaleCatalog's show: its
// special, another show, a film enrichment identified and one it did not.
const (
	special   = "5e6f7a8b-9c0d-4e1f-8a2b-3c4d5e6f7a8b"
	otherShow = "8b7a6f5e-4d3c-4b2a-9f1e-0d9c8b7a6f5e"
	heldFilm  = "f001aeff-9c18-4183-b51b-51403af2515e"
	bareFilm  = "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
)

// holdingsCatalog is the service's router, its bearer routes behind a
// verifier of the issuer it answers, on finaleCatalog's show (tmdb 97546, tvdb
// 362392, imdb tt9208876, from 2020, rated 16), with migration 045: its opener
// a file of its own not packaged yet, its finale one file of two episodes
// packaged; besides, its special S00E01, packaged before the library. Another
// show (tmdb 1399, tvdb 81189) holds no episode; a film (tmdb 4711, imdb
// tt0004711, rated 18) is packaged, and a film nothing identified nor rated
// has no file.
func holdingsCatalog(t *testing.T) (http.Handler, *authtest.Issuer) {
	t.Helper()
	db := finaleCatalog(t, true)
	db.Migrate036(t)
	db.Exec(t, `UPDATE com_nalet_katalog_items SET year = 2020, min_age = 16 WHERE id = $1`, show)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, year, parent_id, seasonnumber, episodenumber, min_age) VALUES
		($1, 'episode', 'A Special',                NULL, $2,   0,    1,    NULL),
		($3, 'series',  'Another Show',             2011, NULL, NULL, NULL, 12),
		($4, 'movie',   'A Film',                   2024, NULL, NULL, NULL, 18),
		($5, 'movie',   'A Film Nobody Identified', NULL, NULL, NULL, NULL, NULL)`, special, show, otherShow, heldFilm, bareFilm)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemexternalids (id, item_id, source, externalid) VALUES
		('x1', $1, 'tmdb', '97546'), ('x2', $1, 'tvdb', '362392'), ('x3', $1, 'imdb', 'tt9208876'),
		('x4', $2, 'tmdb', '1399'), ('x5', $2, 'tvdb', '81189'),
		('x6', $3, 'tmdb', '4711'), ('x7', $3, 'imdb', 'tt0004711')`, show, otherShow, heldFilm)
	db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind) VALUES
		('a4', $1, '/var/lib/katalog/media/shows/A Show/A Show S00E01.mkv', true, 'primary'),
		('a5', $1, '/var/lib/katalog/packages/shows/5e/`+special+`/manifest.json', false, 'packaged'),
		('a6', $2, '/var/lib/katalog/movies/f0/`+heldFilm+`/versions/v1/package.json', false, 'packaged')`, special, heldFilm)

	iss := authtest.NewIssuer(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	verifier, err := auth.NewVerifier(ctx, iss.URL, "chino")
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewRouter(config.Config{}, &store.Store{Pool: db.Pool}, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return h, iss
}

// bearer is the header that sends tok.
func bearer(tok string) http.Header { return http.Header{"Authorization": {"Bearer " + tok}} }

// What the library holds of holdingsCatalog's titles, on the wire.
const (
	showHoldings = `{"seriesId":"` + show + `","title":"A Show","year":2020,` +
		`"externalIds":{"imdb":"tt9208876","tmdb":"97546","tvdb":"362392"},"seasons":[` +
		`{"season":0,"episodes":[{"episode":1,"itemId":"` + special + `","hasFile":true,"playable":true,"coveredBy":null}]},` +
		`{"season":1,"episodes":[` +
		`{"episode":1,"itemId":"` + opener + `","hasFile":true,"playable":false,"coveredBy":null},` +
		`{"episode":9,"itemId":"` + finale + `","hasFile":true,"playable":true,"coveredBy":null},` +
		`{"episode":10,"itemId":"` + finaleTwo + `","hasFile":true,"playable":true,"coveredBy":"` + finale + `"}]}]}`
	filmHolding = `{"itemId":"` + heldFilm + `","title":"A Film","year":2024,` +
		`"externalIds":{"imdb":"tt0004711","tmdb":"4711"},"hasFile":true,"playable":true}`
	showListed = `{"itemId":"` + show + `","title":"A Show","year":2020,` +
		`"externalIds":{"imdb":"tt9208876","tmdb":"97546","tvdb":"362392"},"heldEpisodes":4}`
	otherListed = `{"itemId":"` + otherShow + `","title":"Another Show","year":2011,` +
		`"externalIds":{"tmdb":"1399","tvdb":"81189"},"heldEpisodes":0}`
	bareListed = `{"itemId":"` + bareFilm + `","title":"A Film Nobody Identified","year":null,"externalIds":{}}`
	filmListed = `{"itemId":"` + heldFilm + `","title":"A Film","year":2024,"externalIds":{"imdb":"tt0004711","tmdb":"4711"}}`
)

// The holdings on the wire, through the router, as an addon's service account
// asks them: a series by any of its identities or by several that agree,
// season by season, the special first, its covered episode naming its holder
// and playing as it does; a movie by either of its identities; and every title
// of a type, a page at a time in id order, with a series' count of episodes
// that have a file. Whatever the titles are rated, none is left out.
func TestHoldingsOnTheWire(t *testing.T) {
	h, iss := holdingsCatalog(t)
	addon := bearer(iss.Addon(t, nil))
	for _, tc := range []struct{ query, want string }{
		{"?type=series&tmdb=97546", showHoldings},
		{"?type=series&tvdb=362392", showHoldings},
		{"?type=series&imdb=tt9208876", showHoldings},
		{"?type=series&tmdb=97546&tvdb=362392&imdb=tt9208876", showHoldings},
		{"?type=series&tmdb=97546&tvdb=1", showHoldings},
		{"?type=series&tmdb=%2097546%20&tmdb=97546", showHoldings},
		{"?type=movie&tmdb=4711", filmHolding},
		{"?type=movie&imdb=tt0004711", filmHolding},
		{"?type=movie&tmdb=4711&imdb=tt0004711", filmHolding},
		{"?type=series", `{"items":[` + showListed + `,` + otherListed + `],"page":1,"pageSize":200,"total":2}`},
		{"?type=series&page=1&pageSize=1", `{"items":[` + showListed + `],"page":1,"pageSize":1,"total":2}`},
		{"?type=series&page=2&pageSize=1", `{"items":[` + otherListed + `],"page":2,"pageSize":1,"total":2}`},
		{"?type=movie&pageSize=1000", `{"items":[` + bareListed + `,` + filmListed + `],"page":1,"pageSize":1000,"total":2}`},
	} {
		code, body := serve(h, "/api/v1/holdings"+tc.query, addon)
		t.Logf("GET /api/v1/holdings%s\n%s", tc.query, body)
		if code != http.StatusOK || body != tc.want {
			t.Errorf("GET /api/v1/holdings%s:\n got %d %s\nwant 200 %s", tc.query, code, body, tc.want)
		}
	}
}

// A lookup no title answers is 404, saying so; a request the holdings cannot
// answer is 400, saying why: no type or another one, an identity the type is
// not looked up by, empty or malformed, one given twice with two values,
// identities that name different titles, a page or a page size that is no
// whole number in range, a page past the last, a page asked of a lookup, and
// a parameter the holdings do not take.
func TestHoldingsNotFoundAndBadRequests(t *testing.T) {
	h, iss := holdingsCatalog(t)
	addon := bearer(iss.Addon(t, nil))
	for _, tc := range []struct {
		query string
		code  int
		want  string
	}{
		{"?type=series&tmdb=1", 404, "no series has tmdb 1"},
		{"?type=series&tmdb=1&tvdb=2&imdb=tt3", 404, "no series has tmdb 1, tvdb 2 or imdb tt3"},
		{"?type=series&tmdb=4711", 404, "no series has tmdb 4711"},
		{"?type=movie&tmdb=97546", 404, "no movie has tmdb 97546"},
		{"?type=movie&imdb=tt1&tmdb=1", 404, "no movie has tmdb 1 or imdb tt1"},
		{"", 400, "type is required: series or movie"},
		{"?tmdb=97546", 400, "type is required: series or movie"},
		{"?type=", 400, "type is required: series or movie"},
		{"?type=episode&tmdb=1", 400, "type is series or movie, not episode"},
		{"?type=series&type=movie", 400, "type is given twice: series and movie"},
		{"?type=movie&tvdb=81189", 400, "a movie is looked up by tmdb or imdb, not tvdb"},
		{"?type=series&tmdb=", 400, "tmdb is empty"},
		{"?type=series&tvdb=+", 400, "tvdb is empty"},
		{"?type=series&tmdb=abc", 400, "tmdb is a number, not abc"},
		{"?type=series&tvdb=-1", 400, "tvdb is a number, not -1"},
		{"?type=series&imdb=9208876", 400, "imdb is tt and a number, not 9208876"},
		{"?type=series&tmdb=97546&tmdb=1399", 400, "tmdb is given twice: 97546 and 1399"},
		{"?type=series&tmdb=97546&tvdb=81189", 400, "tmdb 97546 and tvdb 81189 name different series"},
		{"?type=series&tmdb=1399&imdb=tt9208876", 400, "tmdb 1399 and imdb tt9208876 name different series"},
		{"?type=series&tmdb=97546&page=1", 400, "page and pageSize page the listing; a lookup by identity takes neither"},
		{"?type=movie&imdb=tt0004711&pageSize=10", 400, "page and pageSize page the listing; a lookup by identity takes neither"},
		{"?type=series&page=0", 400, "page is a whole number from 1, not 0"},
		{"?type=series&page=-1", 400, "page is a whole number from 1, not -1"},
		{"?type=series&page=one", 400, "page is a whole number from 1, not one"},
		{"?type=series&page=99999999999", 400, "page is a whole number from 1, not 99999999999"},
		{"?type=series&pageSize=0", 400, "pageSize is a whole number from 1 to 1000, not 0"},
		{"?type=series&pageSize=1001", 400, "pageSize is a whole number from 1 to 1000, not 1001"},
		{"?type=series&pageSize=", 400, "pageSize is a whole number from 1 to 1000, not "},
		{"?type=series&page=3&pageSize=1", 400, "page 3 is past the last page, 2: 2 series at 1 a page"},
		{"?type=movie&page=2", 400, "page 2 is past the last page, 1: 2 movies at 200 a page"},
		{"?type=series&tmdbid=97546", 400, "holdings take type, tmdb, tvdb, imdb, page and pageSize, not tmdbid"},
		{"?type=series&limit=1&offset=2", 400, "holdings take type, tmdb, tvdb, imdb, page and pageSize, not limit, offset"},
	} {
		code, body := serve(h, "/api/v1/holdings"+tc.query, addon)
		if want := `{"error":"` + tc.want + `"}`; code != tc.code || body != want {
			t.Errorf("GET /api/v1/holdings%s:\n got %d %s\nwant %d %s", tc.query, code, body, tc.code, want)
		}
	}
}

// Who reads the holdings is who reads the catalog: any bearer the verifier
// admits, a signed-in person's and an addon's service account's alike, and
// the addon reads an item's identities with it as well. No bearer, one that
// does not verify and one for another audience are refused as on every
// route; so is every request while the issuer cannot be reached. A request
// capped at an age, by its bearer's claim or a max_rating parameter, is
// refused (403): the holdings are not filtered, and a capped viewer is served
// nothing above the cap.
func TestWhoReadsTheHoldings(t *testing.T) {
	h, iss := holdingsCatalog(t)
	const path = "/api/v1/holdings?type=series&tmdb=97546"
	const capped = `{"error":"holdings are the library's, unfiltered by any rating: ` +
		`a request capped at an age (the bearer's max_rating, or a max_rating parameter) is not served them"}`
	for _, tc := range []struct {
		who, path string
		header    http.Header
		code      int
		body      string
	}{
		{"a signed-in person", path, bearer(iss.Person(t, nil)), 200, showHoldings},
		{"an addon's service account", path, bearer(iss.Addon(t, nil)), 200, showHoldings},
		{"an addon reading an item", "/api/v1/items/" + heldFilm, bearer(iss.Addon(t, nil)), 200,
			`"externalIds":{"imdb":"tt0004711","tmdb":"4711"}`},
		{"no bearer", path, nil, 401, "missing bearer"},
		{"a bearer that is no token", path, bearer("not-a-token"), 401, "invalid token"},
		{"a token for another audience", path, bearer(iss.Addon(t, map[string]any{"aud": "account"})), 403,
			"audience not permitted"},
		{"a person capped at 18", path, bearer(iss.Person(t, map[string]any{"max_rating": 18})), 403, capped},
		{"a capped addon token", path, bearer(iss.Addon(t, map[string]any{"max_rating": 21})), 403, capped},
		{"a person, for a viewer capped at 16", path + "&max_rating=16", bearer(iss.Person(t, nil)), 403, capped},
		{"a listing for a capped viewer", "/api/v1/holdings?type=movie&max_rating=", bearer(iss.Addon(t, nil)), 403, capped},
	} {
		code, body := serve(h, tc.path, tc.header)
		if code != tc.code || (tc.code == 200 && !strings.Contains(body, tc.body)) || (tc.code != 200 && body != tc.body) {
			t.Errorf("%s, GET %s:\n got %d %s\nwant %d %s", tc.who, tc.path, code, body, tc.code, tc.body)
		}
	}
	unreachable := router(t, &store.Store{})
	if code, body := serve(unreachable, path, bearer(iss.Addon(t, nil))); code != 503 || body != "auth unavailable" {
		t.Errorf("the issuer unreachable: %d %q, want 503 auth unavailable", code, body)
	}
}
