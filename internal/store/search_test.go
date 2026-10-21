package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// title is a title of the search fixture: an id, a type, a title, a year, a
// rating (0: none), a plot, and for an episode its series, season and number.
type title struct {
	id, typ, title string
	year           int
	rating         float64
	plot           string
	series         string
	season, number int
}

// searchFixture is a small catalog with the cases a search must rank: a
// query in a title's beginning, inside it, only in plots, a typo, accents,
// apostrophes, marks inside a word, an episode and its series, a series' other
// name, and people.
var searchFixture = []title{
	{id: "black-widow", typ: "movie", title: "Black Widow", year: 2021, rating: 6.7,
		plot: "Natasha Romanoff confronts the darker parts of her ledger."},
	{id: "winter-soldier", typ: "movie", title: "Captain America: The Winter Soldier", year: 2014, rating: 7.7,
		plot: "Steve Rogers teams up with Black Widow to battle a new threat."},
	{id: "harbour", typ: "movie", title: "Harbour Lights", year: 2001, rating: 6.1,
		plot: "A young widow returns to the fishermen of her village by the sea."},
	{id: "long-winter", typ: "movie", title: "The Long Winter", year: 1999, rating: 6.5,
		plot: "He meets a widow who changes everything."},
	{id: "rear-window", typ: "movie", title: "Rear Window", year: 1954, rating: 8.5,
		plot: "A photographer in a wheelchair spies on his neighbours."},
	{id: "neverland", typ: "movie", title: "Finding Neverland", year: 2004, rating: 7.3,
		plot: "The playwright and the family that inspired a boy who would not grow up."},
	{id: "leon", typ: "movie", title: "Léon", year: 1994, rating: 8.3, plot: "A hitman takes in a girl."},
	{id: "leon-morin", typ: "movie", title: "Leon Morin, Priest", year: 1961, rating: 7.2, plot: "A widow falls for a priest."},
	{id: "up", typ: "movie", title: "Up", year: 2009, rating: 8.3, plot: "An old man flies his house to South America."},
	{id: "up-air", typ: "movie", title: "Up in the Air", year: 2009, rating: 7.4, plot: "A man who fires people for a living."},
	{id: "grown-ups", typ: "movie", title: "Grown Ups", year: 2010, rating: 6.0, plot: "Five friends meet up again."},
	{id: "heat", typ: "movie", title: "Heat", year: 1995, rating: 8.3, plot: "A detective hunts a crew of thieves."},
	{id: "the-heat", typ: "movie", title: "The Heat", year: 2013, rating: 6.6, plot: "Two detectives take down a drug lord."},
	{id: "spider-man", typ: "movie", title: "Spider-Man", year: 2002, rating: 7.4, plot: "A student is bitten by a spider."},
	{id: "homecoming", typ: "movie", title: "Spider-Man: Homecoming", year: 2017, rating: 7.4, plot: "A young hero returns to school."},
	{id: "widows-bay", typ: "series", title: "Widow's Bay", year: 2026, rating: 7.4,
		plot: "A small island town and the secrets its mayor keeps."},
	{id: "breaking-bad", typ: "series", title: "Breaking Bad", year: 2008, rating: 9.5,
		plot: "A chemistry teacher turns to making drugs."},
	{id: "bb-ozymandias", typ: "episode", title: "Ozymandias", series: "breaking-bad", season: 5, number: 14,
		plot: "Everyone copes with radically changed circumstances."},
	{id: "bb-bad-blood", typ: "episode", title: "Bad Blood", series: "breaking-bad", season: 1, number: 2},
	{id: "wb-pilot", typ: "episode", title: "The Widow's Walk", series: "widows-bay", season: 1, number: 1},
	{id: "money-heist", typ: "series", title: "Money Heist", year: 2017, rating: 8.2, plot: "Eight thieves take hostages."},
	{id: "fisherman", typ: "movie", title: "Fisherman's Friends", year: 2019, rating: 6.6,
		plot: "Ten fishermen from a harbour town sing."},
}

// searchPeople are the fixture's people and the titles they are credited on.
var searchPeople = []struct {
	name   string
	titles []string
}{
	{"Scarlett Johansson", []string{"black-widow", "winter-soldier", "up-air"}},
	{"Bryan Cranston", []string{"breaking-bad"}},
	{"Zoë Kravitz", []string{"the-heat"}},
	{"Al Pacino", []string{"heat"}},
	{"Pacino Example", []string{"up"}},
}

// searchFixtureCatalog is the fixture on a catalog with 049 (and 030, 032, 036, 046,
// which it reads).
func searchFixtureCatalog(t *testing.T) (*Store, *storetest.DB) {
	t.Helper()
	st, db := open(t)
	db.Migrate030(t)
	db.Migrate032(t)
	db.Migrate036(t)
	db.Migrate046(t)
	addSearchFixture(t, db)
	db.Migrate049(t)
	return st, db
}

func addSearchFixture(t *testing.T, db *storetest.DB) {
	t.Helper()
	for _, f := range searchFixture {
		var (
			year, season, number *int
			rating               *float64
			parent               *string
		)
		if f.year != 0 {
			year = &f.year
		}
		if f.rating != 0 {
			rating = &f.rating
		}
		if f.series != "" {
			parent, season, number = &f.series, &f.season, &f.number
		}
		db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, sorttitle, year, rating, description,
			parent_id, seasonnumber, episodenumber) VALUES ($1, $2, $3::text, lower($3::text), $4, $5, NULLIF($6::text, ''), $7, $8, $9)`,
			f.id, f.typ, f.title, year, rating, f.plot, parent, season, number)
	}
	// The library files the series under the name its folder gives it.
	db.Exec(t, `UPDATE com_nalet_katalog_items SET scantitle = 'haus des geldes' WHERE id = 'money-heist'`)
	for _, p := range searchPeople {
		for _, item := range p.titles {
			credit(t, db, item, p.name, "actor")
		}
	}
}

// search runs a search and answers its titles as "id:match" each.
func search(t *testing.T, st *Store, q string, types ...string) []string {
	t.Helper()
	res, err := st.SearchItems(context.Background(), SearchOpts{Query: q, Types: types, Limit: 10})
	if err != nil {
		t.Fatalf("search %q: %v", q, err)
	}
	var out []string
	for _, it := range res.Items {
		out = append(out, it.ID+":"+it.Match)
	}
	return out
}

func firstN(got []string, n int) []string {
	return got[:min(n, len(got))]
}

// The golden queries: each answers its titles, best first, as a viewer
// should see them.
func TestGoldenSearches(t *testing.T) {
	st, _ := searchFixtureCatalog(t)
	films := []string{"movie", "series"}
	for _, c := range []struct {
		why, q string
		types  []string
		want   string // the titles first, in this order
		absent string // titles that must not be found
	}{
		{why: "a word in two titles: both, the one it begins first, no plot", q: "widow", types: films,
			want: "widows-bay:prefix black-widow:words", absent: "winter-soldier harbour long-winter leon-morin rear-window"},
		{why: "as the viewer wrote it, without the apostrophe", q: "widows bay", types: films, want: "widows-bay:exact"},
		{why: "with the apostrophe", q: "widow's bay", types: films, want: "widows-bay:exact"},
		{why: "with an acute accent for an apostrophe", q: "Widow´s Bay", types: films, want: "widows-bay:exact"},
		{why: "while typing", q: "wido", types: films, want: "widows-bay:prefix black-widow:words"},
		{why: "an exact title beats a longer one that begins with it", q: "up", types: films,
			want: "up:exact up-air:prefix", absent: "grown-ups"},
		{why: "an exact title beats a longer one that holds it", q: "heat", types: films, want: "heat:exact the-heat:words"},
		{why: "a typo", q: "nevrland", types: films, want: "neverland:similar"},
		{why: "a typo in one of two words", q: "findng neverland", types: films, want: "neverland:similar"},
		{why: "accents off", q: "leon", types: films, want: "leon:exact leon-morin:prefix"},
		{why: "accents in the query", q: "Léon", types: films, want: "leon:exact leon-morin:prefix"},
		{why: "a mark inside a word", q: "spiderman", types: films, want: "spider-man:exact homecoming:prefix"},
		{why: "the same, written apart", q: "spider man", types: films, want: "spider-man:exact homecoming:prefix"},
		{why: "words in any order", q: "widow black", types: films, want: "black-widow:words"},
		{why: "a series' other name", q: "haus des geldes", types: films, want: "money-heist:alternative"},
		{why: "a word only plots hold: the plots", q: "village sea", types: films, want: "harbour:plot"},
		{why: "a title close to the query, its plot holding it too: once, as a typo, then the plots", q: "fishermen",
			types: films, want: "fisherman:similar harbour:plot"},
		{why: "an episode's title: the episode", q: "ozymandias", want: "bb-ozymandias:exact"},
		{why: "an episode after the films and series", q: "widow", want: "widows-bay:prefix black-widow:words wb-pilot:words"},
		{why: "nothing", q: "zzyzx", types: films, want: ""},
		{why: "a query far longer than a title is read as its beginning", q: "Black Widow " + strings.Repeat("x", 500),
			types: films, want: ""},
		{why: "... which is the title", q: "black widow" + strings.Repeat(" ", 300) + "zzz", types: films, want: "black-widow:exact"},
		{why: "marks alone", q: "?!", types: films, want: ""},
	} {
		got := search(t, st, c.q, c.types...)
		want := strings.Fields(c.want)
		if strings.Join(firstN(got, len(want)), " ") != c.want {
			t.Errorf("%s: %q found %q, want %q first", c.why, c.q, got, c.want)
		}
		for _, id := range strings.Fields(c.absent) {
			for _, g := range got {
				if strings.HasPrefix(g, id+":") {
					t.Errorf("%s: %q found %s", c.why, c.q, g)
				}
			}
		}
	}
}

// A typo is found while the titles do not fill a page, and a word that only
// begins a title does not bring every title close to it.
func TestTyposDoNotFloodASearch(t *testing.T) {
	st, _ := searchFixtureCatalog(t)
	for _, q := range []string{"widow", "widw", "wid"} {
		for _, g := range search(t, st, q, "movie", "series") {
			if strings.HasPrefix(g, "rear-window:") {
				t.Errorf("%q found Rear Window", q)
			}
		}
	}
	// "mary sm": a short last word must begin a word of the title.
	if got := search(t, st, "widow b", "movie", "series"); strings.Join(got, " ") != "widows-bay:prefix black-widow:words" {
		t.Errorf("widow b: %q", got)
	}
	if got := search(t, st, "breaking bx", "movie", "series"); len(got) != 0 {
		t.Errorf("breaking bx: %q, want nothing: bx begins no word and is too short for a typo", got)
	}
}

// A query shorter than three characters reads the beginnings of titles.
func TestAShortQueryReadsBeginnings(t *testing.T) {
	st, _ := searchFixtureCatalog(t)
	if got := search(t, st, "u", "movie", "series"); strings.Join(got, " ") != "up:prefix up-air:prefix" {
		t.Errorf("u: %q", got)
	}
}

// An episode names its series, and whether the series matched by its own
// title; a search of films and series finds no episode.
func TestAnEpisodeNamesItsSeries(t *testing.T) {
	st, _ := searchFixtureCatalog(t)
	ctx := context.Background()
	res, err := st.SearchItems(ctx, SearchOpts{Query: "bad"})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range res.Items {
		s := it.ID + ":" + it.Match
		if it.Series != nil {
			s += fmt.Sprintf(" of %s %q matched=%v S%dE%d", it.Series.ID, it.Series.Title, it.Series.Matched,
				*it.SeasonNumber, *it.EpisodeNumber)
		}
		got = append(got, s)
	}
	want := []string{"breaking-bad:words", `bb-bad-blood:prefix of breaking-bad "Breaking Bad" matched=true S1E2`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("bad: %q, want %q", got, want)
	}
	res, _ = st.SearchItems(ctx, SearchOpts{Query: "ozymandias"})
	if len(res.Items) != 1 || res.Items[0].Series == nil || res.Items[0].Series.Matched || res.Items[0].Series.Title != "Breaking Bad" {
		t.Errorf("ozymandias: %+v", res.Items)
	}
	if got := search(t, st, "ozymandias", "movie", "series"); len(got) != 0 {
		t.Errorf("ozymandias among films and series: %q", got)
	}
}

// People are searched apart, ranked by how their names match, then by how
// many titles credit them.
func TestGoldenPeopleSearches(t *testing.T) {
	st, _ := searchFixtureCatalog(t)
	for _, c := range []struct{ q, want string }{
		{"scarlett johansson", "Scarlett Johansson:exact"},
		{"johansson", "Scarlett Johansson:words"},
		{"scar", "Scarlett Johansson:prefix"},
		{"zoe", "Zoë Kravitz:prefix"},
		{"pacino", "Pacino Example:prefix Al Pacino:words"},
		{"cranstn", "Bryan Cranston:similar"},
		{"widow", ""},
	} {
		people, err := st.SearchPeopleRanked(context.Background(), c.q, 5)
		if err != nil {
			t.Fatalf("%q: %v", c.q, err)
		}
		var got []string
		for _, p := range people {
			got = append(got, p.Name+":"+p.Match)
		}
		if strings.Join(got, " ") != c.want {
			t.Errorf("people %q: %q, want %q", c.q, got, c.want)
		}
	}
}

// A capped viewer's search leaves out what the cap does not allow, an
// episode by its series' rating.
func TestACappedRankedSearch(t *testing.T) {
	st, db := searchFixtureCatalog(t)
	db.Exec(t, `UPDATE com_nalet_katalog_items SET min_age = 0 WHERE type <> 'episode'`)
	db.Exec(t, `UPDATE com_nalet_katalog_items SET min_age = 16 WHERE id IN ('black-widow', 'widows-bay')`)
	capped := WithMaxAge(context.Background(), 12)
	// No title the viewer may see matches: the plots do.
	res, err := st.SearchItems(capped, SearchOpts{Query: "widow"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) == 0 || res.Total != len(res.Items) {
		t.Errorf("capped at 12, widow: %d titles, total %d", len(res.Items), res.Total)
	}
	for _, it := range res.Items {
		if it.Match != "plot" || it.ID == "black-widow" || it.ID == "widows-bay" || it.ID == "wb-pilot" {
			t.Errorf("capped at 12, widow found %s (%s)", it.ID, it.Match)
		}
	}
	people, err := st.SearchPeopleRanked(capped, "johansson", 5)
	if err != nil || len(people) != 1 || people[0].Credits != 2 {
		t.Errorf("capped at 12, johansson: %+v %v, want her two titles the cap allows", people, err)
	}
}

// Until katalog-manager has built 049 a search matches as the list endpoints
// did: by the catalog's search vector where it has one, else by the title.
func TestASearchBefore049(t *testing.T) {
	st, db := open(t)
	db.Migrate046(t)
	addSearchFixture(t, db)
	if got := search(t, st, "Widow"); strings.Join(firstN(got, 1), " ") != "widows-bay:prefix" {
		t.Errorf("by the title: %q", got)
	}
	db.Exec(t, `ALTER TABLE com_nalet_katalog_items ADD COLUMN search_vector tsvector
		GENERATED ALWAYS AS (to_tsvector('simple', coalesce(title, '') || ' ' || coalesce(description, ''))) STORED`)
	got := search(t, st, "widow")
	if len(got) < 3 || got[0] != "widows-bay:prefix" {
		t.Errorf("by the search vector: %q", got)
	}
	people, err := st.SearchPeopleRanked(context.Background(), "johansson", 5)
	if err != nil || len(people) != 1 {
		t.Errorf("people before 049: %+v %v", people, err)
	}
}
