package store

import (
	"context"
	"errors"
	"testing"

	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// addAsset adds a playback asset of item: its path, whether it is marked
// primary, and its kind ("" for a row without one).
func addAsset(t *testing.T, db *storetest.DB, id, item, path string, primary bool, kind string) {
	t.Helper()
	db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))`, id, item, path, primary, kind)
}

// /asset answers the file an item was taken in from: the one marked primary,
// else the first of its files by path; a row without a kind is a file. Never
// the item's package (kind packaged), nor an original retired once its package
// was recorded (kind original, whose file is gone), whatever they are marked
// and wherever they sort: an item with nothing else has no asset.
func TestThePrimaryAssetIsAFileOfTheItem(t *testing.T) {
	st, db := open(t)
	const lib = "/var/lib/katalog"
	addAsset(t, db, "a1", "marked", lib+"/media/b.mkv", true, "primary")
	addAsset(t, db, "a2", "marked", lib+"/media/a.mkv", false, "primary")
	addAsset(t, db, "a3", "marked", lib+"/packages/movies/ma/marked/manifest.json", false, "packaged")
	addAsset(t, db, "a4", "unmarked", lib+"/media/z.mkv", false, "primary")
	addAsset(t, db, "a5", "unmarked", lib+"/media/y.mkv", false, "")
	addAsset(t, db, "a6", "unmarked", lib+"/movies/un/unmarked/versions/v1/package.json", false, "packaged")
	addAsset(t, db, "a7", "package-marked", lib+"/movies/pa/package-marked/versions/v1/package.json", true, "packaged")
	addAsset(t, db, "a8", "package-marked", lib+"/.work/incoming/Film.mkv", false, "primary")
	addAsset(t, db, "a9", "retired", lib+"/movies/re/retired/sources/s1", false, "original")
	addAsset(t, db, "a10", "retired", lib+"/movies/re/retired/versions/v1/package.json", false, "packaged")
	addAsset(t, db, "a11", "package-only", lib+"/packages/shows/pa/package-only/manifest.json", false, "packaged")
	ctx := context.Background()
	for item, want := range map[string]Asset{
		"marked":         {Path: lib + "/media/b.mkv", IsPrimary: true},
		"unmarked":       {Path: lib + "/media/y.mkv"},
		"package-marked": {Path: lib + "/.work/incoming/Film.mkv"},
	} {
		if got, err := st.PrimaryAsset(ctx, item); err != nil || got != want {
			t.Errorf("%s: %+v %v, want %+v", item, got, err, want)
		}
	}
	for _, item := range []string{"retired", "package-only", "nothing"} {
		if got, err := st.PrimaryAsset(ctx, item); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %+v %v, want not found", item, got, err)
		}
	}
}
