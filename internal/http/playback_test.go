package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zaentrum/katalog-api/internal/auth"
	"github.com/zaentrum/katalog-api/internal/config"
	"github.com/zaentrum/katalog-api/internal/store"
	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// router is the service's router on st, its bearer routes behind a verifier
// whose issuer never answers: they fail closed.
func router(t *testing.T, st *store.Store) http.Handler {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	verifier, err := auth.NewVerifier(ctx, "http://127.0.0.1:1/realms/none", "chino")
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewRouter(config.Config{}, st, verifier)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// serve serves a GET of path and answers its status and body.
func serve(h http.Handler, path string, header http.Header) (int, string) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code, strings.TrimSpace(w.Body.String())
}

// The playback lookups take no bearer, as the asset routes take none, and pay
// no heed to one sent; the item routes beside them stay behind it. Without a
// database they answer that, as every route that reads it does.
func TestThePlaybackRoutesTakeNoBearer(t *testing.T) {
	h := router(t, &store.Store{})
	for _, path := range []string{"/api/v1/items/m1/playback", "/api/v1/extras/x1/playback", "/api/v1/packaged-ids"} {
		for _, header := range []http.Header{nil, {"Authorization": {"Bearer not-a-token"}}} {
			if code, body := serve(h, path, header); code != http.StatusServiceUnavailable || body != "db not configured" {
				t.Errorf("%s (header %v): %d %q, want 503 db not configured", path, header, code, body)
			}
		}
	}
	for _, path := range []string{"/api/v1/items/m1", "/api/v1/items/m1/segments", "/api/v1/series/s1/episodes"} {
		if code, body := serve(h, path, nil); code != http.StatusServiceUnavailable || body != "auth unavailable" {
			t.Errorf("%s: %d %q, want the bearer routes' 503 auth unavailable", path, code, body)
		}
	}
}

// The playback lookups on the wire, before the library (a catalog without
// migration 040: an item's package is its packaged asset's folder, read by
// its manifest.json; an extra's is the package store's) and in it (an item's
// complete version and its superseded ones, an extra's folder in its title's,
// read by their package.json), with the packaged ids of both. An id of no
// item, of no extra or of a removed one is 404.
func TestPlaybackOnTheWire(t *testing.T) {
	db := storetest.Open(t)
	db.Migrate039(t)
	h := router(t, &store.Store{Pool: db.Pool})
	const (
		film  = "f001aeff-9c18-4183-b51b-51403af2515e"
		other = "ea886f9b-0d06-4f0f-babb-d2a1162f9b01"
		extra = "16aa63f3-5b7e-4c1a-9f1d-2b8e3c4d5a60"
		later = "2b7c9e10-3d4f-4a5b-8c6d-7e8f9a0b1c2d"
	)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title) VALUES ($1, 'movie', 'Sintel'), ($2, 'movie', 'Tears of Steel')`,
		film, other)
	db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind) VALUES
		('a1', $1, '/var/lib/katalog/media/Sintel (2010).mkv', true, 'primary'),
		('a2', $1, $2, false, 'packaged')`, film, "/var/lib/katalog/packages/movies/f0/"+film+"/manifest.json")
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras (id, item_id, kind, title, registeredby, state, packagepath, packagedat)
		VALUES ($1, $2, 'trailer', 'Trailer', 'api', 'ready', $3, '2026-10-05 08:00:00+00')`,
		extra, film, "/var/lib/katalog/packages/extras/16/"+extra)

	check := func(when, path string, wantCode int, want string) {
		t.Helper()
		code, body := serve(h, path, nil)
		t.Logf("%s: GET %s\n%s", when, path, body)
		if code != wantCode || body != want {
			t.Errorf("%s: GET %s:\n got %d %s\nwant %d %s", when, path, code, body, wantCode, want)
		}
	}
	check("before the library", "/api/v1/items/"+film+"/playback", 200, `{"itemId":"`+film+`","type":"movie",`+
		`"package":{"versionId":null,"dir":"/var/lib/katalog/packages/movies/f0/`+film+`","record":"manifest.json"},`+
		`"previous":[],"original":{"path":"/var/lib/katalog/media/Sintel (2010).mkv","sourceId":null}}`)
	check("before the library", "/api/v1/extras/"+extra+"/playback", 200, `{"extraId":"`+extra+`","itemId":"`+film+`",`+
		`"dir":"/var/lib/katalog/packages/extras/16/`+extra+`","record":"manifest.json","packagedAt":"2026-10-05T08:00:00Z"}`)
	check("before the library", "/api/v1/packaged-ids", 200, `{"ids":["`+film+`"]}`)

	// The library: the film is packaged into it anew, its original moved to
	// the arrivals; the other film is packaged there, its original retired;
	// an extra is packaged into the other film's folder.
	db.Migrate040(t)
	const v1, v2, w1 = "77c1d2e3-f405-4a6b-9c8d-0e1f2a3b4c5d", "9a2e4f60-1b2c-4d3e-8f4a-5b6c7d8e9f00", "c3d4e5f6-0718-4293-a4b5-c6d7e8f90a1b"
	fdir, odir := "/var/lib/katalog/movies/f0/"+film, "/var/lib/katalog/movies/ea/"+other
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemversions (id, item_id, state, packageid, dir, completedat, supersededat) VALUES
		($1, $2, 'superseded', 'p1', $3, '2026-10-05 09:00:00+00', '2026-10-06 09:00:01+00'),
		($4, $2, 'complete',   'p2', $5, '2026-10-06 09:00:00+00', NULL),
		($6, $7, 'complete',   'p3', $8, '2026-10-06 10:00:00+00', NULL)`,
		v1, film, fdir+"/versions/"+v1, v2, fdir+"/versions/"+v2, w1, other, odir+"/versions/"+w1)
	db.Exec(t, `UPDATE com_nalet_katalog_playbackassets SET path = '/var/lib/katalog/.work/incoming/Sintel (2010).mkv', sourceid = 's1'
		WHERE id = 'a1'`)
	db.Exec(t, `UPDATE com_nalet_katalog_playbackassets SET path = $1, versionid = $2 WHERE id = 'a2'`,
		fdir+"/versions/"+v2+"/package.json", v2)
	db.Exec(t, `INSERT INTO com_nalet_katalog_playbackassets (id, item_id, path, isprimary, kind, sourceid, versionid) VALUES
		('a3', $1, $2, false, 'original', 's2', NULL),
		('a4', $1, $3, false, 'packaged', NULL, $4)`, other, odir+"/sources/s2", odir+"/versions/"+w1+"/package.json", w1)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras (id, item_id, kind, title, registeredby, state, packagepath, packagedat,
			packageid, recordedat)
		VALUES ($1, $2, 'featurette', 'Making Of', 'api', 'ready', $3, '2026-10-06 11:00:00+00', 'p4', now())`,
		later, other, odir+"/extras/"+later)
	db.Exec(t, `UPDATE com_nalet_katalog_itemextras SET removedat = now() WHERE id = $1`, extra)

	check("in the library", "/api/v1/items/"+film+"/playback", 200, `{"itemId":"`+film+`","type":"movie",`+
		`"package":{"versionId":"`+v2+`","dir":"`+fdir+`/versions/`+v2+`","record":"package.json","completedAt":"2026-10-06T09:00:00Z"},`+
		`"previous":[{"versionId":"`+v1+`","dir":"`+fdir+`/versions/`+v1+`","record":"package.json"}],`+
		`"original":{"path":"/var/lib/katalog/.work/incoming/Sintel (2010).mkv","sourceId":"s1"}}`)
	check("in the library", "/api/v1/items/"+other+"/playback", 200, `{"itemId":"`+other+`","type":"movie",`+
		`"package":{"versionId":"`+w1+`","dir":"`+odir+`/versions/`+w1+`","record":"package.json","completedAt":"2026-10-06T10:00:00Z"},`+
		`"previous":[],"original":null}`)
	check("in the library", "/api/v1/extras/"+later+"/playback", 200, `{"extraId":"`+later+`","itemId":"`+other+`",`+
		`"dir":"`+odir+`/extras/`+later+`","record":"package.json","packagedAt":"2026-10-06T11:00:00Z"}`)
	check("in the library", "/api/v1/packaged-ids", 200, `{"ids":["`+other+`","`+film+`"]}`)
	for _, path := range []string{"/api/v1/items/no-such-item/playback", "/api/v1/extras/" + extra + "/playback",
		"/api/v1/extras/no-such-extra/playback"} {
		check("in the library", path, 404, "not found")
	}
}
