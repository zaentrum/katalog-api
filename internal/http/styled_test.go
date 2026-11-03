package http

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/zaentrum/katalog-api/internal/store"
	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// A styled subtitle on the wire, with include=subtitles: its entry is the
// one every client reads, with styled_format beside it, and the item's fonts
// come with the subtitles, each by its id, its format when it has one and the
// names it is asked for by. An item without fonts, one none of whose
// subtitles keeps a styled original whatever fonts its package carries, or
// one asked for without its subtitles, has no fonts field. The stream services resolve a subtitle's
// styled original and a font by their ids, and a font of no id is not found.
func TestStyledSubtitlesAndFontsOnTheWire(t *testing.T) {
	db := storetest.Open(t)
	db.Migrate053(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title) VALUES ('m1', 'movie', 'A Film'), ('m2', 'movie', 'Plain')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_subtitleassets (id, item_id, path, format, lang, label, isdefault, styledpath, styledformat)
		VALUES ('s1', 'm1', '/v/subs/0.vtt', 'webvtt', 'en', 'English', true, '/v/subs/0.ass', 'ass'),
		       ('s2', 'm1', '/v/subs/1.vtt', 'webvtt', 'de', 'Deutsch', false, NULL, NULL),
		       ('s3', 'm2', '/w/subs/0.vtt', 'webvtt', 'en', 'English', true, NULL, NULL)`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_fontassets (id, item_id, versionid, path, format, names, ordinal) VALUES
		('f1', 'm1', 'v', '/v/subs/fonts/0.ttf', 'ttf', '["Example Sans", "ExampleSans-Regular"]', 0),
		('f2', 'm1', 'v', '/v/subs/fonts/1.bin', NULL, '[]', 1),
		('g1', 'm2', 'w', '/w/subs/fonts/0.ttf', 'ttf', '["Example Serif"]', 0)`)
	h := &ItemsHandler{Store: &store.Store{Pool: db.Pool}}
	r := chi.NewRouter()
	r.Get("/items/{id}", h.Get)
	r.Get("/subtitles/{id}/asset", h.SubtitleAsset)
	r.Get("/fonts/{id}/asset", h.FontAsset)

	body, _ := get(t, r, "/items/m1?include=subtitles", nil)
	if want := []any{
		map[string]any{"id": "s1", "lang": "en", "label": "English", "format": "webvtt", "default": true, "styled_format": "ass"},
		map[string]any{"id": "s2", "lang": "de", "label": "Deutsch", "format": "webvtt"},
	}; !reflect.DeepEqual(body["subtitles"], want) {
		t.Errorf("subtitles %v, want %v", body["subtitles"], want)
	}
	if want := []any{
		map[string]any{"id": "f1", "format": "ttf", "names": []any{"Example Sans", "ExampleSans-Regular"}},
		map[string]any{"id": "f2", "names": []any{}},
	}; !reflect.DeepEqual(body["fonts"], want) {
		t.Errorf("fonts %v, want %v", body["fonts"], want)
	}
	for _, path := range []string{"/items/m1", "/items/m1?include=genres", "/items/m2?include=subtitles"} {
		if body, _ := get(t, r, path, nil); body["fonts"] != nil {
			t.Errorf("%s: fonts %v, want no fonts field", path, body["fonts"])
		}
	}
	if body, _ := get(t, r, "/subtitles/s1/asset", nil); !reflect.DeepEqual(body, map[string]any{"itemId": "m1",
		"path": "/v/subs/0.vtt", "format": "webvtt", "lang": "en", "styledPath": "/v/subs/0.ass", "styledFormat": "ass"}) {
		t.Errorf("subtitle s1's asset: %v", body)
	}
	if body, _ := get(t, r, "/subtitles/s2/asset", nil); body["styledPath"] != nil || body["styledFormat"] != nil {
		t.Errorf("subtitle s2's asset: %v, want no styled original", body)
	}
	if body, _ := get(t, r, "/fonts/f1/asset", nil); !reflect.DeepEqual(body, map[string]any{"itemId": "m1",
		"path": "/v/subs/fonts/0.ttf", "format": "ttf"}) {
		t.Errorf("font f1's asset: %v", body)
	}
	if body, _ := get(t, r, "/fonts/f2/asset", nil); !reflect.DeepEqual(body, map[string]any{"itemId": "m1",
		"path": "/v/subs/fonts/1.bin"}) {
		t.Errorf("font f2's asset: %v", body)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/fonts/nope/asset", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("a font of no id: %d %s, want 404", rec.Code, rec.Body)
	}
}

// A catalog without migration 053 serves an item's subtitles as before — no
// styled_format, no fonts field — and finds no font.
func TestStyledSubtitlesOnTheWireWithoutTheMigration(t *testing.T) {
	db := storetest.Open(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title) VALUES ('m1', 'movie', 'A Film')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_subtitleassets (id, item_id, path, format, lang)
		VALUES ('s1', 'm1', '/v/subs/0.vtt', 'webvtt', 'en')`)
	h := &ItemsHandler{Store: &store.Store{Pool: db.Pool}}
	r := chi.NewRouter()
	r.Get("/items/{id}", h.Get)
	r.Get("/fonts/{id}/asset", h.FontAsset)

	body, _ := get(t, r, "/items/m1?include=subtitles", nil)
	if _, ok := body["fonts"]; ok || !reflect.DeepEqual(body["subtitles"], []any{
		map[string]any{"id": "s1", "lang": "en", "format": "webvtt"}}) {
		t.Errorf("without 053: %v", body)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/fonts/f1/asset", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("a font without 053: %d %s, want 404", rec.Code, rec.Body)
	}
}
