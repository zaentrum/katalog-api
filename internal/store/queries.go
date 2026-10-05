package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// =============================================================================
// List queries (Movies / Series / Episodes / Albums browse).
// =============================================================================

// listItems is the shared workhorse: type discriminator + optional FTS +
// genre filter + year/rating range + sort + pagination. Returns the page
// of items AND the total match count for the same filter, so the caller
// can render an accurate "x of N" footer.
//
// Filters are parameterized; nothing user-controlled hits the SQL string
// directly. The Postgres role on the pool is `cloud_katalog_ro` so even
// a missed-parameterisation bug couldn't mutate state. A capped viewer's
// page and total leave out what the cap does not allow (WithMaxAge).
func (s *Store) ListItems(ctx context.Context, opts ListOpts) (ListResult, error) {
	if s == nil || s.Pool == nil {
		return ListResult{}, ErrNoPool
	}
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}

	var (
		args   []any
		wheres []string
	)
	add := func(s any) string {
		args = append(args, s)
		return fmt.Sprintf("$%d", len(args))
	}

	if opts.Type != "" {
		wheres = append(wheres, "i.type = "+add(opts.Type))
	}
	if opts.Query != "" {
		// Postgres FTS via the `search_vector` tsvector column that
		// migration NN added on the items table. websearch_to_tsquery
		// is the user-friendly variant: "the dark knight" matches both
		// words, "+dark -knight" honours +/- prefixes.
		wheres = append(wheres, "i.search_vector @@ websearch_to_tsquery('simple', "+add(opts.Query)+")")
	}
	if opts.YearMin != nil {
		wheres = append(wheres, "i.year >= "+add(*opts.YearMin))
	}
	if opts.YearMax != nil {
		wheres = append(wheres, "i.year <= "+add(*opts.YearMax))
	}
	if opts.RatingMin != nil {
		wheres = append(wheres, "i.rating >= "+add(*opts.RatingMin))
	}
	if opts.Genre != "" {
		// EXISTS subquery instead of a JOIN keeps each item row unique
		// without needing SELECT DISTINCT (which collides with the
		// "newest" sort — Postgres rejects ORDER BY on a column outside
		// the DISTINCT projection). The planner picks idx_itemgenres
		// on the EXISTS, same cost as the previous join.
		wheres = append(wheres, `EXISTS (
			SELECT 1
			FROM com_nalet_katalog_itemgenres ig
			JOIN com_nalet_katalog_genres g ON g.id = ig.genre_id
			WHERE ig.item_id = i.id AND g.name = `+add(opts.Genre)+`
		)`)
	}

	// A capped viewer is served what its cap allows (capFilter): of films and
	// series their own rating, which an index serves, of the rest (an
	// episode) its series' too.
	capped, err := s.capFilter(ctx, opts.Type == "movie" || opts.Type == "series", add)
	if err != nil {
		return ListResult{}, err
	}
	if capped != "" {
		wheres = append(wheres, capped)
	}

	rated, err := s.rated(ctx)
	if err != nil {
		return ListResult{}, err
	}
	from := "FROM com_nalet_katalog_items i"
	if rated {
		from = fromItems
	}
	where := ""
	if len(wheres) > 0 {
		where = "WHERE " + strings.Join(wheres, " AND ")
	}

	// Total count — run before applying LIMIT/OFFSET so chino-web's
	// "1-50 of N" pager is accurate even when the page itself is short.
	countSQL := "SELECT COUNT(DISTINCT i.id) " + from + " " + where
	var total int
	if err := s.Pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return ListResult{}, fmt.Errorf("count items: %w", err)
	}

	// Sort. Defaulting to sorttitle keeps the page order stable across
	// requests (which year/rating ties would not, since they're ints
	// with a long tail of duplicates). createdat DESC is the "newest"
	// shelf — used by the "Recently added" rail on chino-web.
	orderBy := "i.sorttitle ASC NULLS LAST, i.id ASC"
	switch opts.Sort {
	case "rating":
		orderBy = "i.rating DESC NULLS LAST, i.sorttitle ASC, i.id ASC"
	case "year":
		orderBy = "i.year DESC NULLS LAST, i.sorttitle ASC, i.id ASC"
	case "title":
		// Same as default; named here for explicit-intent callers.
	case "newest":
		orderBy = "i.createdat DESC NULLS LAST, i.id ASC"
	}

	// Episode ordering needs season+episode; ordering by title is wrong
	// for shows like "Friends" where the title is the same for every row.
	if opts.Type == "episode" && (opts.Sort == "" || opts.Sort == "title") {
		orderBy = "i.parent_id, i.seasonnumber NULLS LAST, i.episodenumber NULLS LAST, i.id"
	}

	// Relevance ranking for search. When a query is present and the caller
	// didn't ask for an explicit sort, order by MATCH QUALITY instead of
	// alphabetically: exact title → title-prefix → FTS rank → sorttitle.
	// Previously search returned `sorttitle ASC` regardless of how well a
	// row matched, so an exact hit buried in the A–Z order never surfaced
	// (each client then re-ranked client-side over just the fetched page —
	// a band-aid that couldn't see better matches on later pages). Ranking
	// here fixes every client at once and is correct across the whole set.
	if opts.Query != "" && (opts.Sort == "" || opts.Sort == "title") {
		qExact := add(opts.Query)
		qPrefix := add(opts.Query + "%")
		qRank := add(opts.Query)
		orderBy = "CASE WHEN lower(i.title) = lower(" + qExact + ") THEN 0 " +
			"WHEN i.title ILIKE " + qPrefix + " THEN 1 ELSE 2 END, " +
			"ts_rank_cd(i.search_vector, websearch_to_tsquery('simple', " + qRank + ")) DESC, " +
			"i.sorttitle ASC NULLS LAST, i.id ASC"
	}

	// Genre filter is an EXISTS subquery (not a JOIN), so each item row
	// is already unique — no DISTINCT needed. This also lets ORDER BY
	// reference columns outside the SELECT list, which is what the
	// "newest" sort (`i.createdat`) requires.
	listSQL := `SELECT ` + itemColumns + ratingSelect(rated) + `
		` + from + " " + where + `
		ORDER BY ` + orderBy + `
		LIMIT ` + add(opts.Limit) + ` OFFSET ` + add(opts.Offset)

	rows, err := s.Pool.Query(ctx, listSQL, args...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list items: %w", err)
	}
	defer rows.Close()

	items := make([]Item, 0, opts.Limit)
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return ListResult{}, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, err
	}
	return ListResult{Items: items, Total: total, Limit: opts.Limit, Offset: opts.Offset}, nil
}

// =============================================================================
// Similarity ("More like this").
// =============================================================================

// ListSimilar returns the top-N most similar items to the given id.
// Score model (cheap V1, sufficient for chino-web's "More like this"
// row on the detail page — OpenProject #115):
//
//	+3 per shared genre
//	+5 per shared cast member (role='actor' on both sides)
//	filter: same `type` as the source (movies recommend movies,
//	        series recommend series — cross-type recs read as a bug)
//	filter: exclude the source item itself and any episodes (we only
//	        recommend top-level titles; the user lands on episodes
//	        from the parent series)
//	tiebreak: rating DESC NULLS LAST, then id for stability
//
// Watched-history exclusion is intentionally not applied here: the
// watched_history projection lives on chino-api per-user, so filtering
// would either need an N+1 fan-out or a cross-database join. chino-web
// stamps watched state per-card via the existing `watched_at` field on
// each item, so the row visually marks "already seen" without a
// server-side filter.
//
// Single round-trip via CTEs. Both genre + cast index scans are keyed
// by (genre_id) / (person_id, role) — selectivity is high enough that
// the planner picks index seeks on the catalogue we have today
// (~1000 items, ~5k genre rows, ~20k cast rows). If the catalogue
// grows past 100k items, revisit by materialising a similarity rollup.
//
// Returns an empty slice (not ErrNotFound) when the source item exists
// but has no genre/cast overlap with anything else. Returns
// ErrNotFound when the source id itself isn't in the items table, or the
// viewer's cap leaves it out; the cap leaves out what it does not allow of
// the similar titles too (WithMaxAge).
func (s *Store) ListSimilar(ctx context.Context, itemID string, limit int) ([]Item, error) {
	if s == nil || s.Pool == nil {
		return nil, ErrNoPool
	}
	if limit <= 0 || limit > 50 {
		limit = 12
	}

	// Confirm the source item exists + grab its type. Doing this first
	// turns "unknown id" into a clean 404 instead of "empty list".
	// A source the viewer's cap leaves out is not found either.
	srcArgs := []any{itemID}
	srcCap, err := s.capFilter(ctx, false, func(v any) string {
		srcArgs = append(srcArgs, v)
		return fmt.Sprintf("$%d", len(srcArgs))
	})
	if err != nil {
		return nil, err
	}
	srcSQL := `SELECT i.type ` + fromItems + ` WHERE i.id = $1`
	if srcCap != "" {
		srcSQL += ` AND ` + srcCap
	}
	var srcType string
	err = s.Pool.QueryRow(ctx, srcSQL, srcArgs...).Scan(&srcType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("similar source lookup: %w", err)
	}
	// We never recommend "more like this" from an episode — the UI
	// only mounts the row on movie/series detail pages. Belt-and-
	// braces: short-circuit here so the SQL below isn't asked for
	// episode-vs-episode scoring it doesn't support.
	if srcType == "episode" {
		return []Item{}, nil
	}

	rated, err := s.rated(ctx)
	if err != nil {
		return nil, err
	}
	args := []any{itemID, srcType, limit}
	capped, err := s.capFilter(ctx, false, func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	})
	if err != nil {
		return nil, err
	}
	if capped != "" {
		capped = " AND " + capped
	}
	similarSQL := `
		WITH src_genres AS (
			SELECT genre_id FROM com_nalet_katalog_itemgenres WHERE item_id = $1
		),
		src_people AS (
			SELECT person_id FROM com_nalet_katalog_itempeople
			WHERE item_id = $1 AND role = 'actor'
		),
		genre_scores AS (
			SELECT ig.item_id, COUNT(*) * 3 AS s
			FROM com_nalet_katalog_itemgenres ig
			WHERE ig.genre_id IN (SELECT genre_id FROM src_genres)
			  AND ig.item_id <> $1
			GROUP BY ig.item_id
		),
		people_scores AS (
			SELECT ip.item_id, COUNT(*) * 5 AS s
			FROM com_nalet_katalog_itempeople ip
			WHERE ip.person_id IN (SELECT person_id FROM src_people)
			  AND ip.role = 'actor'
			  AND ip.item_id <> $1
			GROUP BY ip.item_id
		)
		SELECT ` + itemColumns + ratingSelect(rated) + `
		FROM com_nalet_katalog_items i
		LEFT JOIN com_nalet_katalog_items par ON par.id = i.parent_id
		LEFT JOIN genre_scores g  ON g.item_id = i.id
		LEFT JOIN people_scores p ON p.item_id = i.id
		WHERE i.id <> $1
		  AND i.type = $2
		  AND i.type <> 'episode'
		  AND (g.s IS NOT NULL OR p.s IS NOT NULL)
		  AND (COALESCE(g.s, 0) + COALESCE(p.s, 0)) > 0` + capped + `
		ORDER BY (COALESCE(g.s, 0) + COALESCE(p.s, 0)) DESC,
		         i.rating DESC NULLS LAST,
		         i.id
		LIMIT $3`

	rows, err := s.Pool.Query(ctx, similarSQL, args...)
	if err != nil {
		return nil, fmt.Errorf("similar items: %w", err)
	}
	defer rows.Close()
	out := make([]Item, 0, limit)
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// =============================================================================
// Single-item lookup, with optional rich-association expansion.
// =============================================================================

// IncludeOpts is the parsed `?include=...` set. Each field maps 1:1 to
// a query CASE in GetItemWithIncludes. Nothing here is user-controlled
// at the SQL level — the field names are hard-coded.
type IncludeOpts struct {
	Genres    bool
	People    bool
	Subtitles bool
	Trailers  bool
	Extras    bool
	Segments  bool
}

// ParseInclude turns the comma-separated `?include=genres,people,...`
// query string into an IncludeOpts. Unknown tokens are silently dropped.
func ParseInclude(raw string) IncludeOpts {
	out := IncludeOpts{}
	for _, tok := range strings.Split(raw, ",") {
		switch strings.TrimSpace(strings.ToLower(tok)) {
		case "genres":
			out.Genres = true
		case "people", "cast":
			out.People = true
		case "subtitles":
			out.Subtitles = true
		case "trailers", "trailerlinks":
			out.Trailers = true
		case "extras":
			out.Extras = true
		case "segments":
			out.Segments = true
		}
	}
	return out
}

// GetItem returns a single item by id without any associations. Returns
// ErrNotFound when the id isn't in the catalogue, or the viewer's cap leaves
// it out (WithMaxAge).
func (s *Store) GetItem(ctx context.Context, id string) (Item, error) {
	if s == nil || s.Pool == nil {
		return Item{}, ErrNoPool
	}
	rated, err := s.rated(ctx)
	if err != nil {
		return Item{}, err
	}
	args := []any{id}
	capped, err := s.capFilter(ctx, false, func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	})
	if err != nil {
		return Item{}, err
	}
	q := `SELECT ` + itemColumns + ratingSelect(rated) + ` ` + fromItems + ` WHERE i.id = $1`
	if capped != "" {
		q += ` AND ` + capped
	}
	it, err := scanItem(s.Pool.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	return it, err
}

// GetItemWithIncludes loads an item plus the requested associations in
// one round-trip per association (cheap: each association is a single
// query keyed by item_id). Returns ErrNotFound when the id doesn't
// exist, or the viewer's cap leaves the item out: then none of its
// associations, its extras among them, is served either.
func (s *Store) GetItemWithIncludes(ctx context.Context, id string, inc IncludeOpts) (Item, error) {
	it, err := s.GetItem(ctx, id)
	if err != nil {
		return Item{}, err
	}
	if inc.Genres {
		gs, err := s.listGenresFor(ctx, id)
		if err != nil {
			return Item{}, err
		}
		it.Genres = gs
	}
	if inc.People {
		ps, err := s.listPeopleFor(ctx, id)
		if err != nil {
			return Item{}, err
		}
		it.Cast = ps
	}
	if inc.Subtitles {
		ss, err := s.listSubtitlesFor(ctx, id)
		if err != nil {
			return Item{}, err
		}
		it.Subtitles = ss
	}
	if inc.Trailers {
		ts, err := s.listTrailersFor(ctx, id)
		if err != nil {
			return Item{}, err
		}
		it.Trailers = ts
	}
	if inc.Extras {
		es, err := s.listExtrasFor(ctx, id, it.Type)
		if err != nil {
			return Item{}, err
		}
		it.Extras = es
	}
	if inc.Segments {
		seg, err := s.segmentSummaryFor(ctx, id)
		if err != nil {
			return Item{}, err
		}
		it.Segments = seg
	}
	return it, nil
}

// =============================================================================
// Association-list queries (one per kind, all keyed by item_id).
// =============================================================================

// ListGenres returns every distinct genre name in the catalogue,
// sorted alphabetically. Used by chino-web's browse filter chips.
func (s *Store) ListGenres(ctx context.Context) ([]string, error) {
	if s == nil || s.Pool == nil {
		return nil, ErrNoPool
	}
	rows, err := s.Pool.Query(ctx,
		`SELECT name FROM com_nalet_katalog_genres ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if n != "" {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// ListEpisodesBySeries returns every episode under the given series id,
// ordered by season then episode. No pagination — even prolific shows
// are <500 episodes and chino-web shows them in a flat list. Returns
// an empty slice (not ErrNotFound) when the series has no episodes
// yet, since the parent series can legitimately exist before any
// episode has been scanned. Returns ErrNotFound when the id names no
// title, or one the viewer's cap leaves out, whose episodes it leaves
// out too, as the episodes the cap does not allow (WithMaxAge).
func (s *Store) ListEpisodesBySeries(ctx context.Context, seriesID string) ([]Item, error) {
	if s == nil || s.Pool == nil {
		return nil, ErrNoPool
	}
	if ok, err := s.visible(ctx, seriesID); err != nil {
		return nil, fmt.Errorf("series %s: %w", seriesID, err)
	} else if !ok {
		return nil, ErrNotFound
	}
	rated, err := s.rated(ctx)
	if err != nil {
		return nil, err
	}
	args := []any{seriesID}
	capped, err := s.capFilter(ctx, false, func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	})
	if err != nil {
		return nil, err
	}
	if capped != "" {
		capped = " AND " + capped
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+itemColumns+ratingSelect(rated)+` `+fromItems+`
		WHERE i.type = 'episode' AND i.parent_id = $1`+capped+`
		ORDER BY i.seasonnumber NULLS LAST, i.episodenumber NULLS LAST, i.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("episodes for %s: %w", seriesID, err)
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ListSegments returns the raw MediaSegments rows for an item, ordered
// by start time. Used by the player to wire timeline markers and
// Skip-Intro / Skip-Credits buttons. Returns ErrNotFound when the id names
// no title, or one the viewer's cap leaves out (WithMaxAge).
func (s *Store) ListSegments(ctx context.Context, itemID string) ([]Segment, error) {
	if s == nil || s.Pool == nil {
		return nil, ErrNoPool
	}
	if ok, err := s.visible(ctx, itemID); err != nil {
		return nil, fmt.Errorf("item %s: %w", itemID, err)
	} else if !ok {
		return nil, ErrNotFound
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, kind, startms, endms,
		       COALESCE(source, ''), COALESCE(confidence, 0), COALESCE(label, '')
		FROM com_nalet_katalog_mediasegments
		WHERE item_id = $1
		ORDER BY startms`, itemID)
	if err != nil {
		return nil, fmt.Errorf("segments for %s: %w", itemID, err)
	}
	defer rows.Close()
	out := []Segment{}
	for rows.Next() {
		var s Segment
		if err := rows.Scan(&s.ID, &s.Kind, &s.StartMs, &s.EndMs,
			&s.Source, &s.Confidence, &s.Label); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// listGenresFor returns the genre names attached to one item. Two-table
// hop through itemgenres → genres; safe to call for items without any
// genres (returns empty).
func (s *Store) listGenresFor(ctx context.Context, itemID string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT g.name
		FROM com_nalet_katalog_itemgenres ig
		JOIN com_nalet_katalog_genres g ON g.id = ig.genre_id
		WHERE ig.item_id = $1
		ORDER BY g.name`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		if n != "" {
			out = append(out, n)
		}
	}
	return out, rows.Err()
}

// creditRoles are the well-known credit roles, in the order a title's credits
// are listed. A role is an open vocabulary token (^[a-z][a-z0-9-]{0,39}$):
// one not named here is listed after these, alphabetically.
var creditRoles = []string{
	"actor", "creator", "director", "writer",
	"producer", "composer", "cinematographer", "editor",
}

// A title's credits are capped per role, so a film that lists eighty actors
// neither blows up the payload nor crowds out its crew: the first actorCap
// actors and the first crewCap people of every other role, in billing order.
const (
	actorCap = 20
	crewCap  = 10
)

// creditColumns are the columns migration 032 adds to a credit
// (com_nalet_katalog_itempeople): the job ("Screenplay"), the character
// played, the billing order and, on a series, the number of episodes.
var creditColumns = []string{"job", "charactername", "ordinal", "episodecount"}

// roleRank is the SQL rank of a role, given the parameter that carries
// creditRoles: its position in the vocabulary, or one past the end for any
// other role (which then sort among themselves by name).
func roleRank(param, role string) string {
	return "COALESCE(array_position(" + param + "::text[], " + role + "::text), cardinality(" + param + "::text[]) + 1)"
}

// listPeopleFor returns a title's credits: by role in creditRoles order (other
// roles after them, alphabetically), within a role by billing order (credits
// without one last) and then name, at most actorCap actors and crewCap people
// in each other role. On a catalog older than migration 032 a credit is a
// person and a role: the fields 032 adds are left out, the order falls back to
// role and name, and the caps still hold.
func (s *Store) listPeopleFor(ctx context.Context, itemID string) ([]CastEntry, error) {
	has, err := s.columns(ctx, "com_nalet_katalog_itempeople", creditColumns...)
	if err != nil {
		return nil, err
	}
	col := func(name, typ string) string {
		if has[name] {
			return "ip." + name
		}
		return "NULL::" + typ
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT person_id, name, role, job, charactername, ordinal, episodecount
		FROM (
			SELECT p.id AS person_id, p.name, ip.role,
			       `+col("job", "text")+` AS job,
			       `+col("charactername", "text")+` AS charactername,
			       `+col("ordinal", "integer")+` AS ordinal,
			       `+col("episodecount", "integer")+` AS episodecount,
			       `+roleRank("$2", "ip.role")+` AS rank,
			       row_number() OVER (PARTITION BY ip.role
			           ORDER BY `+col("ordinal", "integer")+` ASC NULLS LAST, p.name, p.id) AS n
			FROM com_nalet_katalog_itempeople ip
			JOIN com_nalet_katalog_people p ON p.id = ip.person_id
			WHERE ip.item_id = $1
		) c
		WHERE n <= CASE WHEN role = 'actor' THEN $3::int ELSE $4::int END
		ORDER BY rank, role, ordinal ASC NULLS LAST, name, person_id`,
		itemID, creditRoles, actorCap, crewCap)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CastEntry{}
	for rows.Next() {
		var (
			c         CastEntry
			job, char *string
		)
		if err := rows.Scan(&c.PersonID, &c.Name, &c.Role, &job, &char, &c.Order, &c.EpisodeCount); err != nil {
			return nil, err
		}
		if job != nil {
			c.Job = *job
		}
		if char != nil {
			c.Character = *char
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// listSubtitlesFor returns one entry per SubtitleAssets row for the
// item. `default` defaults to false in SQL — no need to COALESCE.
func (s *Store) listSubtitlesFor(ctx context.Context, itemID string) ([]Subtitle, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id,
		       COALESCE(lang, ''), COALESCE(label, ''),
		       COALESCE(format, ''), COALESCE(isdefault, false)
		FROM com_nalet_katalog_subtitleassets
		WHERE item_id = $1
		ORDER BY isdefault DESC, lang, id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Subtitle{}
	for rows.Next() {
		var s Subtitle
		if err := rows.Scan(&s.ID, &s.Lang, &s.Label, &s.Format, &s.Default); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// listTrailersFor returns ItemTrailerLinks rows. Doesn't filter on
// downloadedAt — chino-web shows both embedded (YouTube) and
// already-downloaded local files; the player picks the cheapest source.
func (s *Store) listTrailersFor(ctx context.Context, itemID string) ([]Trailer, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT COALESCE(site, ''), COALESCE(externalid, ''),
		       url, COALESCE(title, '')
		FROM com_nalet_katalog_itemtrailerlinks
		WHERE item_id = $1
		ORDER BY publishedat DESC NULLS LAST, id`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Trailer{}
	for rows.Next() {
		var t Trailer
		if err := rows.Scan(&t.Site, &t.ExternalID, &t.URL, &t.Title); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// extraColumns are the columns of com_nalet_katalog_itemextras, the table
// katalog-manager's migration 038 creates, that an item's extras are read
// from: every column listExtrasFor names.
var extraColumns = []string{
	"id", "item_id", "kind", "title", "label", "language", "seasonnumber", "durationms",
	"sortorder", "hidden", "state", "packagedat", "removedat", "createdat",
}

// listExtrasFor returns the extras of the item that play, in the order a
// viewer sees them: by the order an admin gave them (those without one last),
// then as they were taken in, then by id. An extra plays once it is packaged,
// until it is removed, unless an admin hid it or its source went missing;
// while it is packaged anew, the package it had plays on (the packager swaps
// the new one in whole). An extra is titled by its label when it has one, else
// by its title. A series' extra that belongs to a season names it; an extra of
// any other type of item names none.
//
// On a catalog without migration 038, or with a role that may not read the
// table yet (one created after the read-only role was granted its tables), the
// item has no extras rather than failing; they are read as soon as the table
// is there and the role may read it, without a restart.
func (s *Store) listExtrasFor(ctx context.Context, itemID, itemType string) ([]Extra, error) {
	has, err := s.columns(ctx, "com_nalet_katalog_itemextras", extraColumns...)
	if err != nil {
		return nil, err
	}
	if !all(has, extraColumns...) {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, kind, COALESCE(NULLIF(btrim(label), ''), title),
		       COALESCE(language, ''), COALESCE(durationms, 0), seasonnumber
		FROM com_nalet_katalog_itemextras
		WHERE item_id = $1
		  AND packagedat IS NOT NULL AND removedat IS NULL AND NOT hidden AND state <> 'missing'
		ORDER BY sortorder NULLS LAST, createdat, id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("extras of %s: %w", itemID, err)
	}
	defer rows.Close()
	out := []Extra{}
	for rows.Next() {
		var (
			e      Extra
			season *int
		)
		if err := rows.Scan(&e.ID, &e.Kind, &e.Title, &e.Language, &e.DurationMs, &season); err != nil {
			return nil, err
		}
		if itemType == "series" {
			e.SeasonNumber = season
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// segmentSummaryFor produces the SegSummary chino-web uses to decide
// whether the player should expose Skip-Intro / Skip-Credits buttons.
// One COUNT + three EXISTS clauses, single round-trip.
func (s *Store) segmentSummaryFor(ctx context.Context, itemID string) (*SegSummary, error) {
	var sm SegSummary
	err := s.Pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COALESCE(BOOL_OR(kind = 'intro'),   false),
		       COALESCE(BOOL_OR(kind = 'credits'), false),
		       COALESCE(BOOL_OR(kind = 'recap'),   false)
		FROM com_nalet_katalog_mediasegments
		WHERE item_id = $1`, itemID).
		Scan(&sm.Count, &sm.HasIntro, &sm.HasCredits, &sm.HasRecap)
	if err != nil {
		return nil, err
	}
	// Return nil instead of an all-zero summary so the JSON omits the
	// `segments` field entirely on items with nothing to summarise.
	if sm.Count == 0 {
		return nil, nil
	}
	return &sm, nil
}

// =============================================================================
// Scan helpers.
// =============================================================================

// scanItem reads the 12-column item row and its rating into an Item, COALESCE-ing the
// nullable text columns so the JSON wire never carries `null` for a
// missing description / tagline / parent (it carries the field-omitted
// `omitempty` form instead). Year / rating / season / episode stay as
// pointers — null is meaningful there ("year unknown" vs "year=0").
//
// Works with both pgx.Row (single-row scan) and pgx.Rows (multi-row);
// the interface is the common Scan(dest ...any) error method.
type rowScanner interface {
	Scan(dest ...any) error
}

// itemColumns are the twelve columns of an item every item query selects
// first, of the item i, then its rating (ratingSelect).
const itemColumns = `i.id, i.type, i.title, i.sorttitle, i.year, i.rating, i.description, i.tagline,
	i.durationms, i.seasonnumber, i.episodenumber, i.parent_id`

// extra are destinations for columns a query selects after the twelve and the
// rating's three.
func scanItem(r rowScanner, extra ...any) (Item, error) {
	var (
		it            Item
		sortTitle     *string
		year          *int
		rating        *float64
		description   *string
		tagline       *string
		durationMs    *int64
		seasonNum     *int
		episodeNum    *int
		parentID      *string
		certification *string
		certCountry   *string
	)
	if err := r.Scan(append([]any{
		&it.ID, &it.Type, &it.Title,
		&sortTitle, &year, &rating,
		&description, &tagline, &durationMs,
		&seasonNum, &episodeNum, &parentID,
		&it.MinAge, &certification, &certCountry,
	}, extra...)...); err != nil {
		return Item{}, err
	}
	if certification != nil && certCountry != nil {
		it.Certification, it.CertificationCountry = *certification, *certCountry
	}
	if sortTitle != nil {
		it.SortTitle = *sortTitle
	}
	if year != nil {
		it.Year = year
	}
	if rating != nil {
		it.Rating = *rating
	}
	if description != nil {
		it.Description = *description
	}
	if tagline != nil {
		it.Tagline = *tagline
	}
	if durationMs != nil {
		it.DurationMs = *durationMs
	}
	if seasonNum != nil {
		it.SeasonNumber = seasonNum
	}
	if episodeNum != nil {
		it.EpisodeNumber = episodeNum
	}
	if parentID != nil {
		it.ParentID = *parentID
	}
	return it, nil
}
