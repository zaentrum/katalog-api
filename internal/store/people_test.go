package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/zaentrum/katalog-api/internal/store/storetest"
)

// portrait gives person a profile image, the primary one or not.
func portrait(t *testing.T, db *storetest.DB, person string, primary bool) {
	t.Helper()
	db.Exec(t, `INSERT INTO com_nalet_katalog_personartwork (id, person_id, kind, contenttype, bytes, sha256, isprimary)
		VALUES (gen_random_uuid()::varchar, $1, 'profile', 'image/jpeg', '\xffd8ffe0'::bytea, repeat('a', 64), $2)`,
		person, primary)
}

// filmography is a person's items as "id:role,role".
func filmography(pd *PersonDetail) []string {
	var out []string
	for _, it := range pd.Items {
		s := it.ID + ":"
		for i, r := range it.Roles {
			if i > 0 {
				s += ","
			}
			s += r
		}
		out = append(out, s)
	}
	return out
}

// adaExample credits Ada on three titles, in seven roles, newest title last
// entered; Bob is on a fourth.
func adaExample(t *testing.T, db *storetest.DB) {
	t.Helper()
	addItem(t, db, "m-old", "movie", "Old Film", 1999, 6)
	addItem(t, db, "m-mid", "movie", "Mid Film", 2001, 7)
	addItem(t, db, "s-new", "series", "New Show", 2010, 8)
	addItem(t, db, "m-other", "movie", "Not Hers", 2020, 9)
	credit(t, db, "m-mid", "Ada Example", "composer")
	credit(t, db, "m-mid", "Ada Example", "animator")
	credit(t, db, "m-mid", "Ada Example", "writer")
	credit(t, db, "m-mid", "Ada Example", "actor")
	credit(t, db, "s-new", "Ada Example", "creator")
	credit(t, db, "s-new", "Ada Example", "actor")
	credit(t, db, "m-old", "Ada Example", "director")
	credit(t, db, "m-other", "Bob Other", "actor")
}

// A person carries what 030 holds about them, the biography in the language
// asked for, whether they have a portrait, and one card per title they are
// credited on, newest first, with their roles on it in credit-list order.
func TestPersonCarriesDetailsAndRolesPerTitle(t *testing.T) {
	st, db := open(t)
	db.Migrate030(t)
	db.Migrate032(t)
	adaExample(t, db)
	db.Exec(t, `UPDATE com_nalet_katalog_people SET
		sortname = 'Example, Ada', alsoknownas = '["Ada E.", "", "A. Example"]',
		birthdate = '1950-03-01', deathdate = '2020-11-30', birthplace = 'Bern, Switzerland',
		biography = '{"en": "English text.", "de": "Deutscher Text.", "fr": "  "}',
		tmdbpersonid = '12345', imdbid = 'nm0000123', knownfordepartment = 'Writing'
		WHERE id = 'ada-example'`)
	portrait(t, db, "ada-example", true)
	ctx := context.Background()

	pd, err := st.GetPerson(ctx, "ada-example", 0, []string{"de"})
	if err != nil || pd == nil {
		t.Fatalf("get person: %v, %v", pd, err)
	}
	got := *pd
	got.Items = nil
	want := PersonDetail{
		Person:   Person{ID: "ada-example", Name: "Ada Example", HasProfile: true},
		SortName: "Example, Ada", AlsoKnownAs: []string{"Ada E.", "A. Example"},
		BirthDate: "1950-03-01", DeathDate: "2020-11-30", Birthplace: "Bern, Switzerland",
		KnownForDepartment: "Writing", Biography: "Deutscher Text.", BiographyLang: "de",
		TMDBPersonID: "12345", IMDbID: "nm0000123",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("person:\n got %+v\nwant %+v", got, want)
	}
	// Roles in credit-list order, which is not the alphabet: writer before
	// composer, the unlisted animator last.
	if f, want := filmography(pd), []string{"s-new:actor,creator", "m-mid:actor,writer,composer,animator", "m-old:director"}; !reflect.DeepEqual(f, want) {
		t.Errorf("filmography %q, want %q", f, want)
	}
	if pd.Items[0].Title != "New Show" || pd.Items[0].Type != "series" || pd.Items[0].Year == nil || *pd.Items[0].Year != 2010 {
		t.Errorf("first card %+v, want the series of 2010", pd.Items[0])
	}

	// The biography falls back to English when the language asked for has no
	// text (fr is blank), and is English when none is asked for.
	for _, langs := range [][]string{{"fr"}, {"it"}, nil} {
		if pd, _ = st.GetPerson(ctx, "ada-example", 0, langs); pd.Biography != "English text." || pd.BiographyLang != "en" {
			t.Errorf("langs %q: biography %q (%s), want the English one", langs, pd.Biography, pd.BiographyLang)
		}
	}

	// limit caps the cards, newest first.
	if pd, _ = st.GetPerson(ctx, "ada-example", 2, nil); !reflect.DeepEqual(filmography(pd),
		[]string{"s-new:actor,creator", "m-mid:actor,writer,composer,animator"}) {
		t.Errorf("limit 2: %q", filmography(pd))
	}

	if pd, err = st.GetPerson(ctx, "nobody", 0, nil); pd != nil || err != nil {
		t.Errorf("unknown person: %+v, %v; want nil, nil", pd, err)
	}
}

// has_profile means a primary portrait: the one katalog-manager serves.
func TestPersonHasProfileMeansAPrimaryPortrait(t *testing.T) {
	st, db := open(t)
	db.Migrate030(t)
	adaExample(t, db)
	credit(t, db, "m-other", "Ann Nopic", "actor")
	portrait(t, db, "ada-example", true)
	portrait(t, db, "bob-other", false) // a portrait, not the primary one
	ctx := context.Background()

	for id, want := range map[string]bool{"ada-example": true, "bob-other": false, "ann-nopic": false} {
		pd, err := st.GetPerson(ctx, id, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if pd.HasProfile != want {
			t.Errorf("%s: has_profile %v, want %v", id, pd.HasProfile, want)
		}
	}
	got := map[string]bool{}
	for _, q := range []string{"example", "other", "nopic"} {
		people, err := st.SearchPeople(ctx, q, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range people {
			got[p.ID] = p.HasProfile
		}
	}
	if want := map[string]bool{"ada-example": true, "bob-other": false, "ann-nopic": false}; !reflect.DeepEqual(got, want) {
		t.Errorf("search: has_profile %v, want %v", got, want)
	}
}

// On a catalog older than 030 a person is an id and a name: served without
// details or portrait — and with them once the migration has run.
func TestPersonOnACatalogOlderThan030(t *testing.T) {
	st, db := open(t)
	adaExample(t, db)
	ctx := context.Background()

	pd, err := st.GetPerson(ctx, "ada-example", 0, []string{"de"})
	if err != nil {
		t.Fatal(err)
	}
	got := *pd
	got.Items = nil
	if want := (PersonDetail{Person: Person{ID: "ada-example", Name: "Ada Example"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("before 030: %+v, want %+v", got, want)
	}
	if f := filmography(pd); len(f) != 3 || f[1] != "m-mid:actor,writer,composer,animator" {
		t.Errorf("before 030: filmography %q", f)
	}
	// Seven credits on three titles: credits counts the titles.
	people, err := st.SearchPeople(ctx, "example", 0)
	if err != nil || len(people) != 1 || people[0].HasProfile || people[0].Credits != 3 {
		t.Fatalf("before 030: search %+v, %v; want Ada, 3 titles, no portrait", people, err)
	}

	db.Migrate030(t)
	db.Exec(t, `UPDATE com_nalet_katalog_people SET birthplace = 'Bern', biography = '{"de": "Text."}' WHERE id = 'ada-example'`)
	portrait(t, db, "ada-example", true)
	if pd, err = st.GetPerson(ctx, "ada-example", 0, nil); err != nil {
		t.Fatal(err)
	}
	if !pd.HasProfile || pd.Birthplace != "Bern" || pd.Biography != "Text." || pd.BiographyLang != "de" {
		t.Errorf("after 030: %+v, want the portrait, birthplace and the only biography", pd)
	}
	if people, err = st.SearchPeople(ctx, "example", 0); err != nil || !people[0].HasProfile {
		t.Errorf("after 030: search %+v, %v; want the portrait", people, err)
	}
}

// The read-only role was granted the catalog's tables before 030 created the
// portraits table: people are served without portraits rather than failing,
// and with them once the role is granted the table.
func TestPersonWhenTheRoleMayNotReadPortraits(t *testing.T) {
	_, db := open(t)
	db.Migrate030(t)
	adaExample(t, db)
	db.Exec(t, `UPDATE com_nalet_katalog_people SET birthplace = 'Bern' WHERE id = 'ada-example'`)
	portrait(t, db, "ada-example", true)
	role, pool := db.Role(t)
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_items, com_nalet_katalog_people, com_nalet_katalog_itempeople TO `+role)
	st := &Store{Pool: pool}
	ctx := context.Background()

	pd, err := st.GetPerson(ctx, "ada-example", 0, nil)
	if err != nil || pd.HasProfile || pd.Birthplace != "Bern" {
		t.Fatalf("without the grant: %+v, %v; want the details, no portrait", pd, err)
	}
	if people, err := st.SearchPeople(ctx, "example", 0); err != nil || len(people) != 1 || people[0].HasProfile {
		t.Fatalf("without the grant: search %+v, %v", people, err)
	}
	db.Exec(t, `GRANT SELECT ON com_nalet_katalog_personartwork TO `+role)
	if pd, err = st.GetPerson(ctx, "ada-example", 0, nil); err != nil || !pd.HasProfile {
		t.Fatalf("with the grant: %+v, %v; want the portrait", pd, err)
	}
}

func TestPickBiography(t *testing.T) {
	bios := `{"en": "English.", "de": "Deutsch.", "fr": "", "it": " ", "rm": 7}`
	for _, c := range []struct {
		raw        string
		langs      []string
		text, lang string
	}{
		{bios, []string{"de"}, "Deutsch.", "de"},
		{bios, []string{"rm", "fr", "it", "de", "en"}, "Deutsch.", "de"}, // no text: not a string, empty, blank
		{bios, []string{"es"}, "English.", "en"},
		{bios, nil, "English.", "en"},
		{`{"fr": "Français.", "de": "Deutsch."}`, []string{"es"}, "Deutsch.", "de"}, // no English: the first language
		{`{"FR": "Français.", "Ja": "日本語。"}`, []string{"ja"}, "日本語。", "ja"},
		{`{"FR": "Français.", "de": ""}`, nil, "Français.", "fr"},
		{`{"en": ""}`, nil, "", ""},
		{`{}`, []string{"de"}, "", ""},
		{``, []string{"de"}, "", ""},
		{`["not", "an object"]`, nil, "", ""},
	} {
		if text, lang := pickBiography([]byte(c.raw), c.langs); text != c.text || lang != c.lang {
			t.Errorf("pickBiography(%s, %q) = %q, %q; want %q, %q", c.raw, c.langs, text, lang, c.text, c.lang)
		}
	}
}
