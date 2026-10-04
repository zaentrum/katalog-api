package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

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
//	  e4:           an episode an admin rated 6
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
		{"s16", "series", "Show", "", 2007}, {"e3", "episode", "Opener", "s16", 2007}, {"e4", "episode", "Second", "s16", 2007},
		{"eo", "episode", "Orphan", "gone", 2008},
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
		FROM (VALUES ('mo', 6), ('e2', 18), ('s16', 16), ('e4', 6)) AS o(id, age) WHERE com_nalet_katalog_items.id = o.id`)
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
		"s12": "12 12 DE", "e1": "12 12 DE", "e2": "18 - -", "s16": "16 - -", "e3": "16 - -", "e4": "6 - -", "eo": "- - -",
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
	if got, w := ratingsOf(list.Items), wantOf("m0", "m12", "m16", "m17", "mu", "mo", "s12", "e1", "e2", "s16", "e3", "e4", "eo"); got != w {
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
	if got, w := ratingsOf(pd.Items), wantOf("m0", "m12", "m16", "m17", "mu", "mo", "s12", "e1", "e2", "s16", "e3", "e4", "eo"); got != w {
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

// ratedAges are the ages the titles of ratedCatalog are held to, as
// katalog-manager defines them, worked out by hand: -1 is unrated.
var ratedAges = map[string]int{"m0": 0, "m12": 12, "m16": 16, "m17": 17, "mu": -1, "mo": 6,
	"s12": 12, "e1": 12, "e2": 18, "s16": 16, "e3": 16, "e4": 6, "eo": -1}

// allowed is what a viewer capped at maxAge may be served of ids: a title
// rated at most the cap, an unrated one when show.
func allowed(maxAge int, show bool, ids ...string) string {
	var out []string
	for _, id := range ids {
		if age := ratedAges[id]; (age < 0 && show) || (age >= 0 && age <= maxAge) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func idsOf(items []Item) string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// setUnrated sets ratings.unrated_for_capped, and has the store read it anew.
func setUnrated(t *testing.T, st *Store, db *storetest.DB, value string) {
	t.Helper()
	db.Exec(t, `DELETE FROM com_nalet_katalog_settings WHERE key = 'ratings.unrated_for_capped'`)
	if value != "" {
		db.Exec(t, `INSERT INTO com_nalet_katalog_settings (id, key, valuetext) VALUES ('u', 'ratings.unrated_for_capped', $1)`, value)
	}
	st.unrated.mu.Lock()
	st.unrated.until = time.Time{}
	st.unrated.mu.Unlock()
}

// A capped viewer is served, of every read, what its cap allows: the titles
// rated at most the cap, an episode as its series unless an admin rated it,
// and the unrated ones only while ratings.unrated_for_capped says show; a
// list's total counts nothing else, and a title by id it may not be served
// is not found. At each cap around the ages the catalog holds, the store
// serves what allowed works out by hand.
func TestTheCapLeavesOutWhatItDoesNotAllow(t *testing.T) {
	st, db := ratedCatalog(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_mediasegments (id, item_id, kind, startms, endms, source) VALUES
		('seg-m12', 'm12', 'intro', 0, 1000, 'manual'), ('seg-m16', 'm16', 'intro', 0, 1000, 'manual')`)
	everything := []string{"m0", "m12", "m16", "m17", "mu", "mo", "s12", "e1", "e2", "s16", "e3", "e4", "eo"}
	for _, setting := range []string{"", "hide", "show", "SHOW ", "yes"} {
		setUnrated(t, st, db, setting)
		show := strings.EqualFold(strings.TrimSpace(setting), "show")
		for _, maxAge := range []int{0, 5, 6, 11, 12, 13, 15, 16, 17, 18, 21, 99} {
			ctx := WithMaxAge(context.Background(), maxAge)
			at := fmt.Sprintf("capped at %d, unrated %q", maxAge, setting)

			for typ, ids := range map[string][]string{"": everything, "movie": {"m0", "m12", "m16", "m17", "mu", "mo"},
				"series": {"s12", "s16"}, "episode": {"e1", "e2", "e3", "e4", "eo"}} {
				res, err := st.ListItems(ctx, ListOpts{Type: typ, Limit: 200})
				if err != nil {
					t.Fatal(err)
				}
				want := allowed(maxAge, show, ids...)
				if got := idsOf(res.Items); got != want || res.Total != len(strings.Fields(want)) {
					t.Errorf("%s, the list of type %q: %q (total %d), want %q", at, typ, got, res.Total, want)
				}
			}
			for _, id := range everything {
				_, err := st.GetItem(ctx, id)
				if want := allowed(maxAge, show, id) != ""; (err == nil) != want || (err != nil && !errors.Is(err, ErrNotFound)) {
					t.Errorf("%s, GetItem(%s): %v, want served %v", at, id, err, want)
				}
			}
			for series, eps := range map[string][]string{"s12": {"e1", "e2"}, "s16": {"e3", "e4"}} {
				got, err := st.ListEpisodesBySeries(ctx, series)
				switch {
				case allowed(maxAge, show, series) == "":
					if !errors.Is(err, ErrNotFound) {
						t.Errorf("%s, the episodes of %s, which the cap leaves out: %v %v, want not found", at, series, idsOf(got), err)
					}
				case err != nil || idsOf(got) != allowed(maxAge, show, eps...):
					t.Errorf("%s, the episodes of %s: %q %v, want %q", at, series, idsOf(got), err, allowed(maxAge, show, eps...))
				}
			}
			for _, id := range []string{"m12", "m16"} {
				segs, err := st.ListSegments(ctx, id)
				if want := allowed(maxAge, show, id) != ""; want != (err == nil && len(segs) == 1) || (!want && !errors.Is(err, ErrNotFound)) {
					t.Errorf("%s, the segments of %s: %v %v, want served %v", at, id, segs, err, want)
				}
			}
			similar, err := st.ListSimilar(ctx, "m0", 50)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := idsOf(similar), allowed(maxAge, show, "m12", "m16", "m17", "mu", "mo"); got != want {
				t.Errorf("%s, like m0: %q, want %q", at, got, want)
			}
			if _, err := st.ListSimilar(ctx, "m16", 50); (err == nil) != (allowed(maxAge, show, "m16") != "") {
				t.Errorf("%s, like m16: %v", at, err)
			}
			pd, err := st.GetPerson(ctx, "ada", 100, nil)
			if err != nil || pd == nil {
				t.Fatalf("%s, Ada: %v %v", at, pd, err)
			}
			if got, want := idsOf(pd.Items), allowed(maxAge, show, everything...); got != want {
				t.Errorf("%s, Ada's filmography: %q, want %q", at, got, want)
			}
			people, err := st.SearchPeople(ctx, "ada", 10)
			if err != nil {
				t.Fatal(err)
			}
			want := len(strings.Fields(allowed(maxAge, show, everything...)))
			switch {
			case want == 0 && len(people) != 0:
				t.Errorf("%s, people called Ada: %+v, want nobody, as she is in no title the cap allows", at, people)
			case want > 0 && (len(people) != 1 || people[0].Credits != want):
				t.Errorf("%s, people called Ada: %+v, want her with %d titles", at, people, want)
			}
		}
	}

	// Uncapped, everything, as before.
	res, err := st.ListItems(context.Background(), ListOpts{Limit: 200})
	if err != nil || idsOf(res.Items) != allowed(99, true, everything...) || res.Total != len(everything) {
		t.Errorf("uncapped: %q %d %v", idsOf(res.Items), res.Total, err)
	}
	if people, err := st.SearchPeople(context.Background(), "ada", 10); err != nil || len(people) != 1 || people[0].Credits != len(everything) {
		t.Errorf("uncapped, people called Ada: %+v %v", people, err)
	}
}

// A title with a parent is no film for a capped viewer's list of films: the
// list reads a film's own rating, so one filed under a parent is left out
// rather than served by a rating its parent might not share.
func TestAFilmWithAParentIsLeftOutOfACappedList(t *testing.T) {
	st, db := ratedCatalog(t)
	db.Exec(t, `UPDATE com_nalet_katalog_items SET parent_id = 's12' WHERE id = 'm0'`)
	ctx := WithMaxAge(context.Background(), 12)
	res, err := st.ListItems(ctx, ListOpts{Type: "movie"})
	if err != nil || idsOf(res.Items) != "m12 mo" {
		t.Errorf("films capped at 12: %q %v, want m12 mo", idsOf(res.Items), err)
	}
	if _, err := st.GetItem(ctx, "m0"); err != nil {
		t.Errorf("by id it is rated as its parent, 12: %v", err)
	}
}

// An id that names no title is not found, for every viewer: the episodes of
// a series there is not, and the segments of an item there is not.
func TestWhatIsNotThereIsNotFound(t *testing.T) {
	st, _ := ratedCatalog(t)
	for _, ctx := range []context.Context{context.Background(), WithMaxAge(context.Background(), 18)} {
		if _, err := st.ListEpisodesBySeries(ctx, "no-such-series"); !errors.Is(err, ErrNotFound) {
			t.Errorf("the episodes of a series there is not: %v", err)
		}
		if _, err := st.ListSegments(ctx, "no-such-item"); !errors.Is(err, ErrNotFound) {
			t.Errorf("the segments of an item there is not: %v", err)
		}
		if eps, err := st.ListEpisodesBySeries(ctx, "m12"); err != nil || len(eps) != 0 {
			t.Errorf("the episodes of a film: %v %v, want none", eps, err)
		}
	}
}

// A catalog that rates nothing (no migration 036, or a role that may not
// read its columns) serves a capped viewer nothing, and every other viewer
// as before.
func TestACatalogThatRatesNothingServesTheCappedNothing(t *testing.T) {
	st, db := open(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	ctx := WithMaxAge(context.Background(), 18)
	if res, err := st.ListItems(ctx, ListOpts{}); err != nil || len(res.Items) != 0 || res.Total != 0 {
		t.Errorf("capped, without 036: %v %d %v", idsOf(res.Items), res.Total, err)
	}
	if _, err := st.GetItem(ctx, "m1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("capped, without 036, by id: %v", err)
	}
	if res, err := st.ListItems(context.Background(), ListOpts{}); err != nil || idsOf(res.Items) != "m1" {
		t.Errorf("uncapped, without 036: %v %v", idsOf(res.Items), err)
	}
}

// The setting is read from the catalog's settings, at most every half
// minute; one the role may not read hides.
func TestTheUnratedSettingIsReadAtMostEveryHalfMinute(t *testing.T) {
	st, db := ratedCatalog(t)
	ctx := context.Background()
	setUnrated(t, st, db, "show")
	if !st.showUnrated(ctx) {
		t.Fatal("show not read")
	}
	db.Exec(t, `UPDATE com_nalet_katalog_settings SET valuetext = 'hide' WHERE key = 'ratings.unrated_for_capped'`)
	if !st.showUnrated(ctx) {
		t.Error("the setting was read again within half a minute")
	}
	st.unrated.mu.Lock()
	st.unrated.until = time.Now().Add(-time.Second)
	st.unrated.mu.Unlock()
	if st.showUnrated(ctx) {
		t.Error("the setting was not read again once its half minute was up")
	}

	role, pool := db.Role(t)
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_items TO `+role)
	db.Exec(t, `UPDATE com_nalet_katalog_settings SET valuetext = 'show' WHERE key = 'ratings.unrated_for_capped'`)
	ro := &Store{Pool: pool}
	if ro.showUnrated(ctx) {
		t.Error("a role that may not read the settings shows unrated titles")
	}
}
