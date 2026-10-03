package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

// castOf returns item's credits as "role:name", in the order listed.
func castOf(t *testing.T, st *Store, item string) ([]CastEntry, []string) {
	t.Helper()
	cast, err := st.listPeopleFor(context.Background(), item)
	if err != nil {
		t.Fatalf("cast of %s: %v", item, err)
	}
	names := make([]string, len(cast))
	for i, c := range cast {
		names[i] = c.Role + ":" + c.Name
	}
	return cast, names
}

// A title's credits are listed role by role in the vocabulary's order, other
// roles after them by name; within a role by billing order, unbilled credits
// last, ties and unbilled by name; at most 20 actors and 10 of any other role.
func TestCastIsListedByRoleAndBillingCappedPerRole(t *testing.T) {
	st, db := open(t)
	db.Migrate032(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7.5)

	var want []string
	// Twenty-five actors. Billed 0..15 are Z..K, so billing is not the
	// alphabet; I and J share 16 (I first, by name); A..G are unbilled and
	// follow by name. Only the first twenty are listed: A and B of the
	// unbilled.
	for k := 0; k < 16; k++ {
		name := fmt.Sprintf("Actor %c", 'Z'-k)
		credit032(t, db, "m1", name, "actor", billing{character: name + " part", order: ptr(k)})
		want = append(want, "actor:"+name)
	}
	credit032(t, db, "m1", "Actor J", "actor", billing{order: ptr(16)})
	credit032(t, db, "m1", "Actor I", "actor", billing{order: ptr(16)})
	want = append(want, "actor:Actor I", "actor:Actor J")
	for _, c := range "GFEDCBA" {
		credit032(t, db, "m1", fmt.Sprintf("Actor %c", c), "actor", billing{})
	}
	want = append(want, "actor:Actor A", "actor:Actor B")

	// The rest, entered in no particular order. Twelve unbilled directors,
	// ten of them listed by name; writers by billing, not name; roles outside
	// the vocabulary after it, alphabetically; a person may hold two roles.
	credit032(t, db, "m1", "Stunt A", "stunts", billing{})
	credit032(t, db, "m1", "Editor A", "editor", billing{})
	credit032(t, db, "m1", "Writer A", "writer", billing{job: "Novel", order: ptr(2)})
	credit032(t, db, "m1", "Writer B", "writer", billing{job: "Screenplay", order: ptr(1)})
	credit032(t, db, "m1", "Cinematographer A", "cinematographer", billing{job: "Director of Photography"})
	credit032(t, db, "m1", "Animator A", "animator", billing{})
	credit032(t, db, "m1", "Composer A", "composer", billing{job: "Original Music Composer"})
	credit032(t, db, "m1", "Producer A", "producer", billing{job: "Producer"})
	credit032(t, db, "m1", "Actor Z", "producer", billing{job: "Executive Producer"})
	credit032(t, db, "m1", "Effects A", "visual-effects", billing{})
	for _, c := range "LKJIHGFEDCBA" {
		credit032(t, db, "m1", fmt.Sprintf("Director %c", c), "director", billing{job: "Director"})
	}
	credit032(t, db, "m1", "Creator A", "creator", billing{})

	want = append(want, "creator:Creator A")
	for _, c := range "ABCDEFGHIJ" {
		want = append(want, fmt.Sprintf("director:Director %c", c))
	}
	want = append(want,
		"writer:Writer B", "writer:Writer A",
		"producer:Actor Z", "producer:Producer A",
		"composer:Composer A",
		"cinematographer:Cinematographer A",
		"editor:Editor A",
		"animator:Animator A", "stunts:Stunt A", "visual-effects:Effects A",
	)

	cast, got := castOf(t, st, "m1")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cast:\n got %q\nwant %q", got, want)
	}

	// What 032 adds comes with each credit; billing 0 is sent, not dropped.
	lead := CastEntry{PersonID: "actor-z", Name: "Actor Z", Role: "actor", Character: "Actor Z part", Order: ptr(0)}
	if !reflect.DeepEqual(cast[0], lead) {
		t.Errorf("first credit %+v, want %+v", cast[0], lead)
	}
	for _, c := range cast {
		if c.Role == "writer" && c.Name == "Writer B" && (c.Job != "Screenplay" || c.Order == nil || *c.Order != 1) {
			t.Errorf("Writer B: %+v, want job Screenplay, order 1", c)
		}
		if c.Role == "director" && c.Order != nil {
			t.Errorf("an unbilled director has order %d", *c.Order)
		}
	}
}

// A series credit carries how many episodes it covers.
func TestCastOfASeriesCarriesEpisodeCounts(t *testing.T) {
	st, db := open(t)
	db.Migrate032(t)
	addItem(t, db, "s1", "series", "A Show", 2010, 8)
	credit032(t, db, "s1", "Actor A", "actor", billing{character: "Lead", order: ptr(0), episodes: ptr(62)})
	credit032(t, db, "s1", "Creator A", "creator", billing{episodes: ptr(62)})
	cast, _ := castOf(t, st, "s1")
	if len(cast) != 2 || cast[0].EpisodeCount == nil || *cast[0].EpisodeCount != 62 ||
		cast[1].Role != "creator" || cast[1].EpisodeCount == nil || *cast[1].EpisodeCount != 62 {
		t.Fatalf("cast %+v, want the actor then the creator, each over 62 episodes", cast)
	}
}

// On a catalog older than 032 a credit is a person and a role: the cast is
// listed by role and name, capped per role, without what 032 adds — and the
// store reads that as soon as the migration has run, without a restart.
func TestCastOnACatalogOlderThan032(t *testing.T) {
	st, db := open(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7.5)
	var want []string
	for k := 0; k < 22; k++ { // Actor A..V, twenty listed
		name := fmt.Sprintf("Actor %c", 'A'+k)
		credit(t, db, "m1", name, "actor")
		if k < 20 {
			want = append(want, "actor:"+name)
		}
	}
	credit(t, db, "m1", "Stunt A", "stunts")
	credit(t, db, "m1", "Director B", "director")
	credit(t, db, "m1", "Animator A", "animator")
	credit(t, db, "m1", "Director A", "director")
	want = append(want, "director:Director A", "director:Director B", "animator:Animator A", "stunts:Stunt A")

	for i := 1; i <= 2; i++ {
		cast, got := castOf(t, st, "m1")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("before 032, call %d:\n got %q\nwant %q", i, got, want)
		}
		for _, c := range cast {
			if c.Job != "" || c.Character != "" || c.Order != nil || c.EpisodeCount != nil {
				t.Fatalf("before 032: %+v carries a field 032 adds", c)
			}
		}
		if n := lookups(st); n != i {
			t.Fatalf("before 032, call %d: %d lookups, want %d", i, n, i)
		}
	}

	// The migration runs while the service is up; the next request reads it.
	db.Migrate032(t)
	db.Exec(t, `UPDATE com_nalet_katalog_itempeople SET ordinal = 0, charactername = 'Lead' WHERE person_id = 'actor-v'`)
	cast, got := castOf(t, st, "m1")
	if got[0] != "actor:Actor V" || cast[0].Character != "Lead" || cast[0].Order == nil || *cast[0].Order != 0 {
		t.Fatalf("after 032: first credit %+v, want Actor V billed first as Lead", cast[0])
	}
	if got[19] != "actor:Actor S" || got[20] != "director:Director A" {
		t.Fatalf("after 032: %q, want Actor V then A..S, then the directors", got)
	}
	n := lookups(st)
	castOf(t, st, "m1")
	if lookups(st) != n {
		t.Fatalf("after 032: the columns were looked up again")
	}
}

// The read-only role may not read 032's columns yet (a column grant that
// predates them): the cast is served without them rather than failing.
func TestCastWhenTheRoleMayNotRead032(t *testing.T) {
	_, db := open(t)
	db.Migrate032(t)
	addItem(t, db, "m1", "movie", "A Film", 2001, 7.5)
	credit032(t, db, "m1", "Actor B", "actor", billing{order: ptr(0), character: "Lead"})
	credit032(t, db, "m1", "Actor A", "actor", billing{order: ptr(1)})
	role, pool := db.Role(t)
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_people TO `+role)
	db.Exec(t, `GRANT SELECT (id, item_id, person_id, role) ON com_nalet_katalog_itempeople TO `+role)
	st := &Store{Pool: pool}

	cast, got := castOf(t, st, "m1")
	if want := []string{"actor:Actor A", "actor:Actor B"}; !reflect.DeepEqual(got, want) || cast[0].Order != nil {
		t.Fatalf("without the grant: %q %+v, want %q by name, unbilled", got, cast, want)
	}
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_itempeople TO `+role)
	if cast, got = castOf(t, st, "m1"); got[0] != "actor:Actor B" || cast[0].Character != "Lead" {
		t.Fatalf("with the grant: %q %+v, want Actor B billed first", got, cast)
	}
}
