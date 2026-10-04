package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Person is a cast/crew member. Credits (the number of titles they are
// credited on, however many roles they hold on each) is filled by
// SearchPeople so the UI can show "Al Pacino · 12 titles" and rank prolific
// names first. HasProfile says the catalog
// holds a portrait of them: katalog-manager serves it at
// /api/artwork/person/{id}/profile.
type Person struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Credits    int    `json:"credits,omitempty"`
	HasProfile bool   `json:"has_profile"`
}

// PersonDetail is a person, what the catalog knows about them (migration
// 030; every field is omitted when unknown), and their filmography — the
// items they're credited in, newest first, each with the person's roles on
// it.
type PersonDetail struct {
	Person
	SortName    string   `json:"sort_name,omitempty"`
	AlsoKnownAs []string `json:"also_known_as,omitempty"`
	// BirthDate and DeathDate are YYYY-MM-DD.
	BirthDate          string `json:"birth_date,omitempty"`
	DeathDate          string `json:"death_date,omitempty"`
	Birthplace         string `json:"birthplace,omitempty"`
	KnownForDepartment string `json:"known_for_department,omitempty"`
	// Biography is one text, in the language BiographyLang names (a
	// primary language subtag): the first of the request's languages the
	// catalog has it in, else English, else any.
	Biography     string `json:"biography,omitempty"`
	BiographyLang string `json:"biography_lang,omitempty"`
	TMDBPersonID  string `json:"tmdb_person_id,omitempty"`
	IMDbID        string `json:"imdb_id,omitempty"`
	Items         []Item `json:"items"`
}

// personColumns are the columns migration 030 adds to com_nalet_katalog_people
// that a person's details are read from.
var personColumns = []string{
	"sortname", "alsoknownas", "birthdate", "deathdate", "birthplace",
	"biography", "tmdbpersonid", "imdbid", "knownfordepartment",
}

// artworkColumns are the columns of com_nalet_katalog_personartwork (added by
// migration 030) that say whether a person has a portrait.
var artworkColumns = []string{"person_id", "kind", "isprimary"}

// hasProfile is the SQL that says whether the person p has a portrait: a
// primary profile image, the one katalog-manager serves. false on a catalog
// without the artwork table (or a role that may not read it).
func (s *Store) hasProfile(ctx context.Context) (string, error) {
	has, err := s.columns(ctx, "com_nalet_katalog_personartwork", artworkColumns...)
	if err != nil {
		return "", err
	}
	if !all(has, artworkColumns...) {
		return "false", nil
	}
	return `EXISTS (SELECT 1 FROM com_nalet_katalog_personartwork a
		WHERE a.person_id = p.id AND a.kind = 'profile' AND a.isprimary)`, nil
}

// SearchPeople returns people whose name matches q, accent- and
// case-insensitively (substring, via the unaccent extension so "francois"
// finds "François" and "pacino" finds "Al Pacino"). Ranked exact → name
// prefix → most-credited → alphabetical. limit clamps to [1,50].
//
// No trigram index yet, so this trigram/ILIKE scan walks the ~16k-row
// people table — fast enough today; a `gin (name gin_trgm_ops)` index on
// com_nalet_katalog_people (owned by katalog-manager-api) is the perf
// follow-up if name search becomes hot.
func (s *Store) SearchPeople(ctx context.Context, q string, limit int) ([]Person, error) {
	if s == nil || s.Pool == nil {
		return nil, ErrNoPool
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	profile, err := s.hasProfile(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT p.id, p.name, count(DISTINCT ip.item_id) AS credits, `+profile+`
		FROM com_nalet_katalog_people p
		JOIN com_nalet_katalog_itempeople ip ON ip.person_id = p.id
		WHERE unaccent(p.name) ILIKE '%' || unaccent($1) || '%'
		GROUP BY p.id, p.name
		ORDER BY
			CASE WHEN lower(unaccent(p.name)) = lower(unaccent($1)) THEN 0
			     WHEN unaccent(p.name) ILIKE unaccent($1) || '%' THEN 1
			     ELSE 2 END,
			count(DISTINCT ip.item_id) DESC,
			p.name ASC
		LIMIT $2`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search people: %w", err)
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name, &p.Credits, &p.HasProfile); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPerson returns a person, their details and their filmography (one card
// per item the person is credited in, newest first, capped at limit, each
// with the person's roles on it in creditRoles order). langs are the
// languages to pick the biography in, most wanted first (primary subtags).
// Returns (nil, nil) when the id doesn't exist so the handler can answer 404.
// Items carry the same core shape as the list endpoints, so clients render
// them with the existing poster-card components.
//
// On a catalog older than migration 030 a person is an id and a name: the
// details are left out and has_profile is false.
func (s *Store) GetPerson(ctx context.Context, id string, limit int, langs []string) (*PersonDetail, error) {
	if s == nil || s.Pool == nil {
		return nil, ErrNoPool
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	has, err := s.columns(ctx, "com_nalet_katalog_people", personColumns...)
	if err != nil {
		return nil, err
	}
	profile, err := s.hasProfile(ctx)
	if err != nil {
		return nil, err
	}
	text := func(col string) string {
		if has[col] {
			return "p." + col
		}
		return "NULL::text"
	}
	date := func(col string) string {
		if has[col] {
			return "to_char(p." + col + ", 'YYYY-MM-DD')"
		}
		return "NULL::text"
	}
	aka, bio := "NULL::text[]", "NULL::jsonb"
	if has["alsoknownas"] {
		// The names in their order, the empty ones dropped. 030 checks that
		// it is an array; the CASE keeps a value from before that check from
		// failing the query.
		aka = `CASE WHEN jsonb_typeof(p.alsoknownas) = 'array' THEN
			ARRAY(SELECT e.name FROM jsonb_array_elements_text(p.alsoknownas) WITH ORDINALITY AS e(name, n)
			      WHERE e.name <> '' ORDER BY e.n) END`
	}
	if has["biography"] {
		bio = "p.biography"
	}

	var (
		pd                                  PersonDetail
		sortName, birth, death, place, dept *string
		tmdbID, imdbID                      *string
		biographies                         []byte
	)
	err = s.Pool.QueryRow(ctx, `
		SELECT p.id, p.name, `+profile+`, `+text("sortname")+`, `+aka+`,
		       `+date("birthdate")+`, `+date("deathdate")+`, `+text("birthplace")+`,
		       `+text("knownfordepartment")+`, `+bio+`, `+text("tmdbpersonid")+`, `+text("imdbid")+`
		FROM com_nalet_katalog_people p WHERE p.id = $1`, id).
		Scan(&pd.ID, &pd.Name, &pd.HasProfile, &sortName, &pd.AlsoKnownAs,
			&birth, &death, &place, &dept, &biographies, &tmdbID, &imdbID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get person: %w", err)
	}
	pd.SortName, pd.Birthplace, pd.KnownForDepartment = deref(sortName), deref(place), deref(dept)
	pd.BirthDate, pd.DeathDate = deref(birth), deref(death)
	pd.TMDBPersonID, pd.IMDbID = deref(tmdbID), deref(imdbID)
	pd.Biography, pd.BiographyLang = pickBiography(biographies, langs)

	// One card per title, however many roles the person holds on it (a
	// person credited as actor and director is one card with both roles).
	rated, err := s.rated(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT `+itemColumns+ratingSelect(rated)+`, c.roles
		FROM (
			SELECT ip.item_id, array_agg(ip.role ORDER BY `+roleRank("$3", "ip.role")+`, ip.role) AS roles
			FROM (SELECT DISTINCT item_id, role::text AS role
			      FROM com_nalet_katalog_itempeople WHERE person_id = $1) ip
			GROUP BY ip.item_id
		) c
		JOIN com_nalet_katalog_items i ON i.id = c.item_id
		LEFT JOIN com_nalet_katalog_items par ON par.id = i.parent_id
		ORDER BY i.year DESC NULLS LAST, i.rating DESC NULLS LAST, i.sorttitle ASC NULLS LAST, i.id
		LIMIT $2`, id, limit, creditRoles)
	if err != nil {
		return nil, fmt.Errorf("person filmography: %w", err)
	}
	defer rows.Close()
	pd.Items = []Item{}
	for rows.Next() {
		var roles []string
		it, err := scanItem(rows, &roles)
		if err != nil {
			return nil, err
		}
		it.Roles = roles
		pd.Items = append(pd.Items, it)
	}
	return &pd, rows.Err()
}

// pickBiography picks one text out of a person's biographies (a JSON object of
// primary language subtag → text): the first of langs it has a text in, else
// English, else the first language with a text in alphabetical order. Returns
// the text and its language; empty when there is no text at all.
func pickBiography(raw []byte, langs []string) (text, lang string) {
	if len(raw) == 0 {
		return "", ""
	}
	var bios map[string]any
	if err := json.Unmarshal(raw, &bios); err != nil {
		return "", ""
	}
	keys := make([]string, 0, len(bios))
	for k := range bios {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { // by language, then as written: "de" and "DE" are one
		if li, lj := strings.ToLower(keys[i]), strings.ToLower(keys[j]); li != lj {
			return li < lj
		}
		return keys[i] < keys[j]
	})
	texts := make(map[string]string, len(keys))
	var have []string // the languages with a text, alphabetically
	for _, k := range keys {
		s, ok := bios[k].(string)
		l := strings.ToLower(k)
		if !ok || strings.TrimSpace(s) == "" || texts[l] != "" {
			continue
		}
		texts[l] = s
		have = append(have, l)
	}
	for _, l := range append(append([]string{}, langs...), "en") {
		if t := texts[l]; t != "" {
			return t, l
		}
	}
	if len(have) == 0 {
		return "", ""
	}
	return texts[have[0]], have[0]
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
