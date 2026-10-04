package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// A title's age rating is katalog-manager's (its migration 036): the
// certification TMDB gives the title in a country, the minimum age it means
// (min_age) and an admin's override (min_age_override). An episode carries no
// certification of its own: it is rated as its series. A kid's account is
// capped at an age, and every read of a viewer's leaves out what is rated
// above the cap, and what nothing rates unless the catalog's setting
// ratings.unrated_for_capped says show.

type ctxKey int

const maxAgeKey ctxKey = 0

// WithMaxAge is ctx for a viewer capped at age: every read of the store made
// with it leaves out the titles rated above age, and those nothing rates
// unless ratings.unrated_for_capped says show, as if the catalog did not hold
// them (an item by id is ErrNotFound).
func WithMaxAge(ctx context.Context, age int) context.Context {
	return context.WithValue(ctx, maxAgeKey, age)
}

// maxAgeOf is the cap a read is made for; nil for an uncapped viewer.
func maxAgeOf(ctx context.Context) *int {
	if age, ok := ctx.Value(maxAgeKey).(int); ok {
		return &age
	}
	return nil
}

// ratingColumns are the columns migration 036 adds to com_nalet_katalog_items
// that a title's rating is read from.
var ratingColumns = []string{"certification", "certification_country", "min_age", "min_age_override"}

// fromItems is where an item is read from: i, joined with its parent as par
// (an episode's series), whose rating the item's falls back to.
const fromItems = `FROM com_nalet_katalog_items i LEFT JOIN com_nalet_katalog_items par ON par.id = i.parent_id`

// ageSQL is the age a viewer must be to be served the item i (fromItems):
// its own override, else its parent's override, else its parent's rating,
// else its own rating; NULL when nothing rates it.
const ageSQL = `COALESCE(i.min_age_override, par.min_age_override, par.min_age, i.min_age)`

// ownAgeSQL is ageSQL for an item without a parent, a film or a series:
// katalog-manager's idx_items_rated_age indexes it.
const ownAgeSQL = `COALESCE(i.min_age_override, i.min_age)`

// ratingSelect is what every item query reads of the item's rating, after its
// twelve columns (scanItem): the age (ageSQL), and the certification and its
// country that the age comes from: the parent's for an episode rated as its
// series, none when an override rates it. NULLs on a catalog without
// migration 036, or a role that may not read its columns.
func ratingSelect(rated bool) string {
	if !rated {
		return `, NULL::int, NULL::text, NULL::text`
	}
	from := func(col string) string {
		return `CASE WHEN i.min_age_override IS NOT NULL OR par.min_age_override IS NOT NULL THEN NULL
			WHEN par.min_age IS NOT NULL THEN par.` + col + ` ELSE i.` + col + ` END`
	}
	return `, ` + ageSQL + `::int, ` + from("certification") + `::text, ` + from("certification_country") + `::text`
}

// rated reports whether the catalog rates its titles: it has the columns of
// migration 036 and this role may read them.
func (s *Store) rated(ctx context.Context) (bool, error) {
	has, err := s.columns(ctx, "com_nalet_katalog_items", ratingColumns...)
	return all(has, ratingColumns...), err
}

var unratedCatalog sync.Once

// capFilter is the condition that keeps what the viewer capped at the age of
// ctx (WithMaxAge) may be served of the items i (fromItems), binding values
// with add: an item rated at most the cap, or one nothing rates when
// ratings.unrated_for_capped says show. "" for an uncapped viewer. top says
// the query lists titles without a parent alone (films, series): it then
// reads their own rating, which katalog-manager's index serves, and a title
// that has a parent after all is not served to a capped viewer. On a catalog
// that rates nothing (no migration 036, or a role that may not read its
// columns) a capped viewer is served nothing, and the service says so once.
func (s *Store) capFilter(ctx context.Context, top bool, add func(any) string) (string, error) {
	maxAge := maxAgeOf(ctx)
	if maxAge == nil {
		return "", nil
	}
	rated, err := s.rated(ctx)
	if err != nil {
		return "", err
	}
	if !rated {
		unratedCatalog.Do(func() {
			slog.Warn("the catalog rates no title (katalog-manager's migration 036, or SELECT on its columns, is " +
				"missing): a viewer with a rating cap is served nothing until it does")
		})
		return "FALSE", nil
	}
	age := ageSQL
	if top {
		age = ownAgeSQL
	}
	cond := age + " <= " + add(*maxAge) + "::int"
	if s.showUnrated(ctx) {
		cond = "(" + cond + " OR " + age + " IS NULL)"
	}
	if top {
		cond = "i.parent_id IS NULL AND " + cond
	}
	return cond, nil
}

// unratedSetting is the catalog's setting that says whether a viewer with a
// rating cap is served the titles nothing rates (show), or not (hide, the
// default).
const unratedSetting = "ratings.unrated_for_capped"

// unratedEvery is how long the setting is kept before it is read again.
const unratedEvery = 30 * time.Second

// unratedPolicy is the setting as last read.
type unratedPolicy struct {
	mu    sync.Mutex
	show  bool
	until time.Time
	said  bool // that it cannot be read
}

// showUnrated reads ratings.unrated_for_capped from the catalog's settings, at
// most every unratedEvery: true when it says show, whatever its case and
// spaces. Unset, anything else, or unreadable (a role that may not read the
// settings), it hides, and an unreadable one is said once.
func (s *Store) showUnrated(ctx context.Context) bool {
	s.unrated.mu.Lock()
	defer s.unrated.mu.Unlock()
	if time.Now().Before(s.unrated.until) {
		return s.unrated.show
	}
	var value string
	err := s.Pool.QueryRow(ctx, `SELECT valuetext FROM com_nalet_katalog_settings WHERE key = $1
		ORDER BY id LIMIT 1`, unratedSetting).Scan(&value)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		if ctx.Err() != nil {
			return false // the request went away: hide now, and read it again next time
		}
		if !s.unrated.said {
			s.unrated.said = true
			slog.Warn("the setting "+unratedSetting+" cannot be read: unrated titles are hidden from capped viewers", "err", err)
		}
		value = ""
	}
	s.unrated.show = strings.EqualFold(strings.TrimSpace(value), "show")
	s.unrated.until = time.Now().Add(unratedEvery)
	return s.unrated.show
}

// visible reports whether the item id is there for the viewer of ctx: the
// catalog holds it, and the viewer's cap, if any, allows it.
func (s *Store) visible(ctx context.Context, id string) (bool, error) {
	args := []any{id}
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	cond, err := s.capFilter(ctx, false, add)
	if err != nil {
		return false, err
	}
	q := `SELECT EXISTS (SELECT 1 ` + fromItems + ` WHERE i.id = $1`
	if cond != "" {
		q += ` AND ` + cond
	}
	var ok bool
	err = s.Pool.QueryRow(ctx, q+`)`, args...).Scan(&ok)
	return ok, err
}

// Visible is which of ids name a title the viewer of ctx may be served: in
// the order given, each once, the ids of no title left out as the ones the
// viewer's cap leaves out are.
func (s *Store) Visible(ctx context.Context, ids []string) ([]string, error) {
	if s == nil || s.Pool == nil {
		return nil, ErrNoPool
	}
	args := []any{ids}
	cond, err := s.capFilter(ctx, false, func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	})
	if err != nil {
		return nil, err
	}
	q := `SELECT i.id ` + fromItems + ` WHERE i.id = ANY($1::text[])`
	if cond != "" {
		q += ` AND ` + cond
	}
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("visible items: %w", err)
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("visible items: %w", err)
	}
	there := make(map[string]bool, len(found))
	for _, id := range found {
		there[id] = true
	}
	out := make([]string, 0, len(found))
	for _, id := range ids {
		if there[id] {
			out = append(out, id)
			delete(there, id)
		}
	}
	return out, nil
}
