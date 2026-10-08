package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// addEpisode adds the episode id of the show series: its season and number.
func addEpisode(t *testing.T, db *storetest.DB, id, series string, season, episode int) {
	t.Helper()
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, sorttitle, parent_id, seasonnumber, episodenumber)
		VALUES ($1, 'episode', $2, $2, $3, $4, $5)`, id, fmt.Sprintf("Episode %d", episode), series, season, episode)
}

// cover links the episodes covered to the file of holder (migration 045).
func cover(t *testing.T, db *storetest.DB, holder string, covered ...string) {
	t.Helper()
	db.Exec(t, `UPDATE com_nalet_katalog_items SET coveredby = $1 WHERE id = ANY($2)`, holder, covered)
}

// showWithSharedFiles is a catalog with migration 040 whose show s1 holds, each
// "id: what it is":
//
//	e1: S01E01, a file of its own, packaged
//	h2: S01E02, its file holding S01E02 to S01E04, packaged before the library
//	z3: S01E03, held in h2's file
//	a4: S01E04, held in h2's file (its id sorts before z3's, its number after)
//	h6: S01E06, its file holding S01E06 and S01E07, not packaged yet
//	c7: S01E07, held in h6's file
//
// and the film m1, packaged. The scanner registered each file for the first
// episode it holds, so the others have none.
func showWithSharedFiles(t *testing.T) (*Store, *storetest.DB) {
	t.Helper()
	st, db := open(t)
	db.Migrate040(t)
	addItem(t, db, "s1", "series", "A Show", 2010, 8)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	for id, n := range map[string]int{"e1": 1, "h2": 2, "z3": 3, "a4": 4, "h6": 6, "c7": 7} {
		addEpisode(t, db, id, "s1", 1, n)
	}
	addAsset(t, db, "a-e1", "e1", lib+"/media/shows/A Show/A Show S01E01.mkv", true, "primary")
	addAsset(t, db, "p-e1", "e1", lib+"/packages/shows/e1/e1/manifest.json", false, "packaged")
	addAsset(t, db, "a-h2", "h2", lib+"/media/shows/A Show/A Show S01E02-E04.mkv", true, "primary")
	addAsset(t, db, "p-h2", "h2", lib+"/packages/shows/h2/h2/manifest.json", false, "packaged")
	addAsset(t, db, "a-h6", "h6", lib+"/media/shows/A Show/A Show S01E06-E07.mkv", true, "primary")
	addAsset(t, db, "a-m1", "m1", lib+"/media/A Film (2001).mkv", true, "primary")
	addAsset(t, db, "p-m1", "m1", lib+"/packages/movies/m1/m1/manifest.json", false, "packaged")
	return st, db
}

// sharedFiles is showWithSharedFiles with migration 045, the files' episodes
// linked: z3 and a4 covered by h2, c7 by h6.
func sharedFiles(t *testing.T) (*Store, *storetest.DB) {
	t.Helper()
	st, db := showWithSharedFiles(t)
	db.Migrate045(t)
	cover(t, db, "h2", "z3", "a4")
	cover(t, db, "h6", "c7")
	return st, db
}

// The answers of showWithSharedFiles' episodes of their own, before the
// library.
const (
	playsH2 = `{"itemId":"h2","type":"episode",` +
		`"package":{"versionId":null,"dir":"/var/lib/katalog/packages/shows/h2/h2","record":"manifest.json"},` +
		`"previous":[],"original":{"path":"/var/lib/katalog/media/shows/A Show/A Show S01E02-E04.mkv","sourceId":null}}`
	playsH6 = `{"itemId":"h6","type":"episode","package":null,"previous":[],` +
		`"original":{"path":"/var/lib/katalog/media/shows/A Show/A Show S01E06-E07.mkv","sourceId":null}}`
	playsE1 = `{"itemId":"e1","type":"episode",` +
		`"package":{"versionId":null,"dir":"/var/lib/katalog/packages/shows/e1/e1","record":"manifest.json"},` +
		`"previous":[],"original":{"path":"/var/lib/katalog/media/shows/A Show/A Show S01E01.mkv","sourceId":null}}`
)

// coveredAnswer is the answer of the episode id covered by holder, whose own
// answer is w: the holder's, but for its id, and naming the holder.
func coveredAnswer(w, holder, id string) string {
	return strings.Replace(w, `{"itemId":"`+holder+`","type":"episode",`,
		`{"itemId":"`+id+`","type":"episode","coveredBy":"`+holder+`",`, 1)
}

// nothingToPlay is the answer of the episode id with nothing to play.
func nothingToPlay(id string) string {
	return `{"itemId":"` + id + `","type":"episode","package":null,"previous":[],"original":null}`
}

// A covered episode plays its holder's file: it is answered the holder's
// package, previous versions and original, and names the holder; the holder,
// and an episode with a file of its own, are answered their own and name none.
// Before the library the package is the folder of the holder's packaged asset;
// in it the holder's complete version and its superseded ones, so a session
// pinned to a version of the holder's is served from it whichever of the
// file's episodes it plays. The episodes of a holder not packaged yet are not
// packaged either, and have its file for their original; once the holder's
// original is retired, they have none. Whatever a covered episode holds itself
// is not read.
func TestACoveredEpisodePlaysItsHoldersFile(t *testing.T) {
	st, db := sharedFiles(t)
	check := func(when string, want map[string]string) {
		t.Helper()
		for item, w := range want {
			if got := playbackOf(t, st, item); got != w {
				t.Errorf("%s, %s:\n got %s\nwant %s", when, item, got, w)
			}
		}
	}
	check("before the library", map[string]string{
		"h2": playsH2, "z3": coveredAnswer(playsH2, "h2", "z3"), "a4": coveredAnswer(playsH2, "h2", "a4"),
		"h6": playsH6, "c7": coveredAnswer(playsH6, "h6", "c7"),
		"e1": playsE1, "nothing": "not found",
	})

	// h2 is packaged into the library anew, after a version that is
	// superseded now; its original's source is recorded.
	ep := lib + "/series/s1/s1/episodes/h2"
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemversions (id, item_id, state, dir, completedat, supersededat) VALUES
		('vP', 'h2', 'superseded', $1, '2026-10-05 09:00:00+00', '2026-10-06 09:00:05+00'),
		('vH', 'h2', 'complete',   $2, '2026-10-06 09:00:00+00', NULL)`, ep+"/versions/vP", ep+"/versions/vH")
	db.Exec(t, `UPDATE com_nalet_katalog_playbackassets SET path = $1, versionid = 'vH' WHERE id = 'p-h2'`,
		ep+"/versions/vH/package.json")
	db.Exec(t, `UPDATE com_nalet_katalog_playbackassets SET sourceid = 'src-h2' WHERE id = 'a-h2'`)
	inLibrary := `{"itemId":"h2","type":"episode",` +
		`"package":{"versionId":"vH","dir":"/var/lib/katalog/series/s1/s1/episodes/h2/versions/vH","record":"package.json",` +
		`"completedAt":"2026-10-06T09:00:00Z"},` +
		`"previous":[{"versionId":"vP","dir":"/var/lib/katalog/series/s1/s1/episodes/h2/versions/vP","record":"package.json"}],` +
		`"original":{"path":"/var/lib/katalog/media/shows/A Show/A Show S01E02-E04.mkv","sourceId":"src-h2"}}`
	check("in the library", map[string]string{
		"h2": inLibrary, "z3": coveredAnswer(inLibrary, "h2", "z3"), "a4": coveredAnswer(inLibrary, "h2", "a4"),
	})

	// h2's original is retired; z3 holds a stray file and package of its own.
	db.Exec(t, `UPDATE com_nalet_katalog_playbackassets SET kind = 'original', isprimary = false, path = $1 WHERE id = 'a-h2'`,
		ep+"/sources/src-h2")
	addAsset(t, db, "a-z3", "z3", lib+"/media/shows/A Show/A Show S01E03.mkv", true, "primary")
	addAsset(t, db, "p-z3", "z3", lib+"/packages/shows/z3/z3/manifest.json", false, "packaged")
	retired := strings.Replace(inLibrary,
		`"original":{"path":"/var/lib/katalog/media/shows/A Show/A Show S01E02-E04.mkv","sourceId":"src-h2"}`, `"original":null`, 1)
	check("retired", map[string]string{
		"h2": retired, "z3": coveredAnswer(retired, "h2", "z3"), "a4": coveredAnswer(retired, "h2", "a4"),
	})
}

// /asset answers a covered episode its holder's file, whatever it holds
// itself; the holder, and an episode with a file of its own, their own. Once
// the holder's original is retired the episodes of its file have none.
func TestTheFileOfACoveredEpisodeIsItsHolders(t *testing.T) {
	st, db := sharedFiles(t)
	addAsset(t, db, "a-z3", "z3", lib+"/media/shows/A Show/A Show S01E03.mkv", true, "primary")
	ctx := context.Background()
	h2 := Asset{Path: lib + "/media/shows/A Show/A Show S01E02-E04.mkv", IsPrimary: true}
	h6 := Asset{Path: lib + "/media/shows/A Show/A Show S01E06-E07.mkv", IsPrimary: true}
	e1 := Asset{Path: lib + "/media/shows/A Show/A Show S01E01.mkv", IsPrimary: true}
	for item, want := range map[string]Asset{"h2": h2, "z3": h2, "a4": h2, "h6": h6, "c7": h6, "e1": e1} {
		if got, err := st.PrimaryAsset(ctx, item); err != nil || got != want {
			t.Errorf("%s: %+v %v, want %+v", item, got, err, want)
		}
	}
	db.Exec(t, `UPDATE com_nalet_katalog_playbackassets SET kind = 'original', isprimary = false, path = $1 WHERE id = 'a-h2'`,
		lib+"/series/s1/s1/episodes/h2/sources/src-h2")
	for _, item := range []string{"h2", "z3", "a4", "nothing"} {
		if got, err := st.PrimaryAsset(ctx, item); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s, h2's original retired: %+v %v, want not found", item, got, err)
		}
	}
}

// A covered episode is packaged when its holder is, whatever it holds itself:
// the holder's package is its. Before migration 045, the episodes a file holds
// after its first are not; then the covered episodes of a packaged holder are,
// in id order with the rest, and those of a holder packaged later join; when
// the holder's package goes, they go with it.
func TestACoveredEpisodeIsPackagedWhenItsHolderIs(t *testing.T) {
	st, db := showWithSharedFiles(t)
	ctx := context.Background()
	check := func(when string, want ...string) {
		t.Helper()
		if ids, err := st.PackagedIDs(ctx); err != nil || strings.Join(ids, " ") != strings.Join(want, " ") {
			t.Errorf("%s: %q %v, want %q", when, ids, err, want)
		}
	}
	check("before 045", "e1", "h2", "m1")
	db.Migrate045(t) // while the service is up
	cover(t, db, "h2", "z3", "a4")
	cover(t, db, "h6", "c7")
	check("after 045", "a4", "e1", "h2", "m1", "z3")

	// h6 is packaged into the library.
	db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind, versionid)
		VALUES ('p-h6', 'h6', $1, false, 'packaged', 'v6')`, lib+"/series/s1/s1/episodes/h6/versions/v6/package.json")
	check("h6 packaged", "a4", "c7", "e1", "h2", "h6", "m1", "z3")

	// h2's package goes; z3 holds a stray package of its own.
	db.Exec(t, `DELETE FROM com_nalet_katalog_playbackassets WHERE id = 'p-h2'`)
	addAsset(t, db, "p-z3", "z3", lib+"/packages/shows/z3/z3/manifest.json", false, "packaged")
	check("h2's package gone", "c7", "e1", "h6", "m1")
}

// On a catalog without migration 045 no episode is covered: each is answered
// its own, as before, and the episodes a file holds after its first have
// nothing to play. Once 045 has run and the file's episodes are linked, the
// covered ones play their holder's, without a restart.
func TestSharedFilesOnACatalogOlderThan045(t *testing.T) {
	st, db := showWithSharedFiles(t)
	ctx := context.Background()
	for i := 1; i <= 2; i++ {
		for item, w := range map[string]string{"h2": playsH2, "z3": nothingToPlay("z3"), "h6": playsH6, "c7": nothingToPlay("c7")} {
			if got := playbackOf(t, st, item); got != w {
				t.Errorf("before 045, call %d, %s:\n got %s\nwant %s", i, item, got, w)
			}
		}
		if a, err := st.PrimaryAsset(ctx, "z3"); !errors.Is(err, ErrNotFound) {
			t.Errorf("before 045, call %d, the file of z3: %+v %v, want not found", i, a, err)
		}
	}

	db.Migrate045(t) // while the service is up
	cover(t, db, "h2", "z3", "a4")
	cover(t, db, "h6", "c7")
	for item, w := range map[string]string{"h2": playsH2, "z3": coveredAnswer(playsH2, "h2", "z3"),
		"h6": playsH6, "c7": coveredAnswer(playsH6, "h6", "c7")} {
		if got := playbackOf(t, st, item); got != w {
			t.Errorf("after 045, %s:\n got %s\nwant %s", item, got, w)
		}
	}
	if a, err := st.PrimaryAsset(ctx, "z3"); err != nil || a.Path != lib+"/media/shows/A Show/A Show S01E02-E04.mkv" {
		t.Errorf("after 045, the file of z3: %+v %v, want h2's", a, err)
	}
}

// A role that may read the items but not 045's column covers nothing, as on a
// catalog without 045; once it may read the column, it covers what the
// catalog says.
func TestSharedFilesWhenTheRoleMayNotReadTheColumn(t *testing.T) {
	_, db := sharedFiles(t)
	role, pool := db.Role(t)
	st := &Store{Pool: pool}
	ctx := context.Background()
	db.Exec(t, `GRANT SELECT (id, type, title, sorttitle, year, rating, description, tagline, durationms, seasonnumber,
		episodenumber, parent_id) ON com_nalet_katalog_items TO `+role)
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_playbackassets, com_nalet_katalog_itemversions TO `+role)
	if got, w := playbackOf(t, st, "z3"), nothingToPlay("z3"); got != w {
		t.Errorf("without the column:\n got %s\nwant %s", got, w)
	}
	if a, err := st.PrimaryAsset(ctx, "z3"); !errors.Is(err, ErrNotFound) {
		t.Errorf("without the column, the file of z3: %+v %v, want not found", a, err)
	}
	if ids, err := st.PackagedIDs(ctx); err != nil || strings.Join(ids, " ") != "e1 h2 m1" {
		t.Errorf("without the column, the packaged ids: %q %v, want e1 h2 m1", ids, err)
	}

	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_items TO `+role)
	if got, w := playbackOf(t, st, "z3"), coveredAnswer(playsH2, "h2", "z3"); got != w {
		t.Errorf("with the column:\n got %s\nwant %s", got, w)
	}
	if a, err := st.PrimaryAsset(ctx, "z3"); err != nil || a.Path != lib+"/media/shows/A Show/A Show S01E02-E04.mkv" {
		t.Errorf("with the column, the file of z3: %+v %v, want h2's", a, err)
	}
	if ids, err := st.PackagedIDs(ctx); err != nil || strings.Join(ids, " ") != "a4 e1 h2 m1 z3" {
		t.Errorf("with the column, the packaged ids: %q %v, want a4 e1 h2 m1 z3", ids, err)
	}
}
