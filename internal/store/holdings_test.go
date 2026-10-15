package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// heldShow is a catalog with migrations 036 and 045 whose show s1 (tmdb 97546,
// tvdb 362392, imdb tt9208876) holds, each "id: what it is":
//
//	e0:  S00E01, a special, a file of its own, not packaged
//	e1:  S01E01, a file of its own, packaged
//	h2:  S01E02, its file holding S01E02 to S01E04, packaged
//	z3:  S01E03, held in h2's file
//	a4:  S01E04, held in h2's file (its id sorts before z3's, its number after)
//	h6:  S01E06, its file holding S01E06 and S01E07, not packaged yet
//	c7:  S01E07, held in h6's file, with a stray package of its own
//	u8:  S01E08, no file: the holder its file was is gone
//	r9:  S01E09, its original retired, nothing packaged
//	p21: S02E01, packaged, its original retired
//	n0:  an episode without numbers, a file of its own
//
// and the show s2 (tmdb 1399) with one episode, f1 (tvdb 7777777), a file of
// its own; the film m1 (imdb tt0004711) that carries the show's tmdb id (TMDB
// numbers films and shows apart); and the show s3 that nothing identified. The
// show is rated 16, the film 18.
func heldShow(t *testing.T) (*Store, *storetest.DB) {
	t.Helper()
	st, db := open(t)
	db.Migrate036(t)
	addItem(t, db, "s1", "series", "A Show", 2020, 8)
	addItem(t, db, "s2", "series", "Another Show", 2011, 7)
	addItem(t, db, "s3", "series", "A Show Nobody Identified", 2015, 6)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	db.Exec(t, `UPDATE com_nalet_katalog_items SET min_age = 16 WHERE id = 's1'`)
	db.Exec(t, `UPDATE com_nalet_katalog_items SET min_age = 18 WHERE id = 'm1'`)
	identify(t, db, "s1", "tmdb", "97546")
	identify(t, db, "s1", "tvdb", "362392")
	identify(t, db, "s1", "imdb", "tt9208876")
	identify(t, db, "s2", "tmdb", "1399")
	identify(t, db, "m1", "tmdb", "97546")
	identify(t, db, "m1", "imdb", "tt0004711")
	addEpisode(t, db, "e0", "s1", 0, 1)
	for id, n := range map[string]int{"e1": 1, "h2": 2, "z3": 3, "a4": 4, "h6": 6, "c7": 7, "u8": 8, "r9": 9} {
		addEpisode(t, db, id, "s1", 1, n)
	}
	addEpisode(t, db, "p21", "s1", 2, 1)
	addEpisode(t, db, "f1", "s2", 1, 1)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, parent_id) VALUES ('n0', 'episode', 'Unnumbered', 's1')`)
	identify(t, db, "e1", "tmdb-episode", "1984432")
	identify(t, db, "f1", "tvdb", "7777777")
	addAsset(t, db, "a-e0", "e0", lib+"/media/shows/A Show/A Show S00E01.mkv", true, "primary")
	addAsset(t, db, "a-e1", "e1", lib+"/media/shows/A Show/A Show S01E01.mkv", true, "primary")
	addAsset(t, db, "p-e1", "e1", lib+"/packages/shows/e1/e1/manifest.json", false, "packaged")
	addAsset(t, db, "a-h2", "h2", lib+"/media/shows/A Show/A Show S01E02-E04.mkv", true, "")
	addAsset(t, db, "p-h2", "h2", lib+"/packages/shows/h2/h2/manifest.json", false, "packaged")
	addAsset(t, db, "a-h6", "h6", lib+"/media/shows/A Show/A Show S01E06-E07.mkv", true, "primary")
	addAsset(t, db, "p-c7", "c7", lib+"/packages/shows/c7/c7/manifest.json", false, "packaged")
	addAsset(t, db, "o-r9", "r9", lib+"/series/s1/s1/episodes/r9/sources/src9", false, "original")
	addAsset(t, db, "o-p21", "p21", lib+"/series/s1/s1/episodes/p21/sources/src21", false, "original")
	addAsset(t, db, "p-p21", "p21", lib+"/series/s1/s1/episodes/p21/versions/v1/package.json", false, "packaged")
	addAsset(t, db, "a-n0", "n0", lib+"/media/shows/A Show/A Show Unnumbered.mkv", true, "primary")
	addAsset(t, db, "a-f1", "f1", lib+"/media/shows/Another Show/Another Show S01E01.mkv", true, "primary")
	addAsset(t, db, "a-m1", "m1", lib+"/media/A Film (2001).mkv", true, "primary")
	db.Migrate045(t)
	cover(t, db, "h2", "z3", "a4")
	cover(t, db, "h6", "c7")
	return st, db
}

// ep is an episode of a series' holdings; by is its holder, "" for none.
func ep(n int, id string, file, plays bool, by string) EpisodeHolding {
	e := EpisodeHolding{Episode: n, ItemID: id, HasFile: file, Playable: plays}
	if by != "" {
		e.CoveredBy = &by
	}
	return e
}

func year(n int) *int { return &n }

// heldShowHoldings is what the library holds of heldShow's show s1.
func heldShowHoldings() SeriesHoldings {
	return SeriesHoldings{SeriesID: "s1", Title: "A Show", Year: year(2020),
		ExternalIDs: map[string]string{"tmdb": "97546", "tvdb": "362392", "imdb": "tt9208876"},
		Seasons: []SeasonHoldings{
			{Season: 0, Episodes: []EpisodeHolding{ep(1, "e0", true, false, "")}},
			{Season: 1, Episodes: []EpisodeHolding{
				ep(1, "e1", true, true, ""), ep(2, "h2", true, true, ""), ep(3, "z3", true, true, "h2"),
				ep(4, "a4", true, true, "h2"), ep(6, "h6", true, false, ""), ep(7, "c7", true, false, "h6"),
				ep(8, "u8", false, false, ""), ep(9, "r9", false, false, ""),
			}},
			{Season: 2, Episodes: []EpisodeHolding{ep(1, "p21", true, true, "")}},
		}}
}

// asJSON is v as the wire carries it, to compare and to show.
func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A series' holdings, found by any of its identities or by several that agree:
// season by season in their order, the specials first, each episode in order
// with its item id, whether it has a file (one it was taken in from, a row
// without a kind among them, or a package) and whether it plays (packaged). A
// covered episode names its holder and has the holder's file and package,
// whatever it holds itself; an episode whose holder is gone, or whose original
// was retired with nothing packaged, has neither. An episode without numbers
// is left out, and so is every episode of another series. Before 045 no
// episode is covered, and each says what it holds itself.
func TestASeriesHoldings(t *testing.T) {
	st, db := heldShow(t)
	ctx := context.Background()
	want := asJSON(t, heldShowHoldings())
	for _, ids := range [][]Identity{
		{{"tmdb", "97546"}}, {{"tvdb", "362392"}}, {{"imdb", "tt9208876"}},
		{{"tmdb", "97546"}, {"tvdb", "362392"}, {"imdb", "tt9208876"}},
		{{"tvdb", "1"}, {"tmdb", "97546"}}, // an identity the catalog keeps no series by
	} {
		h, err := st.SeriesHoldings(ctx, ids)
		if got := asJSON(t, h); err != nil || got != want {
			t.Errorf("by %v: %v\n got %s\nwant %s", ids, err, got, want)
		}
	}
	h, err := st.SeriesHoldings(ctx, []Identity{{"tmdb", "1399"}})
	if err != nil || h.SeriesID != "s2" || asJSON(t, h.Seasons) != `[{"season":1,"episodes":[`+
		`{"episode":1,"itemId":"f1","hasFile":true,"playable":false,"coveredBy":null}]}]` {
		t.Errorf("s2: %s %v", asJSON(t, h), err)
	}

	// Before 045: the episodes h2 and h6 cover hold nothing of their own but
	// c7's stray package.
	db.Exec(t, `ALTER TABLE com_nalet_katalog_items DROP COLUMN coveredby`)
	st = &Store{Pool: db.Pool}
	before := heldShowHoldings()
	before.Seasons[1].Episodes[2] = ep(3, "z3", false, false, "")
	before.Seasons[1].Episodes[3] = ep(4, "a4", false, false, "")
	before.Seasons[1].Episodes[5] = ep(7, "c7", true, true, "")
	if h, err := st.SeriesHoldings(ctx, []Identity{{"tmdb", "97546"}}); err != nil || asJSON(t, h) != asJSON(t, before) {
		t.Errorf("before 045: %v\n got %s\nwant %s", err, asJSON(t, h), asJSON(t, before))
	}
}

// No series carries the identity: one of no title, one only a film carries,
// one only an episode carries. Identities that name different series
// disagree, named in the order they were given.
func TestASeriesNotFoundOrDisagreedOn(t *testing.T) {
	st, _ := heldShow(t)
	ctx := context.Background()
	for _, ids := range [][]Identity{
		{{"tmdb", "4711"}}, {{"imdb", "tt0004711"}}, {{"tvdb", "7777777"}}, {{"tvdb", "1"}, {"imdb", "tt1"}},
	} {
		if h, err := st.SeriesHoldings(ctx, ids); !errors.Is(err, ErrNotFound) {
			t.Errorf("by %v: %s %v, want not found", ids, asJSON(t, h), err)
		}
	}
	for _, tc := range []struct {
		ids  []Identity
		want string
	}{
		{[]Identity{{"tmdb", "1399"}, {"tvdb", "362392"}}, "tmdb 1399 and tvdb 362392 name different series"},
		{[]Identity{{"tvdb", "362392"}, {"tmdb", "1399"}}, "tvdb 362392 and tmdb 1399 name different series"},
		{[]Identity{{"tmdb", "97546"}, {"tvdb", "362392"}, {"imdb", "tt0004711"}, {"tvdb", "1"}}, ""},
	} {
		_, err := st.SeriesHoldings(ctx, tc.ids)
		var d *Disagreement
		if got := ""; errors.As(err, &d) {
			got = d.Error()
			if got != tc.want {
				t.Errorf("by %v: %q, want %q", tc.ids, got, tc.want)
			}
		} else if tc.want != "" || err != nil {
			t.Errorf("by %v: %v, want %q", tc.ids, err, tc.want)
		}
	}
}

// A series the catalog holds twice, two series carrying its identity, is held
// as one: the first of them made, with the episodes of both, an episode both
// hold listed for each. An identity only one of them carries finds that one
// alone; with one both carry, both.
func TestASeriesHeldTwice(t *testing.T) {
	st, db := heldShow(t)
	ctx := context.Background()
	addItem(t, db, "s1b", "series", "A Show (2)", 2020, 8)
	identify(t, db, "s1b", "tmdb", "97546")
	addEpisode(t, db, "d1", "s1b", 1, 1)
	addEpisode(t, db, "d5", "s1b", 1, 5)
	addAsset(t, db, "a-d5", "d5", lib+"/media/shows/A Show (2)/A Show S01E05.mkv", true, "primary")
	db.Exec(t, `UPDATE com_nalet_katalog_items SET createdat = '2026-01-01' WHERE id = 's1'`)
	db.Exec(t, `UPDATE com_nalet_katalog_items SET createdat = '2026-02-01' WHERE id = 's1b'`)

	both := heldShowHoldings()
	eps := both.Seasons[1].Episodes
	both.Seasons[1].Episodes = append([]EpisodeHolding{ep(1, "d1", false, false, "")}, eps[:4]...)
	both.Seasons[1].Episodes = append(both.Seasons[1].Episodes, ep(5, "d5", true, false, ""))
	both.Seasons[1].Episodes = append(both.Seasons[1].Episodes, eps[4:]...)
	for _, tc := range []struct {
		ids  []Identity
		want SeriesHoldings
	}{
		{[]Identity{{"tmdb", "97546"}}, both},
		{[]Identity{{"tmdb", "97546"}, {"tvdb", "362392"}}, both},
		{[]Identity{{"tvdb", "362392"}}, heldShowHoldings()},
	} {
		h, err := st.SeriesHoldings(ctx, tc.ids)
		if err != nil || asJSON(t, h) != asJSON(t, tc.want) {
			t.Errorf("by %v: %v\n got %s\nwant %s", tc.ids, err, asJSON(t, h), asJSON(t, tc.want))
		}
	}

	db.Exec(t, `UPDATE com_nalet_katalog_items SET createdat = '2025-12-01' WHERE id = 's1b'`)
	h, err := st.SeriesHoldings(ctx, []Identity{{"tmdb", "97546"}})
	if err != nil || h.SeriesID != "s1b" || h.Title != "A Show (2)" || !reflect.DeepEqual(h.ExternalIDs, map[string]string{"tmdb": "97546"}) ||
		asJSON(t, h.Seasons) != asJSON(t, both.Seasons) {
		t.Errorf("s1b made first: %s %v, want s1b with the episodes of both", asJSON(t, h), err)
	}
}

// A movie's holding, found by any of its identities or by both: whether it has
// a file (one it was taken in from, or a package) and whether it plays
// (packaged). A show's identity finds no movie, though a movie carries the
// same tmdb number. Identities that name different movies disagree. A movie
// the catalog holds more than once is answered the one that plays, else one
// with a file, else the first made.
func TestAMoviesHolding(t *testing.T) {
	st, db := heldShow(t)
	ctx := context.Background()
	for id, title := range map[string]string{"m2": "Packaged", "m3": "Retired", "m4": "Nothing", "m5a": "Held Twice",
		"m5b": "Held Twice", "m6a": "Neither", "m6b": "Neither"} {
		addItem(t, db, id, "movie", title, 2024, 6)
	}
	for id, tmdb := range map[string]string{"m2": "4712", "m3": "4713", "m4": "4714", "m5a": "4715", "m5b": "4715",
		"m6a": "4716", "m6b": "4716"} {
		identify(t, db, id, "tmdb", tmdb)
	}
	db.Exec(t, `UPDATE com_nalet_katalog_items SET createdat = '2026-01-01' WHERE id IN ('m5a', 'm6a')`)
	db.Exec(t, `UPDATE com_nalet_katalog_items SET createdat = '2026-02-01' WHERE id IN ('m5b', 'm6b')`)
	addAsset(t, db, "a-m2", "m2", lib+"/media/Packaged (2024).mkv", true, "primary")
	addAsset(t, db, "p-m2", "m2", lib+"/packages/movies/m2/m2/manifest.json", false, "packaged")
	addAsset(t, db, "o-m3", "m3", lib+"/movies/m3/m3/sources/src3", false, "original")
	addAsset(t, db, "p-m3", "m3", lib+"/movies/m3/m3/versions/v1/package.json", false, "packaged")
	addAsset(t, db, "a-m5a", "m5a", lib+"/media/Held Twice (2024).mkv", true, "primary")
	addAsset(t, db, "p-m5b", "m5b", lib+"/movies/m5b/m5b/versions/v1/package.json", false, "packaged")

	held := func(id, title string, y int, ids map[string]string, file, plays bool) string {
		return asJSON(t, MovieHolding{ItemID: id, Title: title, Year: year(y), ExternalIDs: ids, HasFile: file, Playable: plays})
	}
	film := held("m1", "A Film", 2001, map[string]string{"tmdb": "97546", "imdb": "tt0004711"}, true, false)
	for _, tc := range []struct {
		ids  []Identity
		want string
	}{
		{[]Identity{{"tmdb", "97546"}}, film},
		{[]Identity{{"imdb", "tt0004711"}}, film},
		{[]Identity{{"imdb", "tt0004711"}, {"tmdb", "97546"}}, film},
		{[]Identity{{"tmdb", "4712"}}, held("m2", "Packaged", 2024, map[string]string{"tmdb": "4712"}, true, true)},
		{[]Identity{{"tmdb", "4713"}}, held("m3", "Retired", 2024, map[string]string{"tmdb": "4713"}, true, true)},
		{[]Identity{{"tmdb", "4714"}}, held("m4", "Nothing", 2024, map[string]string{"tmdb": "4714"}, false, false)},
		{[]Identity{{"tmdb", "4715"}}, held("m5b", "Held Twice", 2024, map[string]string{"tmdb": "4715"}, true, true)},
		{[]Identity{{"tmdb", "4716"}}, held("m6a", "Neither", 2024, map[string]string{"tmdb": "4716"}, false, false)},
	} {
		m, err := st.MovieHolding(ctx, tc.ids)
		if err != nil || asJSON(t, m) != tc.want {
			t.Errorf("by %v: %v\n got %s\nwant %s", tc.ids, err, asJSON(t, m), tc.want)
		}
	}
	for _, ids := range [][]Identity{{{"tmdb", "1399"}}, {{"imdb", "tt9208876"}}, {{"tmdb", "1"}}} {
		if m, err := st.MovieHolding(ctx, ids); !errors.Is(err, ErrNotFound) {
			t.Errorf("by %v: %s %v, want not found", ids, asJSON(t, m), err)
		}
	}
	_, err := st.MovieHolding(ctx, []Identity{{"tmdb", "4712"}, {"imdb", "tt0004711"}})
	if d := (*Disagreement)(nil); !errors.As(err, &d) || d.Error() != "tmdb 4712 and imdb tt0004711 name different movies" {
		t.Errorf("tmdb 4712 and imdb tt0004711: %v, want them to disagree", err)
	}
}

// Every title of a type, a page at a time in id order, with its identities
// ({} when there are none) and, of a series, how many of its episodes have a
// file, as its holdings count them; how many titles there are of the type. A
// page past the last is an error that says which is the last; page 1 of a type
// of no title is there, empty. Before 045 a series counts what its episodes
// hold themselves.
func TestTheHoldingsListing(t *testing.T) {
	st, db := heldShow(t)
	ctx := context.Background()
	series := func(page, size int) string {
		t.Helper()
		p, err := st.ListHoldings(ctx, "series", page, size)
		if err != nil {
			return err.Error()
		}
		return asJSON(t, p)
	}
	const (
		s1 = `{"itemId":"s1","title":"A Show","year":2020,"externalIds":{"imdb":"tt9208876","tmdb":"97546","tvdb":"362392"},"heldEpisodes":8}`
		s2 = `{"itemId":"s2","title":"Another Show","year":2011,"externalIds":{"tmdb":"1399"},"heldEpisodes":1}`
		s3 = `{"itemId":"s3","title":"A Show Nobody Identified","year":2015,"externalIds":{},"heldEpisodes":0}`
	)
	for _, tc := range []struct {
		page, size int
		want       string
	}{
		{1, 200, `{"items":[` + s1 + `,` + s2 + `,` + s3 + `],"page":1,"pageSize":200,"total":3}`},
		{1, 2, `{"items":[` + s1 + `,` + s2 + `],"page":1,"pageSize":2,"total":3}`},
		{2, 2, `{"items":[` + s3 + `],"page":2,"pageSize":2,"total":3}`},
		{3, 2, `page 3 is past the last page, 2: 3 series at 2 a page`},
		{2, 3, `page 2 is past the last page, 1: 3 series at 3 a page`},
	} {
		if got := series(tc.page, tc.size); got != tc.want {
			t.Errorf("page %d at %d:\n got %s\nwant %s", tc.page, tc.size, got, tc.want)
		}
	}
	p, err := st.ListHoldings(ctx, "movie", 1, 200)
	if got, want := asJSON(t, p), `{"items":[{"itemId":"m1","title":"A Film","year":2001,`+
		`"externalIds":{"imdb":"tt0004711","tmdb":"97546"}}],"page":1,"pageSize":200,"total":1}`; err != nil || got != want {
		t.Errorf("the movies: %v\n got %s\nwant %s", err, got, want)
	}

	db.Exec(t, `ALTER TABLE com_nalet_katalog_items DROP COLUMN coveredby`)
	st = &Store{Pool: db.Pool}
	if p, err := st.ListHoldings(ctx, "series", 1, 1); err != nil || p.Items[0].HeldEpisodes == nil || *p.Items[0].HeldEpisodes != 6 {
		t.Errorf("before 045: %s %v, want s1 with 6 held episodes (e0, e1, h2, h6, c7, p21)", asJSON(t, p), err)
	}

	empty, _ := open(t)
	if p, err := empty.ListHoldings(ctx, "movie", 1, 200); err != nil || asJSON(t, p) != `{"items":[],"page":1,"pageSize":200,"total":0}` {
		t.Errorf("no movies: %s %v", asJSON(t, p), err)
	}
	var past *PastTheLastPage
	if _, err := empty.ListHoldings(ctx, "movie", 2, 200); !errors.As(err, &past) || past.Last != 1 {
		t.Errorf("no movies, page 2: %v, want past the last page, 1", err)
	}
}

// The holdings are the library's, not a viewer's: a read for them ignores the
// rating cap its context carries, and a title rated above it, or rated by
// nothing, is in them as in any other.
func TestHoldingsIgnoreTheCap(t *testing.T) {
	st, _ := heldShow(t)
	ctx := WithMaxAge(context.Background(), 0)
	if h, err := st.SeriesHoldings(ctx, []Identity{{"tmdb", "97546"}}); err != nil || asJSON(t, h) != asJSON(t, heldShowHoldings()) {
		t.Errorf("the show rated 16, capped at 0: %s %v", asJSON(t, h), err)
	}
	if m, err := st.MovieHolding(ctx, []Identity{{"tmdb", "97546"}}); err != nil || m.ItemID != "m1" {
		t.Errorf("the film rated 18, capped at 0: %s %v", asJSON(t, m), err)
	}
	if p, err := st.ListHoldings(ctx, "series", 1, 200); err != nil || p.Total != 3 || len(p.Items) != 3 {
		t.Errorf("the series, capped at 0, s3 rated by nothing: %s %v", asJSON(t, p), err)
	}
}

// While the role may not read the catalog's identities, the holdings are an
// error that says so, never an answer that the library holds nothing; once it
// may, they are read, without a restart.
func TestHoldingsWithoutTheIdentities(t *testing.T) {
	_, db := heldShow(t)
	role, pool := db.Role(t)
	st := &Store{Pool: pool}
	ctx := context.Background()
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_items, com_nalet_katalog_playbackassets TO `+role)
	tmdb := []Identity{{"tmdb", "97546"}}
	if _, err := st.SeriesHoldings(ctx, tmdb); !errors.Is(err, ErrNoIdentities) {
		t.Errorf("a series: %v, want %v", err, ErrNoIdentities)
	}
	if _, err := st.MovieHolding(ctx, tmdb); !errors.Is(err, ErrNoIdentities) {
		t.Errorf("a movie: %v, want %v", err, ErrNoIdentities)
	}
	if _, err := st.ListHoldings(ctx, "series", 1, 200); !errors.Is(err, ErrNoIdentities) {
		t.Errorf("the listing: %v, want %v", err, ErrNoIdentities)
	}
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_itemexternalids TO `+role)
	if h, err := st.SeriesHoldings(ctx, tmdb); err != nil || asJSON(t, h) != asJSON(t, heldShowHoldings()) {
		t.Errorf("granted: %s %v", asJSON(t, h), err)
	}
}
