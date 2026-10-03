package store

import (
	"context"
	"reflect"
	"testing"
)

// "More like this" scores +3 per shared genre and +5 per shared actor; a
// shared director, or an actor of the source credited otherwise, counts
// nothing. The credits' new columns change none of it.
func TestSimilarScoresSharedGenresAndActors(t *testing.T) {
	st, db := open(t)
	db.Migrate032(t)
	db.Exec(t, `INSERT INTO com_nalet_katalog_genres (id, name) VALUES ('g1', 'Drama'), ('g2', 'Comedy')`)
	genre := func(item, g string) {
		db.Exec(t, `INSERT INTO com_nalet_katalog_itemgenres (id, item_id, genre_id)
			VALUES (gen_random_uuid()::varchar, $1, $2)`, item, g)
	}
	addItem(t, db, "src", "movie", "Source", 2000, 7)
	genre("src", "g1")
	credit032(t, db, "src", "Actor A", "actor", billing{order: ptr(0)})
	credit032(t, db, "src", "Actor B", "actor", billing{order: ptr(1)})
	credit032(t, db, "src", "Director A", "director", billing{})

	addItem(t, db, "two-actors", "movie", "Two Actors", 2001, 1) // 10
	credit(t, db, "two-actors", "Actor A", "actor")
	credit(t, db, "two-actors", "Actor B", "actor")
	addItem(t, db, "one-actor", "movie", "One Actor", 2002, 5) // 5
	credit(t, db, "one-actor", "Actor B", "actor")
	addItem(t, db, "one-genre", "movie", "One Genre", 2003, 9) // 3: below one actor despite its rating
	genre("one-genre", "g1")
	addItem(t, db, "director", "movie", "Same Director", 2004, 9) // 0
	credit(t, db, "director", "Director A", "director")
	addItem(t, db, "directing-actor", "movie", "Actor Directs", 2005, 9) // 0
	credit(t, db, "directing-actor", "Actor A", "director")
	addItem(t, db, "series", "series", "A Series", 2006, 9) // another type
	credit(t, db, "series", "Actor A", "actor")
	genre("series", "g2")

	items, err := st.ListSimilar(context.Background(), "src", 10)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.ID)
	}
	if want := []string{"two-actors", "one-actor", "one-genre"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("similar to src: %q, want %q", got, want)
	}
}
