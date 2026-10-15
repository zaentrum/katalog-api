package store

import (
	"context"
	"errors"
	"fmt"
)

// The holdings say what the library holds, by the identities titles have
// outside it (externalids.go), for a service that thinks in those identities
// and holds no catalog ids (ADR-0012): of a series, season by season, each
// episode the catalog holds, whether it has a file and whether it plays; of a
// movie, the same; and every title of a type with its identities. They are the
// library's, not a viewer's: no rating cap nor any other filter leaves a title
// out of them, and a store read for them ignores the cap a context carries
// (WithMaxAge). They say what is held, never what anyone wants.
//
// An episode has a file when the catalog records one for the file it plays
// (fileSQL): the file it was taken in from, or its package. It plays when that
// file is packaged (packagedSQL), as the packaged ids say. A covered episode
// plays its holder's file (covers.go), so both are its holder's.

// Identity is an identity a title has outside the catalog: a source the
// catalog keeps ids in, and the title's id in it.
type Identity struct {
	Source string
	ID     string
}

func (id Identity) String() string { return id.Source + " " + id.ID }

// SeriesHoldings is what the library holds of a series: the series (its id,
// title, year and identities) and, season by season in their order, the
// specials (0) among them, each episode the catalog holds of it, in episode
// order. An episode the catalog holds without a season or an episode number
// has no place among them and is left out.
type SeriesHoldings struct {
	SeriesID    string            `json:"seriesId"`
	Title       string            `json:"title"`
	Year        *int              `json:"year"`
	ExternalIDs map[string]string `json:"externalIds"`
	Seasons     []SeasonHoldings  `json:"seasons"`
}

// SeasonHoldings is one season's episodes in a series' holdings.
type SeasonHoldings struct {
	Season   int              `json:"season"`
	Episodes []EpisodeHolding `json:"episodes"`
}

// EpisodeHolding is one episode the catalog holds of a series: its number, its
// item id, whether it has a file and whether it plays. CoveredBy is the holder
// whose file a covered episode plays, and its HasFile and Playable are the
// holder's; null for every other episode.
type EpisodeHolding struct {
	Episode   int     `json:"episode"`
	ItemID    string  `json:"itemId"`
	HasFile   bool    `json:"hasFile"`
	Playable  bool    `json:"playable"`
	CoveredBy *string `json:"coveredBy"`
}

// MovieHolding is what the library holds of a movie: the movie (its id,
// title, year and identities), whether it has a file and whether it plays.
type MovieHolding struct {
	ItemID      string            `json:"itemId"`
	Title       string            `json:"title"`
	Year        *int              `json:"year"`
	ExternalIDs map[string]string `json:"externalIds"`
	HasFile     bool              `json:"hasFile"`
	Playable    bool              `json:"playable"`
}

// HoldingsPage is one page of every title of a type the library holds, in id
// order: the page asked for, its size, and how many titles there are of the
// type.
type HoldingsPage struct {
	Items    []TitleHolding `json:"items"`
	Page     int            `json:"page"`
	PageSize int            `json:"pageSize"`
	Total    int            `json:"total"`
}

// TitleHolding is a title in a page of holdings: its id, title, year and
// identities ({} when the catalog keeps none). HeldEpisodes is, for a series,
// how many of its episodes have a file (HasFile in its holdings); omitted for a
// movie.
type TitleHolding struct {
	ItemID       string            `json:"itemId"`
	Title        string            `json:"title"`
	Year         *int              `json:"year"`
	ExternalIDs  map[string]string `json:"externalIds"`
	HeldEpisodes *int              `json:"heldEpisodes,omitempty"`
}

// ErrNoIdentities is returned by the holdings while this role may not read the
// items' identities: nothing can be found by its identity then, and saying
// that the library holds nothing would be wrong.
var ErrNoIdentities = errors.New("the catalog's identities cannot be read: this role may not SELECT on " + externalIDsTable)

// Disagreement is the error of identities that name different titles: A names
// one or more, B one or more, and none of them the same.
type Disagreement struct {
	Type string // series or movie
	A, B Identity
}

func (e *Disagreement) Error() string {
	return e.A.String() + " and " + e.B.String() + " name different " + plural(e.Type)
}

// PastTheLastPage is the error of a page of the holdings of a type past its
// last: Last is the last page at PageSize titles a page, of Total.
type PastTheLastPage struct {
	Type                        string
	Page, Last, PageSize, Total int
}

func (e *PastTheLastPage) Error() string {
	return fmt.Sprintf("page %d is past the last page, %d: %d %s at %d a page", e.Page, e.Last, e.Total, plural(e.Type),
		e.PageSize)
}

// plural is the type typ, series or movie, named for more titles than one.
func plural(typ string) string {
	if typ == "movie" {
		return "movies"
	}
	return typ
}

// SeriesHoldings returns what the library holds of the series that carries the
// identities ids; one is enough, and one the catalog keeps no series by does
// not keep the others from finding it. It is ErrNotFound when no series
// carries any of them, and a *Disagreement when two of them name different
// series.
//
// Should the catalog hold the series twice, more series than one carrying the
// identities, its holdings are those of all of them: the series is the first
// of them made, and its seasons hold the episodes of each.
//
// On a catalog without migration 045, or with a role that may not read its
// column, no episode is covered: each says what it holds itself.
func (s *Store) SeriesHoldings(ctx context.Context, ids []Identity) (SeriesHoldings, error) {
	if s == nil || s.Pool == nil {
		return SeriesHoldings{}, ErrNoPool
	}
	series, err := s.titlesBy(ctx, "series", ids)
	if err != nil {
		return SeriesHoldings{}, err
	}
	var h SeriesHoldings
	if err := s.Pool.QueryRow(ctx, `
		SELECT id, title, year FROM com_nalet_katalog_items WHERE id = ANY($1::text[])
		ORDER BY createdat NULLS LAST, id LIMIT 1`, series).Scan(&h.SeriesID, &h.Title, &h.Year); err != nil {
		return SeriesHoldings{}, fmt.Errorf("series %v: %w", series, err)
	}
	if h.ExternalIDs, err = s.identitiesOf(ctx, h.SeriesID); err != nil {
		return SeriesHoldings{}, err
	}

	covering, err := s.covering(ctx)
	if err != nil {
		return SeriesHoldings{}, err
	}
	coveredBy, of := "NULL::text", "e.id"
	if covering {
		coveredBy = "NULLIF(e.coveredby, '')"
		of = "COALESCE(" + coveredBy + ", e.id)"
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT e.seasonnumber, e.episodenumber, e.id, `+coveredBy+`, `+fileSQL(of)+`, `+packagedSQL(of)+`
		FROM com_nalet_katalog_items e
		WHERE e.type = 'episode' AND e.parent_id = ANY($1::text[])
		  AND e.seasonnumber IS NOT NULL AND e.episodenumber IS NOT NULL
		ORDER BY e.seasonnumber, e.episodenumber, e.id`, series)
	if err != nil {
		return SeriesHoldings{}, fmt.Errorf("episodes of %v: %w", series, err)
	}
	defer rows.Close()
	h.Seasons = []SeasonHoldings{}
	for rows.Next() {
		var (
			season int
			ep     EpisodeHolding
		)
		if err := rows.Scan(&season, &ep.Episode, &ep.ItemID, &ep.CoveredBy, &ep.HasFile, &ep.Playable); err != nil {
			return SeriesHoldings{}, err
		}
		if n := len(h.Seasons); n == 0 || h.Seasons[n-1].Season != season {
			h.Seasons = append(h.Seasons, SeasonHoldings{Season: season, Episodes: []EpisodeHolding{}})
		}
		last := &h.Seasons[len(h.Seasons)-1]
		last.Episodes = append(last.Episodes, ep)
	}
	if err := rows.Err(); err != nil {
		return SeriesHoldings{}, err
	}
	return h, nil
}

// MovieHolding returns what the library holds of the movie that carries the
// identities ids; one is enough, and one the catalog keeps no movie by does not
// keep the others from finding it. It is ErrNotFound when no movie carries any
// of them, and a *Disagreement when two of them name different movies.
//
// Should the catalog hold the movie more than once, the answer is the one that
// plays, else one with a file, else the first made.
func (s *Store) MovieHolding(ctx context.Context, ids []Identity) (MovieHolding, error) {
	if s == nil || s.Pool == nil {
		return MovieHolding{}, ErrNoPool
	}
	movies, err := s.titlesBy(ctx, "movie", ids)
	if err != nil {
		return MovieHolding{}, err
	}
	var m MovieHolding
	if err := s.Pool.QueryRow(ctx, `
		SELECT i.id, i.title, i.year, `+fileSQL("i.id")+` AS hasfile, `+packagedSQL("i.id")+` AS playable
		FROM com_nalet_katalog_items i
		WHERE i.id = ANY($1::text[])
		ORDER BY playable DESC, hasfile DESC, i.createdat NULLS LAST, i.id
		LIMIT 1`, movies).Scan(&m.ItemID, &m.Title, &m.Year, &m.HasFile, &m.Playable); err != nil {
		return MovieHolding{}, fmt.Errorf("movie %v: %w", movies, err)
	}
	if m.ExternalIDs, err = s.identitiesOf(ctx, m.ItemID); err != nil {
		return MovieHolding{}, err
	}
	return m, nil
}

// ListHoldings returns page (from 1) of every title of type typ, series or
// movie, at pageSize titles a page, in id order, which renaming a title does
// not change. Page 1 is there for a type of no title, empty; any later page
// past the last is a *PastTheLastPage. A series says how many of its episodes
// have a file (HeldEpisodes), counted as its holdings count them.
func (s *Store) ListHoldings(ctx context.Context, typ string, page, pageSize int) (HoldingsPage, error) {
	if s == nil || s.Pool == nil {
		return HoldingsPage{}, ErrNoPool
	}
	if (typ != "series" && typ != "movie") || page < 1 || pageSize < 1 {
		return HoldingsPage{}, fmt.Errorf("holdings of %q, page %d at %d a page: no such page", typ, page, pageSize)
	}
	if ok, err := s.identified(ctx); err != nil {
		return HoldingsPage{}, err
	} else if !ok {
		return HoldingsPage{}, ErrNoIdentities
	}
	p := HoldingsPage{Items: []TitleHolding{}, Page: page, PageSize: pageSize}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM com_nalet_katalog_items WHERE type = $1`, typ).
		Scan(&p.Total); err != nil {
		return HoldingsPage{}, fmt.Errorf("count the %s: %w", typ, err)
	}
	if last := max(1, (p.Total+pageSize-1)/pageSize); page > last {
		return HoldingsPage{}, &PastTheLastPage{Type: typ, Page: page, Last: last, PageSize: pageSize, Total: p.Total}
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, title, year FROM com_nalet_katalog_items WHERE type = $1
		ORDER BY id LIMIT $2 OFFSET $3`, typ, pageSize, (page-1)*pageSize)
	if err != nil {
		return HoldingsPage{}, fmt.Errorf("the %s, page %d: %w", typ, page, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var t TitleHolding
		if err := rows.Scan(&t.ItemID, &t.Title, &t.Year); err != nil {
			return HoldingsPage{}, err
		}
		p.Items = append(p.Items, t)
		ids = append(ids, t.ItemID)
	}
	if err := rows.Err(); err != nil {
		return HoldingsPage{}, err
	}
	byItem, err := s.externalIDsOf(ctx, ids)
	if err != nil {
		return HoldingsPage{}, err
	}
	for i := range p.Items {
		p.Items[i].ExternalIDs = orNone(byItem[p.Items[i].ItemID])
	}
	if typ == "series" && len(ids) > 0 {
		held, err := s.heldEpisodes(ctx, ids)
		if err != nil {
			return HoldingsPage{}, err
		}
		for i := range p.Items {
			n := held[p.Items[i].ItemID]
			p.Items[i].HeldEpisodes = &n
		}
	}
	return p, nil
}

// heldEpisodes returns how many of each of the series' episodes have a file,
// of those their holdings list, by series; a series none of whose episodes
// has one has no entry.
func (s *Store) heldEpisodes(ctx context.Context, series []string) (map[string]int, error) {
	covering, err := s.covering(ctx)
	if err != nil {
		return nil, err
	}
	of := "e.id"
	if covering {
		of = "COALESCE(NULLIF(e.coveredby, ''), e.id)"
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT e.parent_id, count(*)::int
		FROM com_nalet_katalog_items e
		WHERE e.type = 'episode' AND e.parent_id = ANY($1::text[])
		  AND e.seasonnumber IS NOT NULL AND e.episodenumber IS NOT NULL AND `+fileSQL(of)+`
		GROUP BY e.parent_id`, series)
	if err != nil {
		return nil, fmt.Errorf("held episodes of %d series: %w", len(series), err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var (
			id string
			n  int
		)
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// titlesBy returns the titles of type typ that carry any of the identities
// ids, in id order: ErrNoIdentities while this role may not read them,
// ErrNotFound when no title carries any of them, and a *Disagreement when two
// of them name titles and none of them the same.
func (s *Store) titlesBy(ctx context.Context, typ string, ids []Identity) ([]string, error) {
	if ok, err := s.identified(ctx); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNoIdentities
	}
	sources, values := make([]string, len(ids)), make([]string, len(ids))
	for i, id := range ids {
		sources[i], values[i] = id.Source, id.ID
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT x.source, x.externalid, i.id
		FROM `+externalIDsTable+` x JOIN com_nalet_katalog_items i ON i.id = x.item_id
		WHERE i.type = $1 AND (x.source, x.externalid) IN (SELECT * FROM unnest($2::text[], $3::text[]))
		ORDER BY i.id`, typ, sources, values)
	if err != nil {
		return nil, fmt.Errorf("the %s by %v: %w", typ, ids, err)
	}
	defer rows.Close()
	named := map[Identity]map[string]bool{}
	var titles []string
	seen := map[string]bool{}
	for rows.Next() {
		var (
			id    Identity
			title string
		)
		if err := rows.Scan(&id.Source, &id.ID, &title); err != nil {
			return nil, err
		}
		if named[id] == nil {
			named[id] = map[string]bool{}
		}
		named[id][title] = true
		if !seen[title] {
			seen[title] = true
			titles = append(titles, title)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(titles) == 0 {
		return nil, ErrNotFound
	}
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			if a, b := named[ids[i]], named[ids[j]]; len(a) > 0 && len(b) > 0 && !overlap(a, b) {
				return nil, &Disagreement{Type: typ, A: ids[i], B: ids[j]}
			}
		}
	}
	return titles, nil
}

// identitiesOf returns the identities of the title id ({} when the catalog
// keeps none).
func (s *Store) identitiesOf(ctx context.Context, id string) (map[string]string, error) {
	byItem, err := s.externalIDsOf(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	return orNone(byItem[id]), nil
}

// orNone is m, or an empty map for none, so that a title's identities are
// always an object on the wire.
func orNone(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// overlap reports whether a and b have a key in common.
func overlap(a, b map[string]bool) bool {
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}
