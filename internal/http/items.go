package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/zaentrum/katalog-api/internal/store"
)

// ItemsHandler exposes the read endpoints over the catalog `items` projection.
// All handlers query Postgres via the cloud_katalog_ro role (set in the
// connection string) — there is no write path in this service.
type ItemsHandler struct {
	Store *store.Store
}

// listByType is the shared wrapper for /movies, /series, /episodes, /albums.
// It parses the chino-web browse params, runs the store query, and returns
// the paginated envelope. Wrapping by type keeps the handler signatures
// route-specific so chi's router stays simple.
func (h *ItemsHandler) listByType(itemType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		opts := parseListOpts(r)
		opts.Type = itemType
		res, err := h.Store.ListItems(r.Context(), opts)
		if writeStoreErr(w, r, "list "+itemType, err) {
			return
		}
		writeJSON(w, http.StatusOK, res)
	}
}

// List returns items across all types — the union view chino-api uses for
// global search. Same filter set as listByType but without the type clamp.
func (h *ItemsHandler) List(w http.ResponseWriter, r *http.Request) {
	opts := parseListOpts(r)
	res, err := h.Store.ListItems(r.Context(), opts)
	if writeStoreErr(w, r, "list items", err) {
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// Get returns a single item by id, optionally expanded via `?include=`
// (genres, people, subtitles, trailers, segments). 404 when the id
// doesn't exist in the items table.
func (h *ItemsHandler) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	inc := store.ParseInclude(r.URL.Query().Get("include"))
	it, err := h.Store.GetItemWithIncludes(r.Context(), id, inc)
	if writeStoreErr(w, r, "get item", err) {
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// Asset returns the primary playback asset for an item — the row chino-stream,
// tv-stream, and musig-stream use to map item_id → file path on the media
// share. Read-only; backed by com_nalet_katalog_playbackassets via the
// cloud_katalog_ro Postgres role.
func (h *ItemsHandler) Asset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	a, err := h.Store.PrimaryAsset(r.Context(), id)
	if writeStoreErr(w, r, "primary asset", err) {
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// SubtitleAsset resolves a sidecar subtitle id → on-disk path + format
// + parent item id. Same access pattern as Asset: stream services
// call this once per sidecar fetch so they can open the .vtt file on
// the packages PVC without doing Postgres themselves.
func (h *ItemsHandler) SubtitleAsset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	a, err := h.Store.SubtitleAsset(r.Context(), id)
	if writeStoreErr(w, r, "subtitle asset", err) {
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// Genres returns the catalogue-wide genre name list, alphabetically sorted.
// Used by chino-web's browse filter chips so the picker shows real values.
func (h *ItemsHandler) Genres(w http.ResponseWriter, r *http.Request) {
	names, err := h.Store.ListGenres(r.Context())
	if writeStoreErr(w, r, "list genres", err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"genres": names})
}

// SeriesEpisodes returns every episode under the given series id, ordered
// by season and episode number. The series id is the path param; chi
// gives us either {id} or {sid} depending on the route binding.
func (h *ItemsHandler) SeriesEpisodes(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	eps, err := h.Store.ListEpisodesBySeries(r.Context(), id)
	if writeStoreErr(w, r, "series episodes", err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": eps,
		"total": len(eps),
	})
}

// Similar returns up to N catalogue items most similar to the given
// id, scored on shared genres + shared cast (see store.ListSimilar
// for the model). `?limit=` clamps the page size; defaults to 12 to
// match chino-web's "More like this" row capacity. 404 when the
// source id doesn't exist; empty list when nothing scored above zero.
func (h *ItemsHandler) Similar(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	limit := 12
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	items, err := h.Store.ListSimilar(r.Context(), id, limit)
	if writeStoreErr(w, r, "similar items", err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"total": len(items),
	})
}

// Segments returns the MediaSegments rows for an item, ordered by start
// time. The chino-web player consumes these to draw timeline markers
// and wire Skip-Intro / Skip-Credits buttons.
func (h *ItemsHandler) Segments(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	segs, err := h.Store.ListSegments(r.Context(), id)
	if writeStoreErr(w, r, "segments", err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": segs,
		"total": len(segs),
	})
}

// People searches the cast/crew by name. `?q=` is required (empty → empty
// list); `?limit=` clamps the page (default 20, max 50). Each result
// carries the person's id, name, and total credit count so the UI can
// rank and label prolific names.
func (h *ItemsHandler) People(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusOK, map[string]any{"people": []any{}, "total": 0})
		return
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	people, err := h.Store.SearchPeople(r.Context(), q, limit)
	if writeStoreErr(w, r, "search people", err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"people": people, "total": len(people)})
}

// Person returns a single cast/crew member by id, their details, and their
// filmography (the items they're credited in, newest first, each with the
// person's roles on it). `?limit=` clamps the filmography (default 100, max
// 200). The biography comes in the language `?lang=` names, else the first of
// Accept-Language's the catalog has, else English, else any. 404 when the id
// doesn't exist.
func (h *ItemsHandler) Person(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	pd, err := h.Store.GetPerson(r.Context(), id, limit, biographyLanguages(r))
	if writeStoreErr(w, r, "get person", err) {
		return
	}
	if pd == nil {
		http.Error(w, "person not found", http.StatusNotFound)
		return
	}
	// The biography's language follows the header when ?lang= is absent.
	w.Header().Add("Vary", "Accept-Language")
	writeJSON(w, http.StatusOK, pd)
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

// parseListOpts translates the standard chino-web browse query parameters
// (q, year_min, year_max, rating_min, genre, sort, limit, offset) into
// the store-layer ListOpts. Bad values are silently dropped — never
// return a 400 just because a UI control sent "year_min=banana".
func parseListOpts(r *http.Request) store.ListOpts {
	q := r.URL.Query()
	opts := store.ListOpts{
		Query: q.Get("q"),
		Genre: q.Get("genre"),
		Sort:  q.Get("sort"),
	}
	if v := q.Get("year_min"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 1800 && n < 2200 {
			opts.YearMin = &n
		}
	}
	if v := q.Get("year_max"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 1800 && n < 2200 {
			opts.YearMax = &n
		}
	}
	if v := q.Get("rating_min"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 10 {
			opts.RatingMin = &f
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.Offset = n
		}
	}
	return opts
}

// writeJSON marshals v as JSON, sets the Content-Type, and writes the
// status code. Single place for the encode-failure log line so handlers
// stay terse.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("response encode failed", "err", err)
	}
}

// writeStoreErr collapses the {ErrNotFound, ErrNoPool, generic} fan-out
// into a single helper so each handler doesn't repeat the same switch.
// Returns true if the error was handled (caller must `return`).
func writeStoreErr(w http.ResponseWriter, r *http.Request, op string, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, store.ErrNoPool):
		http.Error(w, "db not configured", http.StatusServiceUnavailable)
	default:
		slog.Error("store call failed", "op", op, "path", r.URL.Path, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
	return true
}
