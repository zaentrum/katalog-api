package store

import (
	"context"
	"testing"
)

func addSubtitle(t *testing.T, db interface {
	Exec(testing.TB, string, ...any)
}, id, item, lang, label string, isDefault bool) {
	t.Helper()
	db.Exec(t, `INSERT INTO com_nalet_katalog_subtitleassets (id, item_id, path, format, lang, label, isdefault)
		VALUES ($1, $2, $3, 'webvtt', $4, $5, $6)`, id, item, "/subs/"+id+".vtt", lang, label, isDefault)
}

// Before migration 038 the catalog says of no subtitle that it is forced.
func TestSubtitlesOnACatalogOlderThan038(t *testing.T) {
	st, db := open(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	addSubtitle(t, db, "s1", "m1", "en", "English", true)
	addSubtitle(t, db, "s2", "m1", "en", "English (Forced)", false)
	subs, err := st.listSubtitlesFor(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 2 {
		t.Fatalf("subtitles: %+v", subs)
	}
	for _, s := range subs {
		if s.Forced {
			t.Errorf("%s is forced on a catalog without isforced", s.ID)
		}
	}
}

// After it, a subtitle the packager marked forced says so; the others don't.
func TestSubtitlesSayWhichIsForced(t *testing.T) {
	st, db := open(t)
	db.Migrate038(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	addSubtitle(t, db, "s1", "m1", "en", "English", true)
	addSubtitle(t, db, "s2", "m1", "en", "Signs", false)
	db.Exec(t, `UPDATE com_nalet_katalog_subtitleassets SET isforced = true WHERE id = 's2'`)
	subs, err := st.listSubtitlesFor(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range subs {
		got[s.ID] = s.Forced
	}
	if len(got) != 2 || got["s1"] || !got["s2"] {
		t.Errorf("forced: %v", got)
	}
}
