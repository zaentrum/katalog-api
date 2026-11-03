package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// styledOf is what GetItemWithIncludes lists of item's subtitles and fonts:
// "id:format:styled" each subtitle, then "id:format:names" each font.
func styledOf(ctx context.Context, t *testing.T, st *Store, item string) (string, error) {
	t.Helper()
	it, err := st.GetItemWithIncludes(ctx, item, IncludeOpts{Subtitles: true})
	out := ""
	for _, s := range it.Subtitles {
		out += fmt.Sprintf("%s:%s:%s ", s.ID, s.Format, s.StyledFormat)
	}
	out += "|"
	for _, f := range it.Fonts {
		out += fmt.Sprintf(" %s:%s:%q", f.ID, f.Format, f.Names)
	}
	return out, err
}

// A subtitle whose package keeps its styled original beside it says the
// original's format; the item's fonts come with its subtitles, in their
// order, a font of no format without one. Its subtitle's file is the one its
// format names, as before. Another item's fonts are its own.
func TestAnItemSaysItsStyledSubtitlesAndFonts(t *testing.T) {
	st, db := open(t)
	db.Migrate053(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	addItem(t, db, "m2", "movie", "Another Film", 2002, 7)
	db.Exec(t, `INSERT INTO com_nalet_katalog_subtitleassets (id, item_id, path, format, lang, styledpath, styledformat) VALUES
		('s1', 'm1', '/v/subs/0.vtt', 'webvtt', 'eng', '/v/subs/0.ass', 'ass'),
		('s2', 'm1', '/v/subs/1.vtt', 'webvtt', 'ger', NULL, NULL),
		('s3', 'm1', '/v/subs/2.sup', 'pgs', 'fre', NULL, 'ass'),
		('t1', 'm2', '/p/subs/0.vtt', 'webvtt', 'eng', '/p/subs/0.ass', 'ass')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_fontassets (id, item_id, versionid, path, format, names, ordinal) VALUES
		('f2', 'm1', 'v', '/v/subs/fonts/1.bin', NULL, '[]', 1),
		('f1', 'm1', 'v', '/v/subs/fonts/0.ttf', 'ttf', '["Example Sans", "ExampleSans-Regular"]', 0),
		('g1', 'm2', NULL, '/p/subs/fonts/0.otf', 'otf', '["Example Serif"]', 0)`)
	ctx := context.Background()
	got, err := styledOf(ctx, t, st, "m1")
	if want := `s1:webvtt:ass s3:pgs: s2:webvtt: | f1:ttf:["Example Sans" "ExampleSans-Regular"] f2::[]`; err != nil || got != want {
		t.Errorf("m1: %q %v, want %q", got, err, want)
	}
	if got, err := styledOf(ctx, t, st, "m2"); err != nil || got != `t1:webvtt:ass | g1:otf:["Example Serif"]` {
		t.Errorf("m2: %q %v", got, err)
	}
	// What a stream service resolves an id to.
	if a, err := st.SubtitleAsset(ctx, "s1"); err != nil || a.Path != "/v/subs/0.vtt" || a.StyledPath != "/v/subs/0.ass" ||
		a.StyledFormat != "ass" {
		t.Errorf("SubtitleAsset s1: %+v %v", a, err)
	}
	if a, err := st.SubtitleAsset(ctx, "s2"); err != nil || a.StyledPath != "" || a.StyledFormat != "" {
		t.Errorf("SubtitleAsset s2: %+v %v", a, err)
	}
	if a, err := st.FontAsset(ctx, "f1"); err != nil || a != (FontAssetInfo{ItemID: "m1", Path: "/v/subs/fonts/0.ttf", Format: "ttf"}) {
		t.Errorf("FontAsset f1: %+v %v", a, err)
	}
	if _, err := st.FontAsset(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FontAsset of no font: %v", err)
	}
}

// Before katalog-manager's migration 053 no subtitle has a styled original
// and no item a font, and no font is found; the migration runs while the
// service is up, and the next request reads it.
func TestStyledSubtitlesOnACatalogOlderThan053(t *testing.T) {
	st, db := open(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	db.Exec(t, `INSERT INTO com_nalet_katalog_subtitleassets (id, item_id, path, format, lang)
		VALUES ('s1', 'm1', '/v/subs/0.vtt', 'webvtt', 'eng')`)
	ctx := context.Background()
	if got, err := styledOf(ctx, t, st, "m1"); err != nil || got != "s1:webvtt: |" {
		t.Fatalf("before 053: %q %v", got, err)
	}
	if a, err := st.SubtitleAsset(ctx, "s1"); err != nil || a.Path != "/v/subs/0.vtt" || a.StyledPath != "" {
		t.Errorf("SubtitleAsset before 053: %+v %v", a, err)
	}
	if _, err := st.FontAsset(ctx, "f1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FontAsset before 053: %v", err)
	}
	db.Migrate053(t)
	db.Exec(t, `UPDATE com_nalet_katalog_subtitleassets SET styledpath = '/v/subs/0.ass', styledformat = 'ass'`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_fontassets (id, item_id, path, format, names, ordinal)
		VALUES ('f1', 'm1', '/v/subs/fonts/0.ttf', 'ttf', '["Example Sans"]', 0)`)
	if got, err := styledOf(ctx, t, st, "m1"); err != nil || got != `s1:webvtt:ass | f1:ttf:["Example Sans"]` {
		t.Errorf("after 053: %q %v", got, err)
	}
}

// The read-only role was granted the catalog's tables before 053 created the
// fonts' table: an item is served with its subtitles and no fonts rather than
// failing, and with them once the role is granted the table.
func TestFontsWhenTheRoleMayNotReadThem(t *testing.T) {
	_, db := open(t)
	db.Migrate053(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	db.Exec(t, `INSERT INTO com_nalet_katalog_subtitleassets (id, item_id, path, format, lang, styledpath, styledformat)
		VALUES ('s1', 'm1', '/v/subs/0.vtt', 'webvtt', 'eng', '/v/subs/0.ass', 'ass')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_fontassets (id, item_id, path, format, names, ordinal)
		VALUES ('f1', 'm1', '/v/subs/fonts/0.ttf', 'ttf', '["Example Sans"]', 0)`)
	role, pool := db.Role(t)
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_items, com_nalet_katalog_subtitleassets TO `+role)
	st := &Store{Pool: pool}
	ctx := context.Background()
	if got, err := styledOf(ctx, t, st, "m1"); err != nil || got != "s1:webvtt:ass |" {
		t.Fatalf("without the grant: %q %v", got, err)
	}
	if _, err := st.FontAsset(ctx, "f1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FontAsset without the grant: %v", err)
	}
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_fontassets TO `+role)
	if got, err := styledOf(ctx, t, st, "m1"); err != nil || got != `s1:webvtt:ass | f1:ttf:["Example Sans"]` {
		t.Errorf("with the grant: %q %v", got, err)
	}
}

// The fonts are the styled originals': an item none of whose subtitles keeps
// one lists none, whatever its package carries, and lists them once one does.
func TestAnItemListsItsFontsBesideAStyledSubtitleAlone(t *testing.T) {
	st, db := open(t)
	db.Migrate053(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	db.Exec(t, `INSERT INTO com_nalet_katalog_subtitleassets (id, item_id, path, format, lang)
		VALUES ('s1', 'm1', '/v/subs/0.vtt', 'webvtt', 'eng')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_fontassets (id, item_id, path, format, names, ordinal)
		VALUES ('f1', 'm1', '/v/subs/fonts/0.ttf', 'ttf', '["Example Sans"]', 0)`)
	ctx := context.Background()
	if got, err := styledOf(ctx, t, st, "m1"); err != nil || got != "s1:webvtt: |" {
		t.Errorf("no styled subtitle: %q %v", got, err)
	}
	db.Exec(t, `UPDATE com_nalet_katalog_subtitleassets SET styledpath = '/v/subs/0.ass', styledformat = 'ass'`)
	if got, err := styledOf(ctx, t, st, "m1"); err != nil || got != `s1:webvtt:ass | f1:ttf:["Example Sans"]` {
		t.Errorf("a styled subtitle: %q %v", got, err)
	}
}
