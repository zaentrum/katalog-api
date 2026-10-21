package http

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/zaentrum/katalog-api/internal/store"
)

// Search is one search of the catalog: its titles, films, series and
// episodes ranked together by how they match the query, best first (the
// tiers of store/search.go), and the people whose names match it, ranked
// apart. ?q= is the query (blank: nothing found); ?types= the types of title
// to look at, comma separated (movie, series, episode; all of them when it
// names none); ?limit= how many titles (default 30, at most 100); ?people=
// how many people (default 12, at most 50; 0 looks for none). A capped
// viewer is served what the cap allows (capped).
//
// The answer is {"items": [...], "people": [...]}: each title as the list
// endpoints send it, with how it matched (match) and, an episode, its series
// (series: id, title, and whether it matched by its own title too); each
// person as /people sends them, with how their name matched.
func (h *ItemsHandler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("q"))
	opts := store.SearchOpts{Query: query, Limit: store.SearchLimit}
	for _, v := range q["types"] {
		opts.Types = append(opts.Types, strings.Split(v, ",")...)
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			opts.Limit = n
		}
	}
	peopleLimit := store.SearchPeopleLimit
	if v := q.Get("people"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			peopleLimit = n
		}
	}
	res, err := h.Store.SearchItems(r.Context(), opts)
	if writeStoreErr(w, r, "search", err) {
		return
	}
	people := []store.Person{}
	if peopleLimit > 0 && query != "" {
		people, err = h.Store.SearchPeopleRanked(r.Context(), query, peopleLimit)
		if writeStoreErr(w, r, "search people", err) {
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": res.Items, "people": people})
}
