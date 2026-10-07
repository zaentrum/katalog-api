package store

// Item is the wire shape of a catalog item served by katalog-api.
//
// Field names follow the *clean REST* convention used by chino-api's
// `Item` struct (snake_case, no nested OData associations). That way
// chino-api's eventual refactor away from manager-api OData is a base
// URL change plus a small parser tweak — not a re-design of its
// internal models.
type Item struct {
	ID            string  `json:"id"`
	Type          string  `json:"type"`
	Title         string  `json:"title"`
	SortTitle     string  `json:"sort_title,omitempty"`
	Year          *int    `json:"year,omitempty"`
	Rating        float64 `json:"rating,omitempty"`
	Description   string  `json:"description,omitempty"`
	Tagline       string  `json:"tagline,omitempty"`
	DurationMs    int64   `json:"duration_ms,omitempty"`
	SeasonNumber  *int    `json:"season_number,omitempty"`
	EpisodeNumber *int    `json:"episode_number,omitempty"`
	ParentID      string  `json:"parent_id,omitempty"`

	// MinAge is the age a viewer must be to be served the item: an admin's
	// rating of it, else (an episode) its series' rating, else the minimum
	// age its certification means; nil when nothing rates it. A pointer, so
	// 0 is sent. Certification and CertificationCountry are the
	// certification TMDB gives the title that the age comes from ("12" in
	// "DE", "PG-13" in "US", ISO 3166-1 alpha-2), an episode's its series';
	// empty when an admin rated it, or nothing did.
	MinAge               *int   `json:"min_age,omitempty"`
	Certification        string `json:"certification,omitempty"`
	CertificationCountry string `json:"certification_country,omitempty"`

	// Optional rich associations populated by GetItemWithIncludes when
	// the caller asks for them via `?include=`. Always nil/empty on
	// list endpoints — those return only the core item fields.
	Genres    []string    `json:"genres,omitempty"`
	Cast      []CastEntry `json:"cast,omitempty"`
	Subtitles []Subtitle  `json:"subtitles,omitempty"`
	Trailers  []Trailer   `json:"trailers,omitempty"`
	Extras    []Extra     `json:"extras,omitempty"`
	Segments  *SegSummary `json:"segments,omitempty"`

	// Roles is set on a person's filmography only (GET /people/{id}): the
	// person's roles on this item, in credit-list order.
	Roles []string `json:"roles,omitempty"`
}

// CastEntry mirrors chino-api's CastEntry: one credit from the (people,
// itempeople) join. Actor/director/writer/… share the same row shape — the
// role discriminator drives client-side rendering. Role is an open
// vocabulary token (see creditRoles for the well-known ones); a client shows
// the ones it knows and may skip the rest.
type CastEntry struct {
	// PersonID lets clients deep-link a cast chip to that person's
	// filmography (GET /people/{id}).
	PersonID string `json:"person_id,omitempty"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	// Job is the credit's job title within the role ("Screenplay",
	// "Executive Producer"). Empty when the catalog does not know it.
	Job string `json:"job,omitempty"`
	// Character is the part an actor plays.
	Character string `json:"character,omitempty"`
	// Order is the billing order within the role, 0 first; nil when the
	// catalog has none. A pointer, so 0 is sent.
	Order *int `json:"order,omitempty"`
	// EpisodeCount is how many episodes of a series the credit covers.
	EpisodeCount *int `json:"episode_count,omitempty"`
}

// Subtitle mirrors chino-api's Subtitle. Both bear/srt/vtt + the
// `default` flag the client uses to pre-select on player start.
type Subtitle struct {
	ID      string `json:"id"`
	Lang    string `json:"lang,omitempty"`
	Label   string `json:"label,omitempty"`
	Format  string `json:"format,omitempty"`
	Default bool   `json:"default,omitempty"`
	// Forced is a subtitle a player shows by itself for the language it is in
	// (signs, a line in another language): katalog-manager's isforced
	// (migration 038). False on a catalog without the column.
	Forced bool `json:"forced,omitempty"`
}

// Trailer mirrors chino-api's Trailer. site+externalId is enough for
// chino-web to embed a YouTube iframe; url is the canonical link the
// crawler will fetch later.
type Trailer struct {
	Site       string `json:"site,omitempty"`
	ExternalID string `json:"external_id,omitempty"`
	URL        string `json:"url,omitempty"`
	Title      string `json:"title,omitempty"`
}

// Extra is one of a movie's or a series' extras that plays: bonus material
// that is a file of its own (a trailer, a teaser, a featurette, a deleted
// scene, …), packaged for streaming apart from the title. ID is the extra's
// id, which its playback path names; Kind is katalog-manager's vocabulary
// (trailer, teaser, featurette, behind-the-scenes, making-of, deleted-scene,
// interview, gag-reel, short, other). Title is what a viewer sees: an
// admin's label, else the title the extra was taken in with. Language (BCP
// 47) and DurationMs are omitted when unknown. SeasonNumber is set only on a
// series' extra that belongs to a season; a pointer, so the specials, 0, are
// sent.
type Extra struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Language     string `json:"language,omitempty"`
	DurationMs   int64  `json:"duration_ms,omitempty"`
	SeasonNumber *int   `json:"season_number,omitempty"`
}

// SegSummary is the rollup chino-web's player uses to decide whether to
// show Skip-Intro / Skip-Credits / Skip-Recap buttons. Count is the
// total segment count regardless of kind.
type SegSummary struct {
	Count      int  `json:"count"`
	HasIntro   bool `json:"has_intro"`
	HasCredits bool `json:"has_credits"`
	HasRecap   bool `json:"has_recap"`
}

// Segment is the wire shape of one MediaSegments row, returned by
// /api/v1/items/{id}/segments. The player uses these to wire the
// timeline markers + skip buttons.
type Segment struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	StartMs    int64   `json:"start_ms"`
	EndMs      int64   `json:"end_ms"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Label      string  `json:"label,omitempty"`
}

// ListResult is the paginated envelope every list endpoint returns.
// `Total` is the unfiltered match count so chino-web can render the
// "Showing 1-50 of 1976" footer without a second round-trip.
type ListResult struct {
	Items  []Item `json:"items"`
	Total  int    `json:"total"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

// ListOpts is the common set of list-endpoint parameters. Year +
// Rating use pointers so the zero value is "not set" without having
// to special-case 0.
type ListOpts struct {
	Type      string // 'movie' | 'series' | 'episode' | 'album' | …
	Query     string // FTS query (search_vector @@ websearch_to_tsquery)
	YearMin   *int
	YearMax   *int
	RatingMin *float64
	Genre     string // genre name (case-sensitive equality)
	Sort      string // 'rating' | 'year' | 'title' | 'newest' | '' (default by sortTitle)
	Limit     int    // clamped to [1, 200], default 50
	Offset    int    // clamped to >=0, default 0
}
