package store

import (
	"context"
	"strings"
	"testing"

	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// open returns a store on a fresh test schema (see storetest), with no
// migration after the base schema applied.
func open(t *testing.T) (*Store, *storetest.DB) {
	t.Helper()
	db := storetest.Open(t)
	return &Store{Pool: db.Pool}, db
}

// addItem inserts a title.
func addItem(t *testing.T, db *storetest.DB, id, typ, title string, year int, rating float64) {
	t.Helper()
	db.Exec(t, `INSERT INTO com_nalet_katalog_items (id, type, title, sorttitle, year, rating)
		VALUES ($1, $2, $3, $3, $4, $5)`, id, typ, title, year, rating)
}

// personID is the id the fixtures give the person called name.
func personID(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, " ", "-"))
}

// credit credits the person called name (created when new) in role on item,
// and returns the credit's id.
func credit(t *testing.T, db *storetest.DB, item, name, role string) string {
	t.Helper()
	db.Exec(t, `INSERT INTO com_nalet_katalog_people (id, name) VALUES ($1, $2)
		ON CONFLICT (id) DO NOTHING`, personID(name), name)
	var id string
	if err := db.Pool.QueryRow(context.Background(), `INSERT INTO com_nalet_katalog_itempeople (id, item_id, person_id, role)
		VALUES (gen_random_uuid()::varchar, $1, $2, $3) RETURNING id`, item, personID(name), role).Scan(&id); err != nil {
		t.Fatalf("credit %s as %s on %s: %v", name, role, item, err)
	}
	return id
}

// billing is what migration 032 adds to a credit; the zero value is none of it.
type billing struct {
	job, character string
	order          *int
	episodes       *int
}

// credit032 is credit on a catalog with migration 032, setting what it adds.
func credit032(t *testing.T, db *storetest.DB, item, name, role string, b billing) {
	t.Helper()
	id := credit(t, db, item, name, role)
	db.Exec(t, `UPDATE com_nalet_katalog_itempeople
		SET job = NULLIF($2, ''), charactername = NULLIF($3, ''), ordinal = $4, episodecount = $5
		WHERE id = $1`, id, b.job, b.character, b.order, b.episodes)
}

func ptr(n int) *int { return &n }

// lookups is how many times st has looked the catalog's columns up.
func lookups(st *Store) int {
	st.schema.mu.Lock()
	defer st.schema.mu.Unlock()
	return st.schema.lookups
}
