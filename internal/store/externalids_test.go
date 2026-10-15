package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// identify gives item its id in source, a row of the catalog's identities.
func identify(t *testing.T, db *storetest.DB, item, source, id string) {
	t.Helper()
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemexternalids (id, item_id, source, externalid)
		VALUES (gen_random_uuid()::varchar, $1, $2, $3)`, item, source, id)
}

// identifiedCatalog is a catalog whose film m1 and show s1 enrichment
// identified, with the show's episode e1, and the film m2 it did not; the
// person p1 is credited in m1 and s1, and m2 shares a genre with m1.
func identifiedCatalog(t *testing.T) (*Store, *storetest.DB) {
	t.Helper()
	st, db := open(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	addItem(t, db, "m2", "movie", "Another Film", 2002, 6)
	addItem(t, db, "s1", "series", "A Show", 2010, 8)
	addEpisode(t, db, "e1", "s1", 1, 1)
	identify(t, db, "m1", "tmdb", "4711")
	identify(t, db, "m1", "imdb", "tt0004711")
	identify(t, db, "s1", "tmdb", "97546")
	identify(t, db, "s1", "tvdb", "362392")
	identify(t, db, "s1", "imdb", "tt9208876")
	identify(t, db, "e1", "tmdb-episode", "1984432")
	credit(t, db, "m1", "Ada Example", "actor")
	credit(t, db, "s1", "Ada Example", "actor")
	db.Exec(t, `INSERT INTO com_nalet_katalog_genres (id, name) VALUES ('g1', 'Drama')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemgenres (id, item_id, genre_id) VALUES ('ig1', 'm1', 'g1'), ('ig2', 'm2', 'g1')`)
	return st, db
}

// The identities identifiedCatalog's items carry.
var catalogIdentities = map[string]map[string]string{
	"m1": {"tmdb": "4711", "imdb": "tt0004711"},
	"s1": {"tmdb": "97546", "tvdb": "362392", "imdb": "tt9208876"},
	"e1": {"tmdb-episode": "1984432"},
}

// Every read of an item says the identities it carries outside the catalog,
// every source of them keyed as stored: the item by id, with its
// associations or without, the lists, a series' episodes, more like a title
// and a person's filmography. An item the catalog keeps none of says none.
func TestEveryReadOfAnItemSaysItsIdentities(t *testing.T) {
	st, _ := identifiedCatalog(t)
	ctx := context.Background()
	check := func(where string, items ...Item) {
		t.Helper()
		if len(items) == 0 {
			t.Errorf("%s: no items", where)
		}
		for _, it := range items {
			if want := catalogIdentities[it.ID]; !reflect.DeepEqual(it.ExternalIDs, want) {
				t.Errorf("%s, %s: %v, want %v", where, it.ID, it.ExternalIDs, want)
			}
		}
	}
	for _, id := range []string{"m1", "m2", "s1", "e1"} {
		it, err := st.GetItem(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		check("by id", it)
		if it, err = st.GetItemWithIncludes(ctx, id, ParseInclude("genres,people,subtitles,extras,segments")); err != nil {
			t.Fatal(err)
		}
		check("by id with its associations", it)
	}
	for _, typ := range []string{"", "movie", "series", "episode"} {
		res, err := st.ListItems(ctx, ListOpts{Type: typ})
		if err != nil {
			t.Fatal(err)
		}
		check("the list of "+typ, res.Items...)
	}
	eps, err := st.ListEpisodesBySeries(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	check("the series' episodes", eps...)
	similar, err := st.ListSimilar(ctx, "m2", 12)
	if err != nil {
		t.Fatal(err)
	}
	check("more like m2", similar...)
	pd, err := st.GetPerson(ctx, personID("Ada Example"), 100, nil)
	if err != nil || pd == nil {
		t.Fatalf("the person: %v %v", pd, err)
	}
	check("the filmography", pd.Items...)
	if m2, _ := st.GetItem(ctx, "m2"); m2.ExternalIDs != nil {
		t.Errorf("m2, which nothing identified: %v, want none", m2.ExternalIDs)
	}
}

// An item's identities are keyed as the catalog stores them, whatever the
// source. A row whose source or id is blank is no identity. Of two rows of one
// source, the same one is answered every time: the row's whose id sorts last.
func TestAnItemsIdentitiesAsStored(t *testing.T) {
	st, db := open(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemexternalids (id, item_id, source, externalid) VALUES
		('x1', 'm1', 'imdb', 'tt0000001'), ('x3', 'm1', 'imdb', 'tt0000003'), ('x2', 'm1', 'imdb', 'tt0000002'),
		('x4', 'm1', 'tmdb', '4711'), ('x5', 'm1', 'wikidata', 'Q4711'), ('x6', 'm1', 'tvdb', ''),
		('x7', 'm1', '  ', '1'), ('x8', 'm1', 'tmdb-season', '   '), ('x9', 'other', 'tmdb', '1')`)
	it, err := st.GetItem(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"imdb": "tt0000003", "tmdb": "4711", "wikidata": "Q4711"}; !reflect.DeepEqual(it.ExternalIDs, want) {
		t.Errorf("%v, want %v", it.ExternalIDs, want)
	}
}

// A role that may not read the catalog's identities is served its items
// without them, rather than failing, and with them as soon as it may.
func TestIdentitiesTheRoleMayNotRead(t *testing.T) {
	_, db := identifiedCatalog(t)
	role, pool := db.Role(t)
	st := &Store{Pool: pool}
	ctx := context.Background()
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_items TO `+role)
	it, err := st.GetItem(ctx, "m1")
	if err != nil || it.ExternalIDs != nil {
		t.Fatalf("not granted the identities: %v %v, want the item without them", it.ExternalIDs, err)
	}
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_itemexternalids TO `+role)
	res, err := st.ListItems(ctx, ListOpts{Type: "movie"})
	if err != nil || len(res.Items) != 2 || !reflect.DeepEqual(res.Items[0].ExternalIDs, catalogIdentities["m1"]) {
		t.Fatalf("granted them: %+v %v, want m1 with %v", res.Items, err, catalogIdentities["m1"])
	}
}
