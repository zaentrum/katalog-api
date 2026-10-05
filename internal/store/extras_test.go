package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// extrasOf is what GetItemWithIncludes lists of item's extras for the viewer of
// ctx: "id:title" each, in their order.
func extrasOf(ctx context.Context, t *testing.T, st *Store, item string) (string, error) {
	t.Helper()
	it, err := st.GetItemWithIncludes(ctx, item, IncludeOpts{Extras: true})
	var out []string
	for _, e := range it.Extras {
		out = append(out, e.ID+":"+e.Title)
	}
	return strings.Join(out, ", "), err
}

// An item lists the extras that play: packaged, not removed, not hidden, and
// with a source that is not missing. One being packaged anew plays on, as does
// one whose new package failed: each still has the package it had. One never
// packaged does not play, whatever its state says. Another item's extras are
// its own.
func TestAnItemListsTheExtrasThatPlay(t *testing.T) {
	st, db := open(t)
	db.Migrate039(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	addItem(t, db, "m2", "movie", "Another Film", 2002, 7)
	addItem(t, db, "m3", "movie", "A Film Without Extras", 2003, 7)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras
			(id, item_id, kind, title, registeredby, state, packagedat, removedat, hidden, createdat) VALUES
		('ready',        'm1', 'trailer', 'Ready',        'api',     'ready',       now(), NULL,  false, '2026-10-05 08:01:00+00'),
		('repackaging',  'm1', 'teaser',  'Repackaging',  'api',     'packaging',   now(), NULL,  false, '2026-10-05 08:02:00+00'),
		('requeued',     'm1', 'trailer', 'Requeued',     'api',     'pending',     now(), NULL,  false, '2026-10-05 08:03:00+00'),
		('refailed',     'm1', 'trailer', 'Refailed',     'api',     'failed',      now(), NULL,  false, '2026-10-05 08:04:00+00'),
		('hidden',       'm1', 'trailer', 'Hidden',       'api',     'ready',       now(), NULL,  true,  '2026-10-05 08:05:00+00'),
		('removed',      'm1', 'trailer', 'Removed',      'api',     'ready',       now(), now(), false, '2026-10-05 08:06:00+00'),
		('missing',      'm1', 'trailer', 'Missing',      'scanner', 'missing',     now(), NULL,  false, '2026-10-05 08:07:00+00'),
		('pending',      'm1', 'trailer', 'Pending',      'api',     'pending',     NULL,  NULL,  false, '2026-10-05 08:08:00+00'),
		('transcoding',  'm1', 'trailer', 'Transcoding',  'api',     'transcoding', NULL,  NULL,  false, '2026-10-05 08:09:00+00'),
		('failed',       'm1', 'trailer', 'Failed',       'api',     'failed',      NULL,  NULL,  false, '2026-10-05 08:10:00+00'),
		('unpackaged',   'm1', 'trailer', 'Unpackaged',   'api',     'ready',       NULL,  NULL,  false, '2026-10-05 08:11:00+00'),
		('theirs',       'm2', 'trailer', 'Theirs',       'api',     'ready',       now(), NULL,  false, '2026-10-05 08:00:00+00'),
		('theirs-gone',  'm3', 'trailer', 'Gone',         'api',     'ready',       now(), now(), false, '2026-10-05 08:00:00+00')`)
	ctx := context.Background()
	for item, want := range map[string]string{
		"m1": "ready:Ready, repackaging:Repackaging, requeued:Requeued, refailed:Refailed",
		"m2": "theirs:Theirs",
		"m3": "",
	} {
		if got, err := extrasOf(ctx, t, st, item); err != nil || got != want {
			t.Errorf("extras of %s: %q %v, want %q", item, got, err, want)
		}
	}
}

// Extras come in the order a viewer sees them: by the order an admin gave them,
// those without one last; then as they were taken in; then by id. Each is
// titled by its label when it has one, else by its title: an empty or blank
// label is none.
func TestExtrasAreListedInOrderUnderTheirLabels(t *testing.T) {
	st, db := open(t)
	db.Migrate039(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	// Entered in no particular order. The ids sort against the order listed,
	// but for a and b, taken in at the same time, which only their ids order:
	// e is ordered first and taken in last; d and c share an order, d taken in
	// first; f has no order and was taken in before all the others.
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras
			(id, item_id, kind, title, label, sortorder, registeredby, state, packagedat, createdat) VALUES
		('b', 'm1', 'gag-reel',  'Gag Reel',      '  Bloopers ',      NULL, 'api', 'ready', now(), '2026-10-05 08:30:00+00'),
		('c', 'm1', 'trailer',   'trailer-2.mov', 'Official Trailer', 1,    'api', 'ready', now(), '2026-10-05 09:00:00+00'),
		('f', 'm1', 'making-of', 'Making Of',     '',                 NULL, 'api', 'ready', now(), '2026-10-05 07:00:00+00'),
		('e', 'm1', 'teaser',    'teaser.mp4',    'Teaser',           0,    'api', 'ready', now(), '2026-10-05 10:00:00+00'),
		('a', 'm1', 'interview', 'Interview',     '   ',              NULL, 'api', 'ready', now(), '2026-10-05 08:30:00+00'),
		('d', 'm1', 'trailer',   'Trailer',       NULL,               1,    'api', 'ready', now(), '2026-10-05 07:30:00+00')`)
	want := "e:Teaser, d:Trailer, c:Official Trailer, f:Making Of, a:Interview, b:Bloopers"
	if got, err := extrasOf(context.Background(), t, st, "m1"); err != nil || got != want {
		t.Errorf("extras:\n got %q %v\nwant %q", got, err, want)
	}
}

// An extra carries its kind, its language and its duration; a series' extra
// that belongs to a season names it, the specials as 0, and one of the series
// as a whole names none. An extra of a film names no season, whatever its row
// says.
func TestAnExtraSaysWhatItIs(t *testing.T) {
	st, db := open(t)
	db.Migrate039(t)
	addItem(t, db, "s1", "series", "A Show", 2010, 8)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras
			(id, item_id, kind, title, language, durationms, seasonnumber, registeredby, state, packagedat, createdat) VALUES
		('show',     's1', 'trailer',       'Series Trailer',   'en',    95000, NULL, 'api', 'ready', now(), '2026-10-05 08:00:00+00'),
		('season-2', 's1', 'teaser',        'Season 2 Teaser',  'de-CH', 30500, 2,    'api', 'ready', now(), '2026-10-05 08:01:00+00'),
		('specials', 's1', 'deleted-scene', 'A Deleted Scene',  NULL,    NULL,  0,    'api', 'ready', now(), '2026-10-05 08:02:00+00'),
		('film',     'm1', 'trailer',       'Trailer',          'fr',    33000, 1,    'api', 'ready', now(), '2026-10-05 08:00:00+00')`)
	ctx := context.Background()
	describe := func(es []Extra) string {
		var out []string
		for _, e := range es {
			season := "-"
			if e.SeasonNumber != nil {
				season = fmt.Sprint(*e.SeasonNumber)
			}
			out = append(out, fmt.Sprintf("%s %s %q %q %d season %s", e.ID, e.Kind, e.Title, e.Language, e.DurationMs, season))
		}
		return strings.Join(out, "; ")
	}
	for item, want := range map[string]string{
		"s1": `show trailer "Series Trailer" "en" 95000 season -; ` +
			`season-2 teaser "Season 2 Teaser" "de-CH" 30500 season 2; ` +
			`specials deleted-scene "A Deleted Scene" "" 0 season 0`,
		"m1": `film trailer "Trailer" "fr" 33000 season -`,
	} {
		it, err := st.GetItemWithIncludes(ctx, item, IncludeOpts{Extras: true})
		if err != nil {
			t.Fatal(err)
		}
		if got := describe(it.Extras); got != want {
			t.Errorf("extras of %s:\n got %s\nwant %s", item, got, want)
		}
	}
}

// On a catalog without migration 039 an item has no extras: it is served with
// what else was asked for rather than failing. The table is looked for again
// on the next request, so its extras are listed as soon as the migration has
// run, without a restart.
func TestExtrasOnACatalogOlderThan039(t *testing.T) {
	st, db := open(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	db.Exec(t, `INSERT INTO com_nalet_katalog_genres (id, name) VALUES ('g1', 'Drama')`)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemgenres (id, item_id, genre_id) VALUES ('ig1', 'm1', 'g1')`)
	ctx := context.Background()
	inc := IncludeOpts{Genres: true, Extras: true}
	for i := 1; i <= 2; i++ {
		it, err := st.GetItemWithIncludes(ctx, "m1", inc)
		if err != nil || it.Extras != nil || len(it.Genres) != 1 {
			t.Fatalf("before 039, call %d: extras %+v, genres %q, %v; want no extras, the genre", i, it.Extras, it.Genres, err)
		}
	}

	// The migration runs while the service is up; the next request reads it.
	db.Migrate039(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras (id, item_id, kind, title, registeredby, state, packagedat)
		VALUES ('x1', 'm1', 'trailer', 'Trailer', 'api', 'ready', now())`)
	if got, err := extrasOf(ctx, t, st, "m1"); err != nil || got != "x1:Trailer" {
		t.Fatalf("after 039: %q %v, want x1:Trailer", got, err)
	}
}

// The read-only role was granted the catalog's tables before 039 created the
// extras table: an item is served without extras rather than failing, also
// while the role may read only some of the columns they are read from, and
// with them once the role is granted the table.
func TestExtrasWhenTheRoleMayNotReadThem(t *testing.T) {
	_, db := open(t)
	db.Migrate039(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7)
	db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras (id, item_id, kind, title, registeredby, state, packagedat)
		VALUES ('x1', 'm1', 'trailer', 'Trailer', 'api', 'ready', now())`)
	role, pool := db.Role(t)
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_items TO `+role)
	st := &Store{Pool: pool}
	ctx := context.Background()

	if got, err := extrasOf(ctx, t, st, "m1"); err != nil || got != "" {
		t.Fatalf("without the grant: %q %v, want the item without extras", got, err)
	}
	db.Exec(t, `GRANT SELECT (id, item_id, kind, title) ON com_nalet_katalog_itemextras TO `+role)
	if got, err := extrasOf(ctx, t, st, "m1"); err != nil || got != "" {
		t.Fatalf("granted some of the columns: %q %v, want the item without extras", got, err)
	}
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_itemextras TO `+role)
	if got, err := extrasOf(ctx, t, st, "m1"); err != nil || got != "x1:Trailer" {
		t.Fatalf("with the grant: %q %v, want x1:Trailer", got, err)
	}
}

// An item's extras are served with the item: a capped viewer gets the extras
// of a title the cap allows, and of one it leaves out neither the title nor
// its extras, as for an id there is not.
func TestACappedViewerGetsTheExtrasOfWhatTheCapAllows(t *testing.T) {
	st, db := ratedCatalog(t)
	db.Migrate039(t)
	titles := []string{"m0", "m12", "m16", "m17", "mu", "mo", "s12", "s16"}
	for _, id := range titles {
		db.Exec(t, `INSERT INTO com_nalet_katalog_itemextras (id, item_id, kind, title, registeredby, state, packagedat)
			VALUES ('x-' || $1::varchar, $1::varchar, 'trailer', 'Trailer', 'api', 'ready', now())`, id)
	}
	for _, setting := range []string{"", "show"} {
		setUnrated(t, st, db, setting)
		show := setting == "show"
		for _, maxAge := range []int{0, 6, 12, 16, 17, 18} {
			ctx := WithMaxAge(context.Background(), maxAge)
			for _, id := range titles {
				got, err := extrasOf(ctx, t, st, id)
				if allowed(maxAge, show, id) != "" {
					if err != nil || got != "x-"+id+":Trailer" {
						t.Errorf("capped at %d, unrated %q, %s: %q %v, want its extra", maxAge, setting, id, got, err)
					}
				} else if !errors.Is(err, ErrNotFound) || got != "" {
					t.Errorf("capped at %d, unrated %q, %s: %q %v, want not found and no extras", maxAge, setting, id, got, err)
				}
			}
		}
	}
	for _, id := range titles {
		if got, err := extrasOf(context.Background(), t, st, id); err != nil || got != "x-"+id+":Trailer" {
			t.Errorf("uncapped, %s: %q %v, want its extra", id, got, err)
		}
	}
}
