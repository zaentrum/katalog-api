package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// ratedCatalog is a catalog with migration 036 whose titles cover what a
// rating can be, each "id: what rates it":
//
//	m0, m12, m16: films TMDB certifies 0, 12 and 16 in DE
//	m17:          a film certified R in the US, 17
//	mu:           a film nothing rates
//	mo:           a film certified 18 in DE that an admin rated 6
//	s12:          a series certified 12 in DE, with
//	  e1:           an episode rated as it
//	  e2:           an episode an admin rated 18
//	s16:          a series certified TV-14 in the US that an admin rated 16, with
//	  e3:           an episode rated as it
//	eo:           an episode whose series is not in the catalog
//
// Every title is a drama with Ada Example in it, so it is like every other
// and in her filmography.
func ratedCatalog(t *testing.T) (*Store, *storetest.DB) {
	t.Helper()
	st, db := open(t)
	db.Migrate030(t)
	db.Migrate032(t)
	db.Migrate036(t)
	for _, it := range []struct {
		id, typ, title, parent string
		year                   int
	}{
		{"m0", "movie", "Zero", "", 2000}, {"m12", "movie", "Twelve", "", 2001}, {"m16", "movie", "Sixteen", "", 2002},
		{"m17", "movie", "Seventeen", "", 2003}, {"mu", "movie", "Unrated", "", 2004}, {"mo", "movie", "Overridden", "", 2005},
		{"s12", "series", "Kids Show", "", 2006}, {"e1", "episode", "Pilot", "s12", 2006}, {"e2", "episode", "Finale", "s12", 2006},
		{"s16", "series", "Show", "", 2007}, {"e3", "episode", "Opener", "s16", 2007}, {"eo", "episode", "Orphan", "gone", 2008},
	} {
		db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, sorttitle, year, parent_id, seasonnumber, episodenumber, createdat)
			VALUES ($1::varchar, $2::varchar, $3::varchar, $3::varchar, $4::int, NULLIF($5::varchar, ''),
			        CASE WHEN $2::varchar = 'episode' THEN 1 END, CASE WHEN $2::varchar = 'episode' THEN length($1::varchar) END,
			        now() - make_interval(days => $4::int - 1990))`,
			it.id, it.typ, it.title, it.year, it.parent)
		db.Exec(t, `INSERT INTO com_nalet_katalog_itemgenres (id, item_id, genre_id) VALUES ('g-' || $1::varchar, $1, 'drama')`, it.id)
		db.Exec(t, `INSERT INTO com_nalet_katalog_itempeople (id, item_id, person_id, role) VALUES ('c-' || $1::varchar, $1, 'ada', 'actor')`, it.id)
	}
	db.Exec(t, `INSERT INTO com_nalet_katalog_genres (id, name) VALUES ('drama', 'Drama')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_people (id, name) VALUES ('ada', 'Ada Example')`)
	db.Exec(t, `UPDATE com_nalet_katalog_items i SET certification = r.c, certification_country = r.country, min_age = r.age
		FROM (VALUES ('m0', '0', 'DE', 0), ('m12', '12', 'DE', 12), ('m16', '16', 'DE', 16), ('m17', 'R', 'US', 17),
		             ('mo', '18', 'DE', 18), ('s12', '12', 'DE', 12), ('s16', 'TV-14', 'US', 14)) AS r(id, c, country, age)
		WHERE i.id = r.id`)
	db.Exec(t, `UPDATE com_nalet_katalog_items SET min_age_override = o.age
		FROM (VALUES ('mo', 6), ('e2', 18), ('s16', 16)) AS o(id, age) WHERE com_nalet_katalog_items.id = o.id`)
	return st, db
}

// ratingOf is what an item says of its rating: "age certification country",
// "-" for each unknown.
func ratingOf(it Item) string {
	age := "-"
	if it.MinAge != nil {
		age = fmt.Sprint(*it.MinAge)
	}
	cert, country := it.Certification, it.CertificationCountry
	if cert == "" {
		cert = "-"
	}
	if country == "" {
		country = "-"
	}
	return age + " " + cert + " " + country
}

// ratingsOf is each item's id and its rating, sorted.
func ratingsOf(items []Item) string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID+": "+ratingOf(it))
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// Every read of an item says what it is rated: the age a viewer must be, and
// the certification it comes from with its country; an episode its series',
// and none where an admin rated it by hand.
func TestAnItemSaysWhatItIsRated(t *testing.T) {
	st, _ := ratedCatalog(t)
	ctx := context.Background()
	want := map[string]string{
		"m0": "0 0 DE", "m12": "12 12 DE", "m16": "16 16 DE", "m17": "17 R US", "mu": "- - -", "mo": "6 - -",
		"s12": "12 12 DE", "e1": "12 12 DE", "e2": "18 - -", "s16": "16 - -", "e3": "16 - -", "eo": "- - -",
	}
	for id, w := range want {
		it, err := st.GetItem(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got := ratingOf(it); got != w {
			t.Errorf("GetItem(%s): %s, want %s", id, got, w)
		}
	}
	wantOf := func(ids ...string) string {
		var out []string
		for _, id := range ids {
			out = append(out, id+": "+want[id])
		}
		sort.Strings(out)
		return strings.Join(out, ", ")
	}
	list, err := st.ListItems(ctx, ListOpts{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got, w := ratingsOf(list.Items), wantOf("m0", "m12", "m16", "m17", "mu", "mo", "s12", "e1", "e2", "s16", "e3", "eo"); got != w {
		t.Errorf("ListItems:\n got  %s\n want %s", got, w)
	}
	eps, err := st.ListEpisodesBySeries(ctx, "s12")
	if err != nil {
		t.Fatal(err)
	}
	if got, w := ratingsOf(eps), wantOf("e1", "e2"); got != w {
		t.Errorf("the episodes of s12:\n got  %s\n want %s", got, w)
	}
	pd, err := st.GetPerson(ctx, "ada", 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, w := ratingsOf(pd.Items), wantOf("m0", "m12", "m16", "m17", "mu", "mo", "s12", "e1", "e2", "s16", "e3", "eo"); got != w {
		t.Errorf("Ada's filmography:\n got  %s\n want %s", got, w)
	}
	similar, err := st.ListSimilar(ctx, "m12", 50)
	if err != nil {
		t.Fatal(err)
	}
	if got, w := ratingsOf(similar), wantOf("m0", "m16", "m17", "mu", "mo"); got != w {
		t.Errorf("like m12:\n got  %s\n want %s", got, w)
	}
}

// On a catalog without migration 036, or with a role that may not read its
// columns, an item says nothing of a rating.
func TestAnItemWithoutRatings(t *testing.T) {
	st, db := open(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	if it, err := st.GetItem(context.Background(), "m1"); err != nil || ratingOf(it) != "- - -" {
		t.Errorf("without 036: %s %v", ratingOf(it), err)
	}

	db.Migrate036(t)
	db.Exec(t, `UPDATE com_nalet_katalog_items SET certification = '12', certification_country = 'DE', min_age = 12`)
	role, pool := db.Role(t)
	db.Exec(t, `GRANT SELECT (id, type, title, sorttitle, year, rating, description, tagline, durationms, seasonnumber,
		episodenumber, parent_id) ON com_nalet_katalog_items TO `+role)
	ro := &Store{Pool: pool}
	if it, err := ro.GetItem(context.Background(), "m1"); err != nil || ratingOf(it) != "- - -" {
		t.Errorf("a role that may not read the ratings: %s %v", ratingOf(it), err)
	}
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_items TO `+role)
	if it, err := ro.GetItem(context.Background(), "m1"); err != nil || ratingOf(it) != "12 12 DE" {
		t.Errorf("once the role may read them: %s %v", ratingOf(it), err)
	}
}
