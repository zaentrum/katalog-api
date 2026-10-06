package store

import (
	"context"
	"reflect"
	"testing"
)

// The packaged ids are the movies and episodes that have a packaged asset,
// each once, in id order, before the library and in it alike: not a series nor
// any other type of item, whatever its assets; not an item with only its file,
// nor one whose original was retired with nothing packaged; nor an asset of no
// item. An empty catalog has none, an empty list.
func TestThePackagedIDs(t *testing.T) {
	st, db := open(t)
	ctx := context.Background()
	if ids, err := st.PackagedIDs(ctx); err != nil || ids == nil || len(ids) != 0 {
		t.Fatalf("an empty catalog: %#v %v, want an empty list", ids, err)
	}
	addItem(t, db, "m1", "movie", "Packaged", 2001, 7)
	addItem(t, db, "m2", "movie", "A File Only", 2002, 7)
	addItem(t, db, "m3", "movie", "Packaged Twice", 2003, 7)
	addItem(t, db, "m4", "movie", "Retired, Nothing Packaged", 2004, 7)
	addItem(t, db, "s1", "series", "A Show", 2010, 8)
	addItem(t, db, "t1", "track", "A Song", 2011, 6)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, parent_id) VALUES ('e1', 'episode', 'Pilot', 's1')`)
	addAsset(t, db, "a1", "m1", lib+"/media/Packaged (2001).mkv", true, "primary")
	addAsset(t, db, "a2", "m1", lib+"/packages/movies/m1/m1/manifest.json", false, "packaged")
	addAsset(t, db, "a3", "m2", lib+"/media/A File Only (2002).mkv", true, "primary")
	addAsset(t, db, "a4", "m3", lib+"/packages/movies/m3/m3/manifest.json", false, "packaged")
	addAsset(t, db, "a5", "m3", lib+"/packages/movies/m3/m3/a/manifest.json", false, "packaged")
	addAsset(t, db, "a6", "m4", lib+"/movies/m4/m4/sources/src4", false, "original")
	addAsset(t, db, "a7", "e1", lib+"/packages/shows/e1/e1/manifest.json", false, "packaged")
	addAsset(t, db, "a8", "s1", lib+"/packages/items/s1/s1/manifest.json", false, "packaged")
	addAsset(t, db, "a9", "t1", lib+"/packages/music/t1/t1/manifest.json", false, "packaged")
	addAsset(t, db, "a10", "gone", lib+"/packages/movies/go/gone/manifest.json", false, "packaged")

	want := []string{"e1", "m1", "m3"}
	for _, when := range []string{"before 040", "after 040"} {
		if when == "after 040" {
			db.Migrate040(t)
		}
		if ids, err := st.PackagedIDs(ctx); err != nil || !reflect.DeepEqual(ids, want) {
			t.Errorf("%s: %q %v, want %q", when, ids, err, want)
		}
	}
	// m2 is packaged into the library.
	db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind, versionid)
		VALUES ('a11', 'm2', $1, false, 'packaged', 'v1')`, lib+"/movies/m2/m2/versions/v1/package.json")
	if ids, err := st.PackagedIDs(ctx); err != nil || !reflect.DeepEqual(ids, []string{"e1", "m1", "m2", "m3"}) {
		t.Errorf("in the library: %q %v, want e1, m1, m2, m3", ids, err)
	}
}
