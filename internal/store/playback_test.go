package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// lib is the library's root in the fixtures' paths.
const lib = "/var/lib/katalog"

// playbackOf is Playback of item as it goes on the wire, or its error.
func playbackOf(t *testing.T, st *Store, item string) string {
	t.Helper()
	p, err := st.Playback(context.Background(), item)
	if err != nil {
		return err.Error()
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// addVersion adds a version of item, its folder under the item's (versions/id),
// when it was completed, superseded and removed ("" for not).
func addVersion(t *testing.T, db *storetest.DB, id, item, state, completed, superseded, removed string) {
	t.Helper()
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemversions (id, item_id, state, dir, completedat, supersededat, removedat)
		VALUES ($1, $2, $3, $4, NULLIF($5, '')::timestamptz, NULLIF($6, '')::timestamptz, NULLIF($7, '')::timestamptz)`,
		id, item, state, lib+"/movies/"+item[:2]+"/"+item+"/versions/"+id, completed, superseded, removed)
}

// Before the library (no migration 040) an item's package is its packaged
// asset's folder, read by its manifest.json, with no version and nothing
// previous; its original is its primary asset, of no known source. An item
// without a packaged asset has no package, one without assets no original
// either, and a series none whatever it holds; an id of no item is not found.
// After 040 an item packaged before the library is answered the same, and one
// given a version is answered from it, without a restart.
func TestPlaybackOnACatalogOlderThan040(t *testing.T) {
	st, db := open(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	addItem(t, db, "m2", "movie", "Not Packaged", 2002, 7)
	addItem(t, db, "m3", "movie", "Nothing At All", 2003, 7)
	addItem(t, db, "s1", "series", "A Show", 2010, 8)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, parent_id) VALUES ('e1', 'episode', 'Pilot', 's1')`)
	addAsset(t, db, "a1", "m1", lib+"/media/A Film (2001).mkv", true, "primary")
	addAsset(t, db, "a2", "m1", lib+"/packages/movies/m1/m1/manifest.json", false, "packaged")
	addAsset(t, db, "a3", "e1", lib+"/media/shows/A Show/A Show S01E01.mkv", true, "")
	addAsset(t, db, "a4", "e1", lib+"/packages/shows/e1/e1/manifest.json", false, "packaged")
	addAsset(t, db, "a5", "m2", lib+"/media/Not Packaged (2002).mkv", true, "primary")
	addAsset(t, db, "a6", "s1", lib+"/packages/items/s1/s1/manifest.json", false, "packaged")
	want := map[string]string{
		"m1": `{"itemId":"m1","type":"movie",` +
			`"package":{"versionId":null,"dir":"/var/lib/katalog/packages/movies/m1/m1","record":"manifest.json"},` +
			`"previous":[],"original":{"path":"/var/lib/katalog/media/A Film (2001).mkv","sourceId":null}}`,
		"e1": `{"itemId":"e1","type":"episode",` +
			`"package":{"versionId":null,"dir":"/var/lib/katalog/packages/shows/e1/e1","record":"manifest.json"},` +
			`"previous":[],"original":{"path":"/var/lib/katalog/media/shows/A Show/A Show S01E01.mkv","sourceId":null}}`,
		"m2": `{"itemId":"m2","type":"movie","package":null,"previous":[],` +
			`"original":{"path":"/var/lib/katalog/media/Not Packaged (2002).mkv","sourceId":null}}`,
		"m3":      `{"itemId":"m3","type":"movie","package":null,"previous":[],"original":null}`,
		"s1":      `{"itemId":"s1","type":"series","package":null,"previous":[],"original":null}`,
		"nothing": "not found",
	}
	for _, when := range []string{"before 040", "after 040"} {
		if when == "after 040" {
			db.Migrate040(t) // while the service is up
		}
		for item, w := range want {
			if got := playbackOf(t, st, item); got != w {
				t.Errorf("%s, %s:\n got %s\nwant %s", when, item, got, w)
			}
		}
	}

	// m1 is packaged into the library: its packaged asset is now its
	// version's package.json, of the source of its primary asset.
	addVersion(t, db, "v1", "m1", "complete", "2026-10-06 09:00:00+00", "", "")
	db.Exec(t, `UPDATE com_nalet_katalog_playbackassets SET path = $1, versionid = 'v1' WHERE id = 'a2'`,
		lib+"/movies/m1/m1/versions/v1/package.json")
	db.Exec(t, `UPDATE com_nalet_katalog_playbackassets SET sourceid = 'src1' WHERE id = 'a1'`)
	w := `{"itemId":"m1","type":"movie",` +
		`"package":{"versionId":"v1","dir":"/var/lib/katalog/movies/m1/m1/versions/v1","record":"package.json","completedAt":"2026-10-06T09:00:00Z"},` +
		`"previous":[],"original":{"path":"/var/lib/katalog/media/A Film (2001).mkv","sourceId":"src1"}}`
	if got := playbackOf(t, st, "m1"); got != w {
		t.Errorf("given a version:\n got %s\nwant %s", got, w)
	}
}

// In the library an item's package is its complete version, read by its
// package.json, with when it was completed, in UTC (whatever the zone it was
// written in, or this process's). Previous are its superseded versions that
// are not removed, the one superseded last first, by the catalog's clock: a
// version's completedat is its packager's, which may run ahead. A version
// being built, one removed, and one whose removal has begun are none of them.
// An item whose original was retired (kind original, its file gone) has none;
// the source of one that has it is said.
func TestPlaybackOfAnItemWithVersions(t *testing.T) {
	st, db := open(t)
	db.Migrate040(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	addItem(t, db, "m2", "movie", "Retired", 2002, 7)
	addVersion(t, db, "v5", "m1", "building", "", "", "")
	addVersion(t, db, "v4", "m1", "complete", "2026-10-06 09:00:00+00", "", "")
	addVersion(t, db, "v3", "m1", "superseded", "2026-10-05 09:00:00+00", "2026-10-06 09:00:05+00", "")
	addVersion(t, db, "v2", "m1", "superseded", "2026-10-05 11:00:00+00", "2026-10-05 09:00:05+00", "")
	addVersion(t, db, "v1", "m1", "superseded", "2026-10-04 09:00:00+00", "2026-10-05 08:00:00+00", "2026-10-06 08:00:00+00")
	addVersion(t, db, "v0", "m1", "removed", "2026-10-03 09:00:00+00", "2026-10-04 09:00:00+00", "2026-10-05 09:00:00+00")
	addVersion(t, db, "w1", "m2", "complete", "2026-10-06 10:30:00.25+02", "", "")
	db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind, sourceid, versionid) VALUES
		('a1', 'm1', $1, true,  'primary',  'src1', NULL),
		('a2', 'm1', $2, false, 'packaged', NULL,   'v4'),
		('a3', 'm2', $3, false, 'original', 'src2', NULL),
		('a4', 'm2', $4, false, 'packaged', NULL,   'w1')`,
		lib+"/.work/incoming/A Film (2001).mkv", lib+"/movies/m1/m1/versions/v4/package.json",
		lib+"/movies/m2/m2/sources/src2", lib+"/movies/m2/m2/versions/w1/package.json")

	for item, w := range map[string]string{
		"m1": `{"itemId":"m1","type":"movie",` +
			`"package":{"versionId":"v4","dir":"/var/lib/katalog/movies/m1/m1/versions/v4","record":"package.json","completedAt":"2026-10-06T09:00:00Z"},` +
			`"previous":[{"versionId":"v3","dir":"/var/lib/katalog/movies/m1/m1/versions/v3","record":"package.json"},` +
			`{"versionId":"v2","dir":"/var/lib/katalog/movies/m1/m1/versions/v2","record":"package.json"}],` +
			`"original":{"path":"/var/lib/katalog/.work/incoming/A Film (2001).mkv","sourceId":"src1"}}`,
		"m2": `{"itemId":"m2","type":"movie",` +
			`"package":{"versionId":"w1","dir":"/var/lib/katalog/movies/m2/m2/versions/w1","record":"package.json","completedAt":"2026-10-06T08:30:00.25Z"},` +
			`"previous":[],"original":null}`,
	} {
		if got := playbackOf(t, st, item); got != w {
			t.Errorf("%s:\n got %s\nwant %s", item, got, w)
		}
	}
}

// A read-only role granted the catalog's tables before 040 created the
// versions' may not read them: an item is answered from its packaged asset,
// which in the library is its version's package.json, so the same folder plays,
// with nothing previous; what 040 adds to an asset is said once the role may
// read it, and the versions once it is granted them, without a restart.
func TestPlaybackWhenTheRoleMayNotReadTheVersions(t *testing.T) {
	_, db := open(t)
	db.Migrate040(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	addVersion(t, db, "v2", "m1", "complete", "2026-10-06 09:00:00+00", "", "")
	addVersion(t, db, "v1", "m1", "superseded", "2026-10-05 09:00:00+00", "2026-10-06 09:00:05+00", "")
	db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind, sourceid, versionid) VALUES
		('a1', 'm1', $1, true,  'primary',  'src1', NULL),
		('a2', 'm1', $2, false, 'packaged', NULL,   'v2')`,
		lib+"/.work/incoming/A Film (2001).mkv", lib+"/movies/m1/m1/versions/v2/package.json")
	role, pool := db.Role(t)
	st := &Store{Pool: pool}

	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_items TO `+role)
	db.Exec(t, `GRANT SELECT (id, item_id, path, isprimary, kind) ON com_nalet_katalog_playbackassets TO `+role)
	for _, step := range []struct{ grant, want string }{
		{"", `{"itemId":"m1","type":"movie",` +
			`"package":{"versionId":null,"dir":"/var/lib/katalog/movies/m1/m1/versions/v2","record":"package.json"},` +
			`"previous":[],"original":{"path":"/var/lib/katalog/.work/incoming/A Film (2001).mkv","sourceId":null}}`},
		{"com_nalet_katalog_playbackassets", `{"itemId":"m1","type":"movie",` +
			`"package":{"versionId":"v2","dir":"/var/lib/katalog/movies/m1/m1/versions/v2","record":"package.json"},` +
			`"previous":[],"original":{"path":"/var/lib/katalog/.work/incoming/A Film (2001).mkv","sourceId":"src1"}}`},
		{"com_nalet_katalog_itemversions", `{"itemId":"m1","type":"movie",` +
			`"package":{"versionId":"v2","dir":"/var/lib/katalog/movies/m1/m1/versions/v2","record":"package.json","completedAt":"2026-10-06T09:00:00Z"},` +
			`"previous":[{"versionId":"v1","dir":"/var/lib/katalog/movies/m1/m1/versions/v1","record":"package.json"}],` +
			`"original":{"path":"/var/lib/katalog/.work/incoming/A Film (2001).mkv","sourceId":"src1"}}`},
	} {
		if step.grant != "" {
			db.Exec(t, `GRANT SELECT ON `+step.grant+` TO `+role)
		}
		if got := playbackOf(t, st, "m1"); got != step.want {
			t.Errorf("granted %q:\n got %s\nwant %s", step.grant, got, step.want)
		}
	}
}
