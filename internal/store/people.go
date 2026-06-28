package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Person is a cast/crew member. Credits (their number of catalogue
// appearances) is filled by SearchPeople so the UI can show "Al Pacino ·
// 12 titles" and rank prolific names first.
type Person struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Credits int    `json:"credits,omitempty"`
}

// PersonDetail is a person plus their filmography — the items they're
// credited in, newest first.
type PersonDetail struct {
	Person
	Items []Item `json:"items"`
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
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT p.id, p.name, count(ip.id) AS credits
		FROM com_nalet_katalog_people p
		JOIN com_nalet_katalog_itempeople ip ON ip.person_id = p.id
		WHERE unaccent(p.name) ILIKE '%' || unaccent($1) || '%'
		GROUP BY p.id, p.name
		ORDER BY
			CASE WHEN lower(unaccent(p.name)) = lower(unaccent($1)) THEN 0
			     WHEN unaccent(p.name) ILIKE unaccent($1) || '%' THEN 1
			     ELSE 2 END,
			count(ip.id) DESC,
			p.name ASC
		LIMIT $2`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search people: %w", err)
	}
	defer rows.Close()
	out := []Person{}
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name, &p.Credits); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPerson returns a person and their filmography (distinct items the
// person is credited in, newest first, capped at limit). Returns
// (nil, nil) when the id doesn't exist so the handler can answer 404.
// Items carry the same core shape as the list endpoints, so clients
// render them with the existing poster-card components.
func (s *Store) GetPerson(ctx context.Context, id string, limit int) (*PersonDetail, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var pd PersonDetail
	err := s.Pool.QueryRow(ctx,
		`SELECT id, name FROM com_nalet_katalog_people WHERE id = $1`, id).
		Scan(&pd.ID, &pd.Name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get person: %w", err)
	}

	// DISTINCT collapses a person credited twice on one title (e.g. actor
	// + director) into a single filmography card.
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT i.id, i.type, i.title, i.sorttitle, i.year,
			i.rating, i.description, i.tagline, i.durationms,
			i.seasonnumber, i.episodenumber, i.parent_id
		FROM com_nalet_katalog_items i
		JOIN com_nalet_katalog_itempeople ip ON ip.item_id = i.id
		WHERE ip.person_id = $1
		ORDER BY i.year DESC NULLS LAST, i.rating DESC NULLS LAST, i.sorttitle ASC NULLS LAST
		LIMIT $2`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("person filmography: %w", err)
	}
	defer rows.Close()
	pd.Items = []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		pd.Items = append(pd.Items, it)
	}
	return &pd, rows.Err()
}
