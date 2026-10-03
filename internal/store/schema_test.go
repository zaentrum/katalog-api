package store

import (
	"context"
	"reflect"
	"testing"
)

// A column is read once the catalog has it: one not there yet is looked for
// again on every call, one found is remembered.
func TestColumnsAreFoundOnceTheMigrationHasRun(t *testing.T) {
	st, db := open(t)
	ctx := context.Background()
	const table = "com_nalet_katalog_itempeople"

	for i := 1; i <= 2; i++ {
		has, err := st.columns(ctx, table, "role", "ordinal")
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string]bool{"role": true}; !reflect.DeepEqual(has, want) {
			t.Fatalf("before 032, call %d: %v, want %v", i, has, want)
		}
		if n := lookups(st); n != i {
			t.Fatalf("before 032, call %d: %d lookups, want %d (a missing column is looked for again)", i, n, i)
		}
	}

	db.Migrate032(t)
	for i := 1; i <= 2; i++ {
		has, err := st.columns(ctx, table, "role", "ordinal")
		if err != nil {
			t.Fatal(err)
		}
		if want := map[string]bool{"role": true, "ordinal": true}; !reflect.DeepEqual(has, want) {
			t.Fatalf("after 032, call %d: %v, want %v", i, has, want)
		}
	}
	if n := lookups(st); n != 3 {
		t.Fatalf("after 032: %d lookups, want 3 (a found column is remembered)", n)
	}
}

// A table the catalog does not have yet has no columns, and is no error.
func TestColumnsOfATableNotThereYet(t *testing.T) {
	st, db := open(t)
	ctx := context.Background()
	art := []string{"person_id", "kind", "isprimary"}
	has, err := st.columns(ctx, "com_nalet_katalog_personartwork", art...)
	if err != nil {
		t.Fatal(err)
	}
	if len(has) != 0 {
		t.Fatalf("before 030: %v, want none", has)
	}
	db.Migrate030(t)
	if has, err = st.columns(ctx, "com_nalet_katalog_personartwork", art...); err != nil {
		t.Fatal(err)
	}
	if !all(has, art...) {
		t.Fatalf("after 030: %v, want all of %v", has, art)
	}
}

// A column the role may not read counts as not there: the read-only role is
// granted its tables, and a migration's new table is not among them until
// someone grants it.
func TestColumnsTheRoleMayNotRead(t *testing.T) {
	_, db := open(t)
	db.Migrate030(t)
	db.Migrate032(t)
	role, pool := db.Role(t)
	st := &Store{Pool: pool}
	ctx := context.Background()
	added := []string{"job", "charactername", "ordinal", "episodecount"}
	art := []string{"person_id", "kind", "isprimary"}

	db.Exec(t, `GRANT SELECT (id, item_id, person_id, role) ON com_nalet_katalog_itempeople TO `+role)
	has, err := st.columns(ctx, "com_nalet_katalog_itempeople", append([]string{"role"}, added...)...)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]bool{"role": true}; !reflect.DeepEqual(has, want) {
		t.Fatalf("granted the base columns: %v, want %v", has, want)
	}
	if has, err = st.columns(ctx, "com_nalet_katalog_personartwork", art...); err != nil || len(has) != 0 {
		t.Fatalf("not granted the table: %v, %v; want none", has, err)
	}

	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_itempeople, com_nalet_katalog_personartwork TO `+role)
	if has, err = st.columns(ctx, "com_nalet_katalog_itempeople", added...); err != nil || !all(has, added...) {
		t.Fatalf("granted the table: %v, %v; want all of %v", has, err, added)
	}
	if has, err = st.columns(ctx, "com_nalet_katalog_personartwork", art...); err != nil || !all(has, art...) {
		t.Fatalf("granted the table: %v, %v; want all of %v", has, err, art)
	}
}
