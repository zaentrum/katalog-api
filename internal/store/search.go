package store

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// A search ranks the titles that match a query by how they match it, best
// first, and the people whose names do, apart (katalog-manager's migration
// 049 holds what it reads). A title, a name and a plot are read as 049's
// functions normalize them: lower case, accents off, apostrophes dropped,
// every other mark a space, and with the marks inside a word dropped too, so
// that "widows bay", "widow's bay" and "Widow´s Bay" are one, "leon" finds
// "Léon" and "spiderman" "Spider-Man". The tiers of a title, best first:
//
//	exact        its title is the query
//	prefix       its title begins with the query, word by word: each word of the
//	             query begins the word of the title in its place ("widow b"
//	             begins "Widow's Bay"; search as you type)
//	words        every word of the query begins a word of its title, in any order
//	alternative  ... of a series' other name: the one the library files it under (046's scantitle)
//	(episode)    an episode whose title matches so (exact, prefix, words), after
//	             every film and series; it names its series
//	similar      its title is close to the query, a typo (pg_trgm): only while the
//	             tiers above fill no page; films and series first
//	plot         every word of the query begins a word of its plot: only when no
//	             title matched by the tiers above it, typos aside
//
// Within a tier a title is ranked by how close its title is to the query
// (trigram similarity; a typo by the word similarity it was found by, a plot
// by none), then by its rating, then by its title. A query shorter than
// three characters reads the beginning of titles alone (exact and prefix).
//
// Until 049 is in place (katalog-manager builds it in the background after
// it starts, and on a catalog it has not reached yet) a search matches as
// the list endpoints did before it (searchItemsAsBefore).

// SearchTypes are the types of title a search looks at: films, series and
// episodes.
var SearchTypes = []string{"movie", "series", "episode"}

// Search limits: how many titles a search answers by default and at most.
const (
	SearchLimit    = 30
	maxSearchLimit = 100
)

// minWordsQuery is how many characters a query needs before a search reads
// the words of titles; a shorter one reads their beginnings alone.
const minWordsQuery = 3

// minTypoWord is how long a word must be to be matched by a typo: a shorter
// one must begin a word of the title.
const minTypoWord = 4

// typoSimilarity is how close a word must be to a word of the title to be
// its typo (pg_trgm's strict word similarity, its default threshold).
const typoSimilarity = 0.5

// SearchOpts is a search: the query, the types of title it looks at (the
// ones of SearchTypes; all of them when none is named) and how many titles it
// answers (SearchLimit by default, at most maxSearchLimit).
type SearchOpts struct {
	Query string
	Types []string
	Limit int
}

// SearchResult is the titles a search found, best first, and how many it
// found: every title that matched by its title (exact, prefix, words,
// alternative, an episode's), and the typos and plots it ranked, at most a few
// times the limit each.
type SearchResult struct {
	Items []Item
	Total int
}

// SearchSeries is the series of an episode a search found: its id and title,
// and whether the series matched the query by its own title in the same
// search (so that a client can show the two as one).
type SearchSeries struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Matched bool   `json:"matched"`
}

// searchIndexes are the indexes of 049 the ranked search needs, valid;
// searchOther and searchAKA the two it reads when they are there.
var (
	searchIndexes = []string{
		"idx_items_search_words", "idx_items_search_prefix", "idx_items_search_trigrams", "idx_items_search_plot",
		"idx_people_search_words", "idx_people_search_prefix", "idx_people_search_trigrams",
	}
	searchOther = "idx_items_search_scantitle"
	searchAKA   = "idx_people_search_aka"
)

// searchFunctions are 049's functions the ranked search calls.
var searchFunctions = []string{
	"com_nalet_katalog_search_text(text)", "com_nalet_katalog_search_joined(text)",
	"com_nalet_katalog_search_words(text)", "com_nalet_katalog_search_plot(text)",
}

// searchEvery is how long a catalog without 049 is taken as it is before it
// is looked at again.
const searchEvery = 15 * time.Second

// searchCatalog is what the store has learnt of 049: once it is in place it
// stays so; until then it is looked at again every searchEvery.
type searchCatalog struct {
	mu         sync.Mutex
	ready      bool
	other, aka bool // a series' other name, a person's other names, are indexed
	until      time.Time
	said       bool
}

// searchReady reports whether 049 is in place: its functions, which this role
// may call, and its indexes, valid; other and aka whether the indexes of a
// series' other name and of a person's other names are too, with their
// columns, which this role may read.
func (s *Store) searchReady(ctx context.Context) (ready, other, aka bool, err error) {
	s.search.mu.Lock()
	defer s.search.mu.Unlock()
	if s.search.ready || time.Now().Before(s.search.until) {
		return s.search.ready, s.search.other, s.search.aka, nil
	}
	var callable bool
	if err := s.Pool.QueryRow(ctx, `SELECT bool_and(COALESCE(has_function_privilege(to_regprocedure(f), 'EXECUTE'), false))
		FROM unnest($1::text[]) AS f`, searchFunctions).Scan(&callable); err != nil {
		return false, false, false, fmt.Errorf("look up the search functions: %w", err)
	}
	rows, err := s.Pool.Query(ctx, `SELECT n.name FROM unnest($1::text[]) AS n(name)
		JOIN pg_index x ON x.indexrelid = to_regclass(n.name) WHERE x.indisvalid AND x.indisready`,
		append([]string{searchOther, searchAKA}, searchIndexes...))
	if err != nil {
		return false, false, false, fmt.Errorf("look up the search indexes: %w", err)
	}
	valid, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, false, false, fmt.Errorf("look up the search indexes: %w", err)
	}
	has := map[string]bool{}
	for _, name := range valid {
		has[name] = true
	}
	ready = callable && all(has, searchIndexes...)
	if ready {
		cols, err := s.columns(ctx, "com_nalet_katalog_items", "scantitle")
		if err != nil {
			return false, false, false, err
		}
		other = has[searchOther] && cols["scantitle"]
		if cols, err = s.columns(ctx, "com_nalet_katalog_people", "alsoknownas"); err != nil {
			return false, false, false, err
		}
		aka = has[searchAKA] && cols["alsoknownas"]
	} else if !s.search.said {
		s.search.said = true
		slog.Info("the catalog's search indexes (katalog-manager's migration 049) are not in place yet: " +
			"a search matches as before until they are")
	}
	s.search.ready, s.search.other, s.search.aka = ready, other, aka
	s.search.until = time.Now().Add(searchEvery)
	return ready, other, aka, nil
}

// searchQuery is a query as 049's functions read it.
type searchQuery struct {
	text, joined string   // com_nalet_katalog_search_text and _joined of the query
	tsquery      string   // every word of either begins a word: (a:* & b:*) | (ab:*)
	words        []string // the words of text
	longest      string   // its longest word of minTypoWord characters or more, a typo's; "" when none
	// begins is a regular expression of a text that begins with the query
	// word by word: each word of the query begins the word of the text in its
	// place ("widow b" begins "widows bay").
	begins string
}

// short reports whether the query is too short to read the words of titles
// by: a search then reads their beginnings alone.
func (q searchQuery) short() bool { return utf8.RuneCountInString(q.text) < minWordsQuery }

// normalize reads q as 049's functions do. Its tsquery holds the words as
// to_tsvector('simple') splits the normalized text, the way the indexes hold
// a title's, each a prefix; empty when q has no word.
func (s *Store) normalize(ctx context.Context, q string) (searchQuery, error) {
	var (
		out    searchQuery
		qt, qj *string
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT t, j,
		       (SELECT string_agg(quote_literal(l) || ':*', ' & ')
		        FROM unnest(tsvector_to_array(to_tsvector('pg_catalog.simple'::regconfig, t))) AS l),
		       (SELECT string_agg(quote_literal(l) || ':*', ' & ')
		        FROM unnest(tsvector_to_array(to_tsvector('pg_catalog.simple'::regconfig, j))) AS l)
		FROM (SELECT com_nalet_katalog_search_text($1) AS t, com_nalet_katalog_search_joined($1) AS j) x`, q).
		Scan(&out.text, &out.joined, &qt, &qj)
	if err != nil {
		return searchQuery{}, fmt.Errorf("normalize the query: %w", err)
	}
	switch {
	case qt == nil || qj == nil:
	case *qt == *qj:
		out.tsquery = *qt
	default:
		out.tsquery = "(" + *qt + ") | (" + *qj + ")"
	}
	out.words = strings.Fields(out.text)
	quoted := make([]string, len(out.words))
	for i, w := range out.words {
		quoted[i] = regexp.QuoteMeta(w)
	}
	out.begins = "^" + strings.Join(quoted, "[^ ]* ")
	for _, w := range out.words {
		if utf8.RuneCountInString(w) >= minTypoWord && utf8.RuneCountInString(w) > utf8.RuneCountInString(out.longest) {
			out.longest = w
		}
	}
	return out, nil
}

// searchTypes are the types of opts that a search looks at, in SearchTypes'
// order, each once; all of them when it names none of them.
func searchTypes(types []string) []string {
	var out []string
	for _, t := range SearchTypes {
		for _, want := range types {
			if strings.EqualFold(strings.TrimSpace(want), t) {
				out = append(out, t)
				break
			}
		}
	}
	if len(out) == 0 {
		return append([]string(nil), SearchTypes...)
	}
	return out
}

// searchLimit is the limit of opts, clamped.
func searchLimit(n int) int {
	if n <= 0 {
		return SearchLimit
	}
	return min(n, maxSearchLimit)
}

// SearchItems runs a search of the titles (the tiers above). A blank query
// finds nothing. A capped viewer is served what the cap allows, and its total
// counts nothing else (WithMaxAge).
func (s *Store) SearchItems(ctx context.Context, opts SearchOpts) (SearchResult, error) {
	return s.searchItems(ctx, opts, searchLimit(opts.Limit))
}

// searchItems is SearchItems answering at most limit titles.
func (s *Store) searchItems(ctx context.Context, opts SearchOpts, limit int) (SearchResult, error) {
	if s == nil || s.Pool == nil {
		return SearchResult{}, ErrNoPool
	}
	if strings.TrimSpace(opts.Query) == "" {
		return SearchResult{Items: []Item{}}, nil
	}
	ready, other, _, err := s.searchReady(ctx)
	if err != nil {
		return SearchResult{}, err
	}
	types := searchTypes(opts.Types)
	if !ready {
		return s.searchItemsAsBefore(ctx, opts.Query, types, limit)
	}
	q, err := s.normalize(ctx, opts.Query)
	if err != nil {
		return SearchResult{}, err
	}
	if q.tsquery == "" {
		return SearchResult{Items: []Item{}}, nil
	}

	args := []any{q.text, q.joined, q.tsquery, types, candidates(limit), limit, q.words, q.longest, q.begins}
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	top := true
	for _, t := range types {
		top = top && (t == "movie" || t == "series")
	}
	capped, err := s.capFilter(ctx, top, add)
	if err != nil {
		return SearchResult{}, err
	}
	from := `FROM com_nalet_katalog_items i`
	if capped != "" {
		from += ` LEFT JOIN com_nalet_katalog_items par ON par.id = i.parent_id`
		capped = ` AND ` + capped
	}

	// The candidates, each set of them under the cap: every word of the query
	// begins a word of the title (words), or of a series' other name (other);
	// how many titles matched so (found). Ranked are the shortest titles that
	// begin with the query, the shortest of the words and of the other names
	// (films and series before episodes, which rank after them): a few times
	// the limit, so that a short query that matches thousands of titles ranks
	// only the best of them.
	var sql strings.Builder
	// params names each parameter with its type, those the query's tiers
	// leave out too (a short query reads no word): it is never read.
	sql.WriteString(`WITH
		params AS (SELECT $1::text, $2::text, $3::tsquery, $4::text[], $5::int, $6::int, $7::text[], $8::text, $9::text),
		words AS MATERIALIZED (
			SELECT i.id, i.type, char_length(i.title) AS len ` + from + `
			WHERE i.type = ANY ($4::text[]) AND `)
	if q.short() {
		sql.WriteString(`false`) // a short query reads the beginnings of titles alone
	} else {
		sql.WriteString(`com_nalet_katalog_search_words(i.title) @@ $3::tsquery`)
	}
	sql.WriteString(capped + `),
		other AS MATERIALIZED (`)
	if other && !q.short() {
		sql.WriteString(`SELECT i.id, i.type, char_length(i.scantitle) AS len ` + from + `
			WHERE i.scantitle IS NOT NULL AND i.type = ANY ($4::text[])
			  AND com_nalet_katalog_search_words(i.scantitle) @@ $3::tsquery` + capped)
	} else {
		sql.WriteString(`SELECT NULL::varchar AS id, NULL::varchar AS type, 0 AS len WHERE false`)
	}
	sql.WriteString(`),
		starts AS MATERIALIZED (
			SELECT i.id, i.type, char_length(i.title) AS len ` + from + `
			WHERE i.type = ANY ($4::text[])
			  AND com_nalet_katalog_search_text(i.title) ~>=~ $1
			  AND com_nalet_katalog_search_text(i.title) ~<~ ($1 || chr(1114111))` + capped + `),
		found AS (SELECT count(*) AS n FROM (SELECT id FROM words UNION SELECT id FROM other UNION SELECT id FROM starts) f),
		pre AS (
			(SELECT id FROM starts ORDER BY type = 'episode', len, id LIMIT $5)
			UNION (SELECT id FROM words ORDER BY type = 'episode', len, id LIMIT $5)
			UNION (SELECT id FROM other ORDER BY type = 'episode', len, id LIMIT $5)),
		typos AS (`)
	if q.longest != "" && !q.short() {
		// Found by its longest word through the trigram index; every word of
		// the query begins a word of the title or is close to one of them.
		sql.WriteString(`SELECT i.id, i.type ` + from + `
			WHERE (SELECT n FROM found) < $6
			  AND $8 <<% com_nalet_katalog_search_text(i.title)
			  AND i.type = ANY ($4::text[])` + capped + `
			  AND NOT EXISTS (SELECT 1 FROM unnest($7::text[]) AS w(word)
			      WHERE NOT ((' ' || com_nalet_katalog_search_text(i.title)) LIKE '% ' || w.word || '%'
			                 OR (char_length(w.word) >= ` + fmt.Sprint(minTypoWord) + `
			                     AND strict_word_similarity(w.word, com_nalet_katalog_search_text(i.title)) >= ` +
			fmt.Sprint(typoSimilarity) + `)))
			  AND i.id NOT IN (SELECT id FROM words) AND i.id NOT IN (SELECT id FROM other)
			ORDER BY i.type = 'episode', strict_word_similarity($1, com_nalet_katalog_search_text(i.title)) DESC, i.id
			LIMIT $5`)
	} else {
		sql.WriteString(`SELECT NULL::varchar AS id, NULL::varchar AS type WHERE false`)
	}
	sql.WriteString(`),
		plot AS (`)
	if !q.short() {
		sql.WriteString(`SELECT i.id ` + from + `
			WHERE (SELECT n FROM found) = 0
			  AND com_nalet_katalog_search_plot(i.description) @@ $3::tsquery
			  AND i.type = ANY ($4::text[])` + capped + `
			  AND i.id NOT IN (SELECT id FROM typos)
			ORDER BY i.type = 'episode', i.rating DESC NULLS LAST, i.id
			LIMIT $5`)
	} else {
		sql.WriteString(`SELECT NULL::varchar AS id WHERE false`)
	}
	// Each candidate's tier: how its title matched (match), whether it is an
	// episode, and how close the title is. Its title is normalized once a row
	// (normed, matched are materialized: the planner would compute an
	// expression of a subquery once for each place it is named).
	sql.WriteString(`),
		cand AS (
			SELECT id, 0 AS via FROM pre
			UNION ALL SELECT id, 1 FROM typos
			UNION ALL SELECT id, 2 FROM plot),
		normed AS MATERIALIZED (
			SELECT c.id, c.via, i.type, i.rating, i.title,
			       com_nalet_katalog_search_text(i.title) AS t, com_nalet_katalog_search_joined(i.title) AS j
			FROM cand c JOIN com_nalet_katalog_items i ON i.id = c.id),
		matched AS MATERIALIZED (
			SELECT n.id, n.type, n.rating, n.title,
			       CASE WHEN n.via = 2 THEN 'plot' WHEN n.via = 1 THEN 'similar'
			            WHEN n.t = $1 OR n.j = $2 THEN 'exact'
			            WHEN n.t LIKE $1 || '%' OR n.j LIKE $2 || '%' OR n.t ~ $9 THEN 'prefix'
			            WHEN n.id IN (SELECT id FROM words) THEN 'words'
			            ELSE 'alternative' END AS match,
			       CASE WHEN n.via = 1 THEN strict_word_similarity($1, n.t)
			            WHEN n.via = 2 THEN 0 ELSE similarity(n.t, $1) END AS sim
			FROM normed n),
		ranked AS (
			SELECT id, match, row_number() OVER (ORDER BY
			         CASE WHEN match = 'plot' THEN 7 WHEN match = 'similar' THEN 6 WHEN type = 'episode' THEN 5
			              WHEN match = 'exact' THEN 1 WHEN match = 'prefix' THEN 2 WHEN match = 'words' THEN 3 ELSE 4 END,
			         CASE WHEN match IN ('plot', 'similar') THEN (type = 'episode')::int WHEN type <> 'episode' THEN 0
			              WHEN match = 'exact' THEN 1 WHEN match = 'prefix' THEN 2 ELSE 3 END,
			         sim DESC, rating DESC NULLS LAST, title, id) AS ord
			FROM matched ORDER BY ord LIMIT $6),
		hits AS (SELECT id FROM words UNION SELECT id FROM other UNION SELECT id FROM typos)`)

	total, items, err := s.searchPage(ctx, sql.String(), `(SELECT n FROM found) + (SELECT count(*) FROM typos) + (SELECT count(*) FROM plot)`, args)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Items: items, Total: total}, nil
}

// candidates is how many titles of each kind a search ranks for a page of
// limit: a few times the page.
func candidates(limit int) int { return max(3*limit, 60) }

// searchPage reads the page a search ranked: the query withCTEs ends with the
// CTEs ranked (id, match, ord: the page, in its order, of at least one title
// whenever the search found one) and hits (the ids that matched by their
// title, a series' matched by its own), total is the SQL of the search's
// total. Each title is read as every list reads it, with how it matched and,
// an episode, its series.
func (s *Store) searchPage(ctx context.Context, withCTEs, total string, args []any) (int, []Item, error) {
	cols, _, err := s.itemSelect(ctx)
	if err != nil {
		return 0, nil, err
	}
	// The series of an episode: its parent, or its parent's when the parent
	// is a season.
	sql := withCTEs + `
		SELECT ` + cols + `, r.match, ser.id, ser.title, ser.id IN (SELECT id FROM hits), (` + total + `)::int
		FROM ranked r JOIN com_nalet_katalog_items i ON i.id = r.id
		LEFT JOIN com_nalet_katalog_items par ON par.id = i.parent_id
		LEFT JOIN com_nalet_katalog_items ser ON i.type = 'episode' AND ser.type = 'series'
		     AND ser.id = CASE WHEN par.type = 'season' THEN par.parent_id ELSE par.id END
		ORDER BY r.ord`
	// Planned with the query's values, each time: what the indexes find of a
	// query depends on it, and a plan made without it reads the whole table.
	rows, err := s.Pool.Query(ctx, sql, append([]any{pgx.QueryExecModeCacheDescribe}, args...)...)
	if err != nil {
		return 0, nil, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()
	var (
		items = []Item{}
		n     int
	)
	for rows.Next() {
		var (
			match           string
			serID, serTitle *string
			serMatched      *bool
		)
		it, err := scanItem(rows, &match, &serID, &serTitle, &serMatched, &n)
		if err != nil {
			return 0, nil, err
		}
		it.Match = match
		if serID != nil {
			it.Series = &SearchSeries{ID: *serID, Title: deref(serTitle), Matched: serMatched != nil && *serMatched}
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("search: %w", err)
	}
	// A page of none carries no total: the search found nothing, as a page
	// holds a title whenever it found one.
	if err := s.withExternalIDs(ctx, items); err != nil {
		return 0, nil, err
	}
	return n, items, nil
}

// searchItemsAsBefore is a search on a catalog without 049: it matches as the
// list endpoints did before it, by the catalog's search_vector (a generated
// column of the title, sort title and description) where there is
// one, else by the title's text, and ranks the titles by the title being the
// query, beginning with it, then by the vector's rank and the sort title.
// Every title is a match of words then.
func (s *Store) searchItemsAsBefore(ctx context.Context, query string, types []string, limit int) (SearchResult, error) {
	args := []any{query, types, limit}
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	top := true
	for _, t := range types {
		top = top && (t == "movie" || t == "series")
	}
	capped, err := s.capFilter(ctx, top, add)
	if err != nil {
		return SearchResult{}, err
	}
	has, err := s.columns(ctx, "com_nalet_katalog_items", "search_vector")
	if err != nil {
		return SearchResult{}, err
	}
	match, rank := `i.title ILIKE '%' || $1 || '%'`, `0`
	if has["search_vector"] {
		match = `i.search_vector @@ websearch_to_tsquery('simple', $1)`
		rank = `ts_rank_cd(i.search_vector, websearch_to_tsquery('simple', $1))`
	}
	where := `WHERE i.type = ANY ($2::text[]) AND ` + match
	if capped != "" {
		where += ` AND ` + capped
	}
	sql := `WITH
		matched AS MATERIALIZED (SELECT i.id, i.title, i.sorttitle, ` + rank + ` AS rank,
			CASE WHEN lower(i.title) = lower($1) THEN 'exact' WHEN i.title ILIKE $1 || '%' THEN 'prefix' ELSE 'words' END AS match
			` + fromItems + ` ` + where + `),
		ranked AS (
			SELECT id, match, row_number() OVER (ORDER BY CASE match WHEN 'exact' THEN 0 WHEN 'prefix' THEN 1 ELSE 2 END,
			       rank DESC, sorttitle ASC NULLS LAST, id) AS ord
			FROM matched ORDER BY ord LIMIT $3),
		hits AS (SELECT id FROM matched)`
	total, items, err := s.searchPage(ctx, sql, `SELECT count(*) FROM matched`, args)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Items: items, Total: total}, nil
}

// SearchPeopleLimit is how many people a search answers by default;
// maxSearchPeople at most.
const (
	SearchPeopleLimit = 12
	maxSearchPeople   = 50
)

// SearchPeopleRanked runs a search of the people, apart from the titles, by
// their names as a search reads a title: the name is the query (exact),
// begins with it (prefix), every word of the query begins a word of it
// (words), of one of the other names the person is known by (alternative,
// 030's alsoknownas), and a name close to the query (similar, while those
// fill no page). Within a tier the people credited on more titles come
// first, then the closer names, then by name; a person credited on none is
// not found. A query shorter than three characters reads the beginning of
// names alone. A capped viewer's credits count the titles the cap allows,
// and a person credited on none of them is not found (WithMaxAge). Until 049
// is in place the people are found as GET /people finds them (SearchPeople).
func (s *Store) SearchPeopleRanked(ctx context.Context, query string, limit int) ([]Person, error) {
	if s == nil || s.Pool == nil {
		return nil, ErrNoPool
	}
	if limit <= 0 {
		limit = SearchPeopleLimit
	}
	limit = min(limit, maxSearchPeople)
	if strings.TrimSpace(query) == "" {
		return []Person{}, nil
	}
	ready, _, aka, err := s.searchReady(ctx)
	if err != nil {
		return nil, err
	}
	if !ready {
		return s.SearchPeople(ctx, query, limit)
	}
	q, err := s.normalize(ctx, query)
	if err != nil {
		return nil, err
	}
	if q.tsquery == "" {
		return []Person{}, nil
	}
	profile, err := s.hasProfile(ctx)
	if err != nil {
		return nil, err
	}
	args := []any{q.text, q.joined, q.tsquery, maxPeopleCandidates, limit, q.words, q.longest, q.begins}
	capped, err := s.capFilter(ctx, false, func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	})
	if err != nil {
		return nil, err
	}
	credits := `FROM cand c JOIN com_nalet_katalog_itempeople ip ON ip.person_id = c.id`
	if capped != "" {
		credits += ` JOIN com_nalet_katalog_items i ON i.id = ip.item_id
			LEFT JOIN com_nalet_katalog_items par ON par.id = i.parent_id WHERE ` + capped
	}

	var sql strings.Builder
	sql.WriteString(`WITH
		params AS (SELECT $1::text, $2::text, $3::tsquery, $4::int, $5::int, $6::text[], $7::text, $8::text),
		starts AS MATERIALIZED (SELECT p.id FROM com_nalet_katalog_people p
			WHERE com_nalet_katalog_search_text(p.name) ~>=~ $1
			  AND com_nalet_katalog_search_text(p.name) ~<~ ($1 || chr(1114111))
			ORDER BY char_length(p.name), p.id LIMIT $4),
		words AS MATERIALIZED (SELECT p.id FROM com_nalet_katalog_people p WHERE `)
	if q.short() {
		sql.WriteString(`false`)
	} else {
		sql.WriteString(`com_nalet_katalog_search_words(p.name) @@ $3::tsquery ORDER BY char_length(p.name), p.id LIMIT $4`)
	}
	sql.WriteString(`),
		aka AS MATERIALIZED (`)
	if aka && !q.short() {
		sql.WriteString(`SELECT p.id FROM com_nalet_katalog_people p
			WHERE p.alsoknownas IS NOT NULL AND com_nalet_katalog_search_words(p.alsoknownas::text) @@ $3::tsquery
			  AND p.id NOT IN (SELECT id FROM words) AND p.id NOT IN (SELECT id FROM starts) LIMIT $4`)
	} else {
		sql.WriteString(`SELECT NULL::varchar AS id WHERE false`)
	}
	sql.WriteString(`),
		typos AS (`)
	if q.longest != "" && !q.short() {
		sql.WriteString(`SELECT p.id FROM com_nalet_katalog_people p
			WHERE (SELECT count(*) FROM (SELECT id FROM starts UNION SELECT id FROM words UNION SELECT id FROM aka) f) < $5
			  AND $7 <<% com_nalet_katalog_search_text(p.name)
			  AND NOT EXISTS (SELECT 1 FROM unnest($6::text[]) AS w(word)
			      WHERE NOT ((' ' || com_nalet_katalog_search_text(p.name)) LIKE '% ' || w.word || '%'
			                 OR (char_length(w.word) >= ` + fmt.Sprint(minTypoWord) + `
			                     AND strict_word_similarity(w.word, com_nalet_katalog_search_text(p.name)) >= ` +
			fmt.Sprint(typoSimilarity) + `)))
			  AND p.id NOT IN (SELECT id FROM starts) AND p.id NOT IN (SELECT id FROM words) AND p.id NOT IN (SELECT id FROM aka)
			ORDER BY strict_word_similarity($1, com_nalet_katalog_search_text(p.name)) DESC, p.id LIMIT $4`)
	} else {
		sql.WriteString(`SELECT NULL::varchar AS id WHERE false`)
	}
	sql.WriteString(`),
		cand AS (
			SELECT id, 0 AS via FROM starts UNION SELECT id, 0 FROM words
			UNION SELECT id, 1 FROM aka UNION SELECT id, 2 FROM typos),
		credited AS (
			SELECT c.id, min(c.via) AS via, count(DISTINCT ip.item_id) AS credits ` + credits + `
			GROUP BY c.id),
		normed AS MATERIALIZED (
			SELECT c.id, c.via, c.credits, p.name,
			       com_nalet_katalog_search_text(p.name) AS t, com_nalet_katalog_search_joined(p.name) AS j
			FROM credited c JOIN com_nalet_katalog_people p ON p.id = c.id),
		tiered AS MATERIALIZED (
			SELECT n.id, n.name, n.credits,
			       CASE WHEN n.via = 2 THEN 'similar' WHEN n.via = 1 THEN 'alternative'
			            WHEN n.t = $1 OR n.j = $2 THEN 'exact'
			            WHEN n.t LIKE $1 || '%' OR n.j LIKE $2 || '%' OR n.t ~ $8 THEN 'prefix'
			            ELSE 'words' END AS match,
			       CASE WHEN n.via = 2 THEN strict_word_similarity($1, n.t) ELSE similarity(n.t, $1) END AS sim
			FROM normed n)
		SELECT t.id, t.name, t.credits::int, ` + strings.ReplaceAll(profile, "p.id", "t.id") + `, t.match
		FROM tiered t
		ORDER BY CASE t.match WHEN 'exact' THEN 1 WHEN 'prefix' THEN 2 WHEN 'words' THEN 3 WHEN 'alternative' THEN 4 ELSE 5 END,
		         t.credits DESC, t.sim DESC, t.name, t.id
		LIMIT $5`)
	rows, err := s.Pool.Query(ctx, sql.String(), append([]any{pgx.QueryExecModeCacheDescribe}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("search people: %w", err)
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name, &p.Credits, &p.HasProfile, &p.Match); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search people: %w", err)
	}
	return out, nil
}

// maxPeopleCandidates bounds how many people of each kind a search ranks: a
// person is ranked by their credits, which are counted for every candidate.
const maxPeopleCandidates = 2000
