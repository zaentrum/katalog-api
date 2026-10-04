package store

import "context"

// A title's age rating is katalog-manager's (its migration 036): the
// certification TMDB gives the title in a country, the minimum age it means
// (min_age) and an admin's override (min_age_override). An episode carries no
// certification of its own: it is rated as its series.

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
