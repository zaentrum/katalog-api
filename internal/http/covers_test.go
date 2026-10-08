package http

import (
	"testing"

	"github.com/zaentrum/katalog-api/internal/store"
	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// The ids of a show whose finale is one file: the holder (its first part, the
// file's first episode) and the part it covers, and an episode with a file of
// its own.
const (
	show      = "3f6b2c1e-8d4a-4e7f-9b0c-5a1d2e3f4a5b"
	finale    = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
	finaleTwo = "0c9d8e7f-6a5b-4c3d-9e1f-2a3b4c5d6e7f"
	opener    = "7d6c5b4a-3e2f-4a1b-8c0d-9e8f7a6b5c4d"
)

// sharedFinale is a catalog with migrations 040 and 045 whose show holds an
// opener with a file of its own and a finale in two parts, one file packaged
// into the library for its first part, the holder, which covers the second.
func sharedFinale(t *testing.T) *storetest.DB {
	t.Helper()
	db := storetest.Open(t)
	db.Migrate040(t)
	db.Migrate045(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, parent_id, seasonnumber, episodenumber, coveredby) VALUES
		($1, 'series',  'A Show',             NULL, NULL, NULL, NULL),
		($2, 'episode', 'Pilot',              $1,   1,    1,    NULL),
		($3, 'episode', 'Finale, Part One',   $1,   1,    9,    NULL),
		($4, 'episode', 'Finale, Part Two',   $1,   1,    10,   $3)`, show, opener, finale, finaleTwo)
	dir := "/var/lib/katalog/series/3f/" + show + "/episodes/" + finale
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemversions (id, item_id, state, packageid, dir, completedat)
		VALUES ('5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d', $1, 'complete', 'p1', $2, '2026-10-08 09:00:00+00')`,
		finale, dir+"/versions/5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d")
	db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind, sourceid, versionid) VALUES
		('a1', $1, '/var/lib/katalog/.work/incoming/A Show S01E09-E10.mkv', true, 'primary', 's1', NULL),
		('a2', $1, $2, false, 'packaged', NULL, '5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d'),
		('a3', $3, '/var/lib/katalog/.work/incoming/A Show S01E01.mkv', true, 'primary', 's2', NULL)`,
		finale, dir+"/versions/5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d/package.json", opener)
	return db
}

// A covered episode's playback lookups on the wire, as the stream services ask
// them, without a bearer: /playback answers the holder's package and original
// and names the holder in coveredBy, which no other item's answer carries, the
// holder's among them; /asset answers the holder's file.
func TestACoveredEpisodesPlaybackOnTheWire(t *testing.T) {
	h := router(t, &store.Store{Pool: sharedFinale(t).Pool})
	const pkg = `"package":{"versionId":"5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d",` +
		`"dir":"/var/lib/katalog/series/3f/` + show + `/episodes/` + finale + `/versions/5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d",` +
		`"record":"package.json","completedAt":"2026-10-08T09:00:00Z"},"previous":[],` +
		`"original":{"path":"/var/lib/katalog/.work/incoming/A Show S01E09-E10.mkv","sourceId":"s1"}}`
	for path, want := range map[string]string{
		"/api/v1/items/" + finaleTwo + "/playback": `{"itemId":"` + finaleTwo + `","type":"episode","coveredBy":"` + finale + `",` + pkg,
		"/api/v1/items/" + finale + "/playback":    `{"itemId":"` + finale + `","type":"episode",` + pkg,
		"/api/v1/items/" + finaleTwo + "/asset":    `{"path":"/var/lib/katalog/.work/incoming/A Show S01E09-E10.mkv","isPrimary":true}`,
		"/api/v1/items/" + opener + "/asset":       `{"path":"/var/lib/katalog/.work/incoming/A Show S01E01.mkv","isPrimary":true}`,
	} {
		code, body := serve(h, path, nil)
		t.Logf("GET %s\n%s", path, body)
		if code != 200 || body != want {
			t.Errorf("GET %s:\n got %d %s\nwant 200 %s", path, code, body, want)
		}
	}
}
