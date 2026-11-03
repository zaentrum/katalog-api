package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

// insertPictureAreas begins an INSERT of rows of the picture areas (migration
// 054): the item or extra measured, its kind, its title and its state, the
// frame and the area in it, the file measured, and the source, the version or
// the package that file is.
const insertPictureAreas = `INSERT INTO com_nalet_katalog_pictureareas
	(id, kind, item_id, state, framewidth, frameheight, areax, areay, areawidth, areaheight, path, sourceid, versionid, packageid)
	VALUES `

// area is the picture area at x, y, w by h in a frame fw by fh, as it goes on
// the wire.
func area(fw, fh, x, y, w, h int) string {
	return fmt.Sprintf(`{"frameWidth":%d,"frameHeight":%d,"x":%d,"y":%d,"width":%d,"height":%d}`, fw, fh, x, y, w, h)
}

// wireArea is a, as it goes on the wire, or "-" when there is none: the answer
// leaves the field out.
func wireArea(t *testing.T, a *PictureArea) string {
	t.Helper()
	if a == nil {
		return "-"
	}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// areaOf is the picture area Playback answers item (wireArea), or its error.
func areaOf(t *testing.T, st *Store, item string) string {
	t.Helper()
	p, err := st.Playback(context.Background(), item)
	if err != nil {
		return err.Error()
	}
	return wireArea(t, p.PictureArea)
}

// extraAreaOf is the picture area ExtraPlayback answers extra (wireArea), or
// its error.
func extraAreaOf(t *testing.T, st *Store, extra string) string {
	t.Helper()
	x, err := st.ExtraPlayback(context.Background(), extra)
	if err != nil {
		return err.Error()
	}
	return wireArea(t, x.PictureArea)
}

// Before katalog-manager's migration 054 the catalog keeps no picture area: an
// item and an extra are answered without one, key for key as before, and the
// view is looked for again on the next request. Once 054 has run they are
// answered so until the analyzer measured bars in what they play, and then
// with the area, after every other key, without a restart.
func TestThePictureAreaOnACatalogOlderThan054(t *testing.T) {
	st, db := open(t)
	db.Migrate040(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	dir, xdir := lib+"/movies/m1/m1/versions/v1", lib+"/movies/m1/m1/extras/x1"
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemversions (id, item_id, sourceids, state, dir, completedat)
		VALUES ('v1', 'm1', '{src1}', 'complete', $1, '2026-10-06 09:00:00+00')`, dir)
	db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind, sourceid, versionid, resolution) VALUES
		('a1', 'm1', $1, true,  'primary',  'src1', NULL, NULL),
		('a2', 'm1', $2, false, 'packaged', NULL,   'v1', '1280x720')`, lib+"/.work/incoming/A Film (2001).mkv", dir+"/package.json")
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras (id, item_id, kind, title, registeredby, state, packagepath, packagedat, packageid)
		VALUES ('x1', 'm1', 'trailer', 'Trailer', 'api', 'ready', $1, '2026-10-06 10:00:00+00', 'p1')`, xdir)
	film := `{"itemId":"m1","type":"movie",` +
		`"package":{"versionId":"v1","dir":"/var/lib/katalog/movies/m1/m1/versions/v1","record":"package.json","completedAt":"2026-10-06T09:00:00Z"},` +
		`"previous":[],"original":{"path":"/var/lib/katalog/.work/incoming/A Film (2001).mkv","sourceId":"src1"}}`
	extra := `{"extraId":"x1","itemId":"m1","dir":"/var/lib/katalog/movies/m1/m1/extras/x1","record":"package.json",` +
		`"packagedAt":"2026-10-06T10:00:00Z"}`
	check := func(when, film, extra string) {
		t.Helper()
		if got := playbackOf(t, st, "m1"); got != film {
			t.Errorf("%s, m1:\n got %s\nwant %s", when, got, film)
		}
		if got := extraPlaybackOf(t, st, "x1"); got != extra {
			t.Errorf("%s, x1:\n got %s\nwant %s", when, got, extra)
		}
	}
	for i := 1; i <= 2; i++ {
		check(fmt.Sprintf("before 054, call %d", i), film, extra)
	}
	db.Migrate054(t) // while the service is up
	check("after 054, nothing measured", film, extra)

	// The analyzer measures the film's original as it is taken in, and the
	// trailer's package.
	db.Exec(t, insertPictureAreas+`
		('m1', 'item',  'm1', 'measured', 1920, 1080, 0, 138, 1920, 804, $1, 'src1', NULL, NULL),
		('x1', 'extra', 'm1', 'measured', 1920, 1080, 0, 132, 1920, 816, $2, NULL,   NULL, 'p1')`,
		lib+"/.work/incoming/A Film (2001).mkv", xdir+"/hls/v0/playlist.m3u8")
	check("measured",
		strings.TrimSuffix(film, "}")+`,"pictureArea":{"frameWidth":1920,"frameHeight":1080,"x":0,"y":138,"width":1920,"height":804}}`,
		strings.TrimSuffix(extra, "}")+`,"pictureArea":{"frameWidth":1920,"frameHeight":1080,"x":0,"y":132,"width":1920,"height":816}}`)
}

// An item is answered the picture area measured of what it plays now, as the
// view of migration 054 holds it: of an original its complete version was
// made of (m1), or of that version's package (m2). Left out: an area that is
// the whole frame, with no bars to fit (m3); a row the analyzer has not
// measured, pending, unknown or failed, whatever it still holds (m4 to m6); a
// row of a file the item no longer plays, its file replaced since, neither of
// the source nor of the version that plays now (m8), until it is measured
// again; and a series, whatever its row says (s1). An item nothing measured has
// none (m9), and an id of no item is not found.
func TestThePictureAreaOfWhatAnItemPlays(t *testing.T) {
	st, db := open(t)
	db.Migrate054(t)
	movies := []string{"m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9"}
	versions := func(item string) string { return lib + "/movies/" + item + "/" + item + "/versions/" }
	for _, id := range movies {
		addItem(t, db, id, "movie", "A Film", 2001, 7)
		// Each plays its complete version v-<id>, made of its original src-<id>,
		// its packaged row as the handover writes it (its video's size); m7's
		// original is an anamorphic disc's, packaged with square pixels.
		db.Exec(t, `INSERT INTO com_nalet_katalog_itemversions (id, item_id, sourceids, state, dir, completedat)
			VALUES ($1, $2, $3, 'complete', $4, '2026-10-06 09:00:00+00')`,
			"v-"+id, id, []string{"src-" + id}, versions(id)+"v-"+id)
		size := "1920x1080"
		if id == "m7" {
			size = "1024x576"
		}
		db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind, versionid, resolution)
			VALUES ($1, $2, $3, false, 'packaged', $4, $5)`, "p-"+id, id, versions(id)+"v-"+id+"/package.json", "v-"+id, size)
	}
	// m8's file was replaced: it played v-m8-old, made of another original.
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemversions (id, item_id, sourceids, state, dir, completedat, supersededat)
		VALUES ('v-m8-old', 'm8', '{src-m8-old}', 'superseded', $1, '2026-10-05 09:00:00+00', '2026-10-06 09:00:05+00')`,
		versions("m8")+"v-m8-old")
	addItem(t, db, "s1", "series", "A Show", 2010, 8)
	addAsset(t, db, "p-s1", "s1", lib+"/packages/items/s1/s1/manifest.json", false, "packaged")
	original := func(item string) string { return lib + "/.work/incoming/" + item + ".mkv" }
	video := "/hls/v0/playlist.m3u8"
	db.Exec(t, insertPictureAreas+`
		('m1', 'item', 'm1', 'measured', 1920, 1080, 0,    138,  1920, 804,  $1, 'src-m1',     NULL,       NULL),
		('m2', 'item', 'm2', 'measured', 3840, 2160, 0,    276,  3840, 1608, $2, NULL,         'v-m2',     NULL),
		('m3', 'item', 'm3', 'measured', 1920, 1080, 0,    0,    1920, 1080, $3, 'src-m3',     NULL,       NULL),
		('m4', 'item', 'm4', 'pending',  1920, 1080, 0,    138,  1920, 804,  $4, 'src-m4',     NULL,       NULL),
		('m5', 'item', 'm5', 'unknown',  1920, 1080, 0,    138,  1920, 804,  $5, 'src-m5',     NULL,       NULL),
		('m6', 'item', 'm6', 'failed',   1920, 1080, 0,    138,  1920, 804,  $6, 'src-m6',     NULL,       NULL),
		('m7', 'item', 'm7', 'measured', 720,  576,  0,    70,   720,  436,  $9, 'src-m7',     NULL,       NULL),
		('m8', 'item', 'm8', 'measured', 1920, 1080, 0,    138,  1920, 804,  $7, 'src-m8-old', 'v-m8-old', NULL),
		('s1', 'item', 's1', 'measured', 1920, 1080, 0,    138,  1920, 804,  $8, NULL,         NULL,       NULL)`,
		original("m1"), versions("m2")+"v-m2"+video, original("m3"), original("m4"), original("m5"), original("m6"),
		versions("m8")+"v-m8-old"+video, lib+"/packages/items/s1/s1"+video, original("m7"))

	// The view leaves out m8's row (of another file) and m7's (of a frame
	// of another shape than its package's) alone; the others are left out
	// as they are read.
	var current string
	if err := db.Pool.QueryRow(context.Background(),
		`SELECT string_agg(id, ' ' ORDER BY id) FROM com_nalet_katalog_currentpictureareas`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	if want := "m1 m2 m3 m4 m5 m6 s1"; current != want {
		t.Fatalf("the view holds %q, want %q", current, want)
	}

	for item, w := range map[string]string{
		"m1": area(1920, 1080, 0, 138, 1920, 804), "m2": area(3840, 2160, 0, 276, 3840, 1608),
		"m3": "-", "m4": "-", "m5": "-", "m6": "-", "m7": "-", "m8": "-", "m9": "-", "s1": "-",
		"nothing": "not found",
	} {
		if got := areaOf(t, st, item); got != w {
			t.Errorf("%s: %s, want %s", item, got, w)
		}
	}

	// m7's measurement read its pixels as 64:45: its picture has the shape
	// of its package of square pixels, and the area is of what it plays.
	db.Exec(t, `UPDATE com_nalet_katalog_pictureareas SET sampling = '{"video": {"sar": "64:45"}}' WHERE id = 'm7'`)
	if got, w := areaOf(t, st, "m7"), area(720, 576, 0, 70, 720, 436); got != w {
		t.Errorf("m7 with its pixel aspect: %s, want %s", got, w)
	}

	// m8 is measured again, of the version it plays now.
	db.Exec(t, `UPDATE com_nalet_katalog_pictureareas SET path = $1, sourceid = 'src-m8', versionid = 'v-m8',
		areay = 140, areaheight = 800 WHERE id = 'm8'`, versions("m8")+"v-m8"+video)
	if got, w := areaOf(t, st, "m8"), area(1920, 1080, 0, 140, 1920, 800); got != w {
		t.Errorf("m8 measured again: %s, want %s", got, w)
	}
}

// A covered episode is answered the picture area of the file it plays, its
// holder's, beside the holder's package and original, whatever rows it holds
// itself; the holder is answered its own, and an episode with a file of its
// own its own. Without a complete version, a holder's area is one measured of
// the folder of its package from before the library (h2), or of its original,
// by its path (h6, pillarboxed: its picture narrower than the frame).
func TestACoveredEpisodeIsAnsweredItsHoldersPictureArea(t *testing.T) {
	st, db := sharedFiles(t)
	db.Migrate054(t)
	// z3 holds a stray file of its own, which the analyzer measured too.
	addAsset(t, db, "a-z3", "z3", lib+"/media/shows/A Show/A Show S01E03.mkv", true, "primary")
	db.Exec(t, insertPictureAreas+`
		('h2', 'item', 'h2', 'measured', 1920, 1080, 0,   138, 1920, 804,  $1, NULL, NULL, NULL),
		('h6', 'item', 'h6', 'measured', 1920, 1080, 240, 0,   1440, 1080, $2, NULL, NULL, NULL),
		('z3', 'item', 'z3', 'measured', 1920, 1080, 0,   100, 1920, 880,  $3, NULL, NULL, NULL)`,
		lib+"/packages/shows/h2/h2/hls/v0/playlist.m3u8", lib+"/media/shows/A Show/A Show S01E06-E07.mkv",
		lib+"/media/shows/A Show/A Show S01E03.mkv")
	h2, h6 := area(1920, 1080, 0, 138, 1920, 804), area(1920, 1080, 240, 0, 1440, 1080)
	for item, w := range map[string]string{"h2": h2, "z3": h2, "a4": h2, "h6": h6, "c7": h6, "e1": "-"} {
		if got := areaOf(t, st, item); got != w {
			t.Errorf("%s: %s, want %s", item, got, w)
		}
	}
	w := coveredAnswer(strings.TrimSuffix(playsH2, "}")+`,"pictureArea":`+h2+`}`, "h2", "z3")
	if got := playbackOf(t, st, "z3"); got != w {
		t.Errorf("z3:\n got %s\nwant %s", got, w)
	}
}

// An extra is answered the picture area measured of its package: in the
// library the row of its package id (xlib), before it one measured of a file
// in its package's folder (xstore). Left out: a row of a package it no longer
// plays, of the one it had before it was packaged anew, although in the same
// folder (xanew), until it is measured again; an area that is the whole frame
// (xfull); and a row the analyzer has not measured (xunknown). A removed
// extra is not found, as before, whatever its row says.
func TestThePictureAreaOfAnExtra(t *testing.T) {
	st, db := open(t)
	db.Migrate054(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	extras, store := lib+"/movies/m1/m1/extras/", lib+"/packages/extras/xs/xstore"
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras
			(id, item_id, kind, title, registeredby, state, packagepath, packagedat, packageid, removedat) VALUES
		('xlib',     'm1', 'trailer',    'Trailer',   'api', 'ready', $1 || 'xlib',     '2026-10-06 09:00:00+00', 'p1', NULL),
		('xstore',   'm1', 'teaser',     'Teaser',    'api', 'ready', $2,               '2026-10-05 08:00:00+00', NULL, NULL),
		('xanew',    'm1', 'featurette', 'Making Of', 'api', 'ready', $1 || 'xanew',    '2026-10-06 09:00:00+00', 'p3', NULL),
		('xfull',    'm1', 'interview',  'Interview', 'api', 'ready', $1 || 'xfull',    '2026-10-06 09:00:00+00', 'p4', NULL),
		('xunknown', 'm1', 'trailer',    'Trailer 2', 'api', 'ready', $1 || 'xunknown', '2026-10-06 09:00:00+00', 'p5', NULL),
		('xremoved', 'm1', 'trailer',    'Removed',   'api', 'ready', $1 || 'xremoved', '2026-10-06 09:00:00+00', 'p6', now())`,
		extras, store)
	video := "/hls/v0/playlist.m3u8"
	db.Exec(t, insertPictureAreas+`
		('xlib',     'extra', 'm1', 'measured', 1920, 1080, 0, 132, 1920, 816,  $1, NULL, NULL, 'p1'),
		('xstore',   'extra', 'm1', 'measured', 1280, 720,  0, 88,  1280, 544,  $2, NULL, NULL, NULL),
		('xanew',    'extra', 'm1', 'measured', 1920, 1080, 0, 138, 1920, 804,  $3, NULL, NULL, 'p2'),
		('xfull',    'extra', 'm1', 'measured', 1920, 1080, 0, 0,   1920, 1080, $4, NULL, NULL, 'p4'),
		('xunknown', 'extra', 'm1', 'unknown',  1920, 1080, 0, 138, 1920, 804,  $5, NULL, NULL, 'p5'),
		('xremoved', 'extra', 'm1', 'measured', 1920, 1080, 0, 138, 1920, 804,  $6, NULL, NULL, 'p6')`,
		extras+"xlib"+video, store+video, extras+"xanew"+video, extras+"xfull"+video, extras+"xunknown"+video,
		extras+"xremoved"+video)
	for extra, w := range map[string]string{
		"xlib": area(1920, 1080, 0, 132, 1920, 816), "xstore": area(1280, 720, 0, 88, 1280, 544),
		"xanew": "-", "xfull": "-", "xunknown": "-", "xremoved": "not found", "nothing": "not found",
	} {
		if got := extraAreaOf(t, st, extra); got != w {
			t.Errorf("%s: %s, want %s", extra, got, w)
		}
	}

	// xanew's package is measured again.
	db.Exec(t, `UPDATE com_nalet_katalog_pictureareas SET packageid = 'p3', areay = 140, areaheight = 800 WHERE id = 'xanew'`)
	if got, w := extraAreaOf(t, st, "xanew"), area(1920, 1080, 0, 140, 1920, 800); got != w {
		t.Errorf("xanew measured again: %s, want %s", got, w)
	}
}

// A read-only role granted the catalog's tables before 054 created the view
// may not read it: an item and an extra are answered without a picture area
// rather than failing, also while the role may read only some of the view's
// columns, and with it once the role is granted the view, without a restart.
// The view reads the table beneath it as its owner, so the role needs no
// grant of the table.
func TestThePictureAreaWhenTheRoleMayNotReadIt(t *testing.T) {
	_, db := open(t)
	db.Migrate054(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	addVersion(t, db, "v1", "m1", "complete", "2026-10-06 09:00:00+00", "", "")
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras (id, item_id, kind, title, registeredby, state, packagepath, packagedat, packageid)
		VALUES ('x1', 'm1', 'trailer', 'Trailer', 'api', 'ready', $1, '2026-10-06 10:00:00+00', 'p1')`, lib+"/movies/m1/m1/extras/x1")
	db.Exec(t, insertPictureAreas+`
		('m1', 'item',  'm1', 'measured', 1920, 1080, 0, 138, 1920, 804, $1, NULL, 'v1', NULL),
		('x1', 'extra', 'm1', 'measured', 1920, 1080, 0, 132, 1920, 816, $2, NULL, NULL, 'p1')`,
		lib+"/movies/m1/m1/versions/v1/hls/v0/playlist.m3u8", lib+"/movies/m1/m1/extras/x1/hls/v0/playlist.m3u8")
	role, pool := db.Role(t)
	st := &Store{Pool: pool}

	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_items, com_nalet_katalog_playbackassets, com_nalet_katalog_itemversions,
		com_nalet_katalog_itemextras TO `+role)
	for _, step := range []struct{ grant, film, extra string }{
		{"", "-", "-"},
		{"SELECT (id, kind, state, framewidth, frameheight)", "-", "-"},
		{"SELECT", area(1920, 1080, 0, 138, 1920, 804), area(1920, 1080, 0, 132, 1920, 816)},
	} {
		if step.grant != "" {
			db.Exec(t, `GRANT `+step.grant+` ON com_nalet_katalog_currentpictureareas TO `+role)
		}
		if got := areaOf(t, st, "m1"); got != step.film {
			t.Errorf("granted %q, m1: %s, want %s", step.grant, got, step.film)
		}
		if got := extraAreaOf(t, st, "x1"); got != step.extra {
			t.Errorf("granted %q, x1: %s, want %s", step.grant, got, step.extra)
		}
	}
}
