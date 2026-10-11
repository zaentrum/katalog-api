package http

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

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

// finaleCatalog is a catalog with migration 040 whose show holds an opener with
// a file of its own and a finale in two parts, one file packaged into the
// library for its first part; linked, it has migration 045 too, and the first
// part, the holder, covers the second.
func finaleCatalog(t *testing.T, linked bool) *storetest.DB {
	t.Helper()
	db := storetest.Open(t)
	db.Migrate040(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, parent_id, seasonnumber, episodenumber) VALUES
		($1, 'series',  'A Show',           NULL, NULL, NULL),
		($2, 'episode', 'Pilot',            $1,   1,    1),
		($3, 'episode', 'Finale, Part One', $1,   1,    9),
		($4, 'episode', 'Finale, Part Two', $1,   1,    10)`, show, opener, finale, finaleTwo)
	dir := "/var/lib/katalog/series/3f/" + show + "/episodes/" + finale
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemversions (id, item_id, state, packageid, dir, completedat)
		VALUES ('5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d', $1, 'complete', 'p1', $2, '2026-10-08 09:00:00+00')`,
		finale, dir+"/versions/5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d")
	db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind, sourceid, versionid) VALUES
		('a1', $1, '/var/lib/katalog/.work/incoming/A Show S01E09-E10.mkv', true, 'primary', 's1', NULL),
		('a2', $1, $2, false, 'packaged', NULL, '5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d'),
		('a3', $3, '/var/lib/katalog/.work/incoming/A Show S01E01.mkv', true, 'primary', 's2', NULL)`,
		finale, dir+"/versions/5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d/package.json", opener)
	if linked {
		db.Migrate045(t)
		db.Exec(t, `UPDATE com_nalet_katalog_items SET coveredby = $1 WHERE id = $2`, finale, finaleTwo)
	}
	return db
}

// A covered episode's playback lookups on the wire, as the stream services ask
// them, without a bearer: /playback answers the holder's package and original
// and names the holder in coveredBy, which no other item's answer carries, the
// holder's among them; /asset answers the holder's file; and the packaged ids
// list it beside its holder.
func TestACoveredEpisodesPlaybackOnTheWire(t *testing.T) {
	h := router(t, &store.Store{Pool: finaleCatalog(t, true).Pool})
	const pkg = `"package":{"versionId":"5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d",` +
		`"dir":"/var/lib/katalog/series/3f/` + show + `/episodes/` + finale + `/versions/5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d",` +
		`"record":"package.json","completedAt":"2026-10-08T09:00:00Z"},"previous":[],` +
		`"original":{"path":"/var/lib/katalog/.work/incoming/A Show S01E09-E10.mkv","sourceId":"s1"}}`
	for path, want := range map[string]string{
		"/api/v1/items/" + finaleTwo + "/playback": `{"itemId":"` + finaleTwo + `","type":"episode","coveredBy":"` + finale + `",` + pkg,
		"/api/v1/items/" + finale + "/playback":    `{"itemId":"` + finale + `","type":"episode",` + pkg,
		"/api/v1/items/" + finaleTwo + "/asset":    `{"path":"/var/lib/katalog/.work/incoming/A Show S01E09-E10.mkv","isPrimary":true}`,
		"/api/v1/items/" + opener + "/asset":       `{"path":"/var/lib/katalog/.work/incoming/A Show S01E01.mkv","isPrimary":true}`,
		"/api/v1/packaged-ids":                     `{"ids":["` + finaleTwo + `","` + finale + `"]}`,
	} {
		code, body := serve(h, path, nil)
		t.Logf("GET %s\n%s", path, body)
		if code != 200 || body != want {
			t.Errorf("GET %s:\n got %d %s\nwant 200 %s", path, code, body, want)
		}
	}
}

// fileFields is what an item on the wire says of the file it shares: those of
// coveredBy, covers and episodeEnd it sends.
func fileFields(item map[string]any) map[string]any {
	out := map[string]any{}
	for _, k := range []string{"coveredBy", "covers", "episodeEnd"} {
		if v, ok := item[k]; ok {
			out[k] = v
		}
	}
	return out
}

// An episode on the wire says which file it shares, as the product BFFs read
// it: on the item by id, with its associations or without, a series' episodes
// and the list of episodes. A covered episode sends
// coveredBy, the holder's id; the holder sends covers, the ids of the other
// episodes its file holds in episode order, and episodeEnd, the number of the
// last of them. An episode with a file of its own sends none of them, and on a
// catalog without migration 045 no episode does: each sends what it sent
// before, key for key.
func TestSharedFilesOnTheWire(t *testing.T) {
	before := []string{"episode_number", "id", "parent_id", "season_number", "title", "type"}
	for _, linked := range []bool{true, false} {
		db := finaleCatalog(t, linked)
		hd := &ItemsHandler{Store: &store.Store{Pool: db.Pool}}
		r := chi.NewRouter()
		r.Get("/items/{id}", hd.Get)
		r.Get("/series/{id}/episodes", hd.SeriesEpisodes)
		r.Get("/episodes", hd.listByType("episode"))

		want := map[string]map[string]any{opener: {}, finale: {}, finaleTwo: {}}
		if linked {
			want[finale] = map[string]any{"covers": []any{finaleTwo}, "episodeEnd": 10.0}
			want[finaleTwo] = map[string]any{"coveredBy": finale}
		}
		check := func(where, id string, item map[string]any) {
			t.Helper()
			wantKeys := append(keys(want[id]), before...)
			sort.Strings(wantKeys)
			if got := fileFields(item); !reflect.DeepEqual(got, want[id]) || !reflect.DeepEqual(keys(item), wantKeys) {
				t.Errorf("045 %v, %s, %s: %v (keys %q), want %v (keys %q)", linked, where, id, got, keys(item), want[id], wantKeys)
			}
		}
		for _, id := range []string{opener, finale, finaleTwo} {
			for _, path := range []string{"/items/" + id, "/items/" + id + "?include=genres,cast,subtitles,extras,segments"} {
				body, _ := get(t, r, path, nil)
				check(path, id, body)
			}
		}
		for _, path := range []string{"/series/" + show + "/episodes", "/episodes"} {
			body, _ := get(t, r, path, nil)
			items, _ := body["items"].([]any)
			var ids []string
			for _, it := range items {
				item := it.(map[string]any)
				id, _ := item["id"].(string)
				ids = append(ids, id)
				check(path, id, item)
			}
			if w := []string{opener, finale, finaleTwo}; !reflect.DeepEqual(ids, w) {
				t.Errorf("045 %v, %s: %q, want %q in episode order", linked, path, ids, w)
			}
		}
	}
}

// A covered episode's subtitles and segments on the wire are those of the file
// it plays, its holder's: the subtitles with include=subtitles, the summary
// with include=segments, and the segments by themselves. An episode with a
// file of its own has its own.
func TestACoveredEpisodesSubtitlesAndSegmentsOnTheWire(t *testing.T) {
	db := finaleCatalog(t, true)
	db.Exec(t, `INSERT INTO com_nalet_katalog_subtitleassets (id, item_id, path, format, lang, label, isdefault)
		VALUES ('e2f3a4b5-c6d7-4e8f-9a0b-1c2d3e4f5a6b', $1, $2, 'webvtt', 'en', 'English', true)`,
		finale, "/var/lib/katalog/series/3f/"+show+"/episodes/"+finale+"/versions/5b4a3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c1d/subs/en.vtt")
	db.Exec(t, `INSERT INTO com_nalet_katalog_mediasegments (id, item_id, kind, startms, endms, source)
		VALUES ('f1e2d3c4-b5a6-4978-8a9b-0c1d2e3f4a5b', $1, 'intro', 30000, 90000, 'chapter')`, finale)
	hd := &ItemsHandler{Store: &store.Store{Pool: db.Pool}}
	r := chi.NewRouter()
	r.Get("/items/{id}", hd.Get)
	r.Get("/items/{id}/segments", hd.Segments)

	subtitles := []any{map[string]any{"id": "e2f3a4b5-c6d7-4e8f-9a0b-1c2d3e4f5a6b", "lang": "en", "label": "English",
		"format": "webvtt", "default": true}}
	summary := map[string]any{"count": 1.0, "has_intro": true, "has_credits": false, "has_recap": false}
	segments := []any{map[string]any{"id": "f1e2d3c4-b5a6-4978-8a9b-0c1d2e3f4a5b", "kind": "intro", "start_ms": 30000.0,
		"end_ms": 90000.0, "source": "chapter"}}
	for _, id := range []string{finale, finaleTwo} {
		body, _ := get(t, r, "/items/"+id+"?include=subtitles,segments", nil)
		if !reflect.DeepEqual(body["subtitles"], subtitles) || !reflect.DeepEqual(body["segments"], summary) {
			t.Errorf("%s: subtitles %v, segments %v; want the holder's, %v and %v", id, body["subtitles"], body["segments"],
				subtitles, summary)
		}
		if body, _ = get(t, r, "/items/"+id+"/segments", nil); !reflect.DeepEqual(body["items"], segments) {
			t.Errorf("%s/segments: %v, want the holder's, %v", id, body["items"], segments)
		}
	}
	body, _ := get(t, r, "/items/"+opener+"?include=subtitles,segments", nil)
	if _, ok := body["subtitles"]; ok || body["segments"] != nil {
		t.Errorf("the opener: %v, want neither subtitles nor segments, as it has none", body)
	}
}

// A capped viewer on the wire is held to the strictest episode of a file, on
// every route: the show is rated 12, and an admin rated one part of its finale
// 16, the second part or the first (the holder). Capped at 12, neither part is
// served: by id, with its associations or its segments, each the 404 of an id
// there is not, nor in the series' episodes, nor by /api/v1/visible, through
// which a BFF holds a capped viewer's playback, subtitles and packaged ids to
// the cap; the opener, a file of its own rated 12, is. Capped at 16, all are.
func TestAFileRatedByItsStrictestEpisodeOnTheWire(t *testing.T) {
	for _, stricter := range []string{finaleTwo, finale} {
		db := finaleCatalog(t, true)
		db.Migrate036(t)
		db.Exec(t, `UPDATE com_nalet_katalog_items SET certification = '12', certification_country = 'DE', min_age = 12
			WHERE id = $1`, show)
		db.Exec(t, `UPDATE com_nalet_katalog_items SET min_age_override = 16 WHERE id = $1`, stricter)
		db.Exec(t, `INSERT INTO com_nalet_katalog_mediasegments (id, item_id, kind, startms, endms, source)
			VALUES ('f1e2d3c4-b5a6-4978-8a9b-0c1d2e3f4a5b', $1, 'intro', 30000, 90000, 'chapter')`, finale)
		st := &store.Store{Pool: db.Pool}
		hd := &ItemsHandler{Store: st}
		r := chi.NewRouter()
		r.Use(capped)
		r.Get("/items/{id}", hd.Get)
		r.Get("/items/{id}/segments", hd.Segments)
		r.Get("/series/{id}/episodes", hd.SeriesEpisodes)
		capAt := func(age, path string) (int, string) { return serve(r, path+"?max_rating="+age, nil) }
		at := map[string]string{finale: "the holder rated 16", finaleTwo: "the covered part rated 16"}[stricter]

		for _, path := range []string{"/items/{id}", "/items/{id}/segments"} {
			wantCode, wantBody := capAt("12", strings.Replace(path, "{id}", "no-such-id", 1))
			for _, id := range []string{finale, finaleTwo} {
				p := strings.Replace(path, "{id}", id, 1)
				if code, body := capAt("12", p); code != wantCode || body != wantBody {
					t.Errorf("%s, capped at 12, %s: %d %q, want %d %q as for an id there is not", at, p, code, body, wantCode, wantBody)
				}
				if code, _ := capAt("16", p); code != 200 {
					t.Errorf("%s, capped at 16, %s: %d, want 200", at, p, code)
				}
			}
		}
		for age, want := range map[string]string{"12": opener, "16": opener + " " + finale + " " + finaleTwo} {
			code, body := capAt(age, "/series/"+show+"/episodes")
			var got struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			_ = json.Unmarshal([]byte(body), &got)
			var ids []string
			for _, it := range got.Items {
				ids = append(ids, it.ID)
			}
			if code != 200 || strings.Join(ids, " ") != want {
				t.Errorf("%s, capped at %s, the episodes: %d %q, want %q", at, age, code, ids, want)
			}
		}
		full := router(t, st)
		for age, want := range map[string]string{
			"12": `{"ids":["` + opener + `"]}`,
			"16": `{"ids":["` + finaleTwo + `","` + opener + `","` + finale + `"]}`,
		} {
			path := "/api/v1/visible?ids=" + finaleTwo + "," + opener + "," + finale + "&max_rating=" + age
			if code, body := serve(full, path, nil); code != 200 || body != want {
				t.Errorf("%s, %s: %d %s, want %s", at, path, code, body, want)
			}
		}
	}
}
