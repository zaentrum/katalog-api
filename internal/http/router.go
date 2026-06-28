package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/zaentrum/katalog-api/internal/auth"
	"github.com/zaentrum/katalog-api/internal/config"
	"github.com/zaentrum/katalog-api/internal/store"
)

// NewRouter wires the chi router with the standard middleware stack +
// the OIDC verifier + the read-only catalog handlers.
func NewRouter(cfg config.Config, st *store.Store, verifier *auth.Verifier) (http.Handler, error) {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * 1e9)) // 30s

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	r.Handle("/metrics", promhttp.Handler())

	items := &ItemsHandler{Store: st}

	// Internal asset lookup: chino-stream / tv-stream / musig-stream call this
	// with a player-issued ?stream= token (not an OIDC bearer), so OIDC can't
	// validate. Security is enforced at the network layer — the Service is
	// ClusterIP and there's no Route, so only in-cluster pods reach it.
	// Service-to-service auth (Keycloak client_credentials) is the proper
	// long-term answer; deferred until the *-stream services need it for
	// per-tenant visibility filtering.
	r.Get("/api/v1/items/{id}/asset", items.Asset)
	// Same trust model for sidecar subtitle assets — the stream services
	// resolve a subtitle id (from the item-detail subtitles[] list) into
	// an on-disk path so they can serve the .vtt straight off the packages
	// PVC instead of round-tripping the bytes through katalog-api.
	r.Get("/api/v1/subtitles/{id}/asset", items.SubtitleAsset)

	// Browse + detail surface for the product BFFs (chino-api, tv-api,
	// musig-api). OIDC-gated; each BFF relays the end-user's bearer
	// token verbatim. The shape of each endpoint mirrors what chino-api
	// historically consumed from manager-api's OData v4 service, but
	// the wire is now clean REST so future BFFs don't need an OData
	// client. Backed by `cloud_katalog_ro` (SELECT-only role).
	api := chi.NewRouter()
	api.Use(verifier.Middleware)

	// Cross-type browse + global search. `?q=` runs the FTS query
	// against the items.search_vector tsvector column; the other
	// params are filter clauses.
	api.Get("/items", items.List)
	// Type-clamped browse — what chino-api / tv-api / musig-api hit
	// for their library shelves. Same parameter set as /items; the
	// type clamp lives in the handler factory.
	api.Get("/movies", items.listByType("movie"))
	api.Get("/series", items.listByType("series"))
	api.Get("/episodes", items.listByType("episode"))
	api.Get("/albums", items.listByType("album"))

	// Catalogue-wide vocabulary list, used by chino-web's filter chips.
	api.Get("/genres", items.Genres)

	// People (cast/crew) search + a person's filmography — powers the
	// "search an actor → see their films" flow. /people?q= matches names
	// (accent-insensitive); /people/{id} returns the person + the items
	// they're credited in.
	api.Get("/people", items.People)
	api.Get("/people/{id}", items.Person)

	// Single-item detail. `?include=genres,people,subtitles,trailers,segments`
	// folds the associations into one round-trip instead of N follow-ups.
	api.Get("/items/{id}", items.Get)
	// Series → episodes association. Returns an empty `items` list (not
	// 404) when the series exists but has no episodes scanned yet.
	api.Get("/series/{id}/episodes", items.SeriesEpisodes)
	// MediaSegments for the player timeline + skip buttons.
	api.Get("/items/{id}/segments", items.Segments)
	// "More like this" — score candidates by shared genre + cast
	// against the source item. chino-web mounts this row at the
	// bottom of every movie/series detail page (#115).
	api.Get("/items/{id}/similar", items.Similar)

	r.Mount("/api/v1", api)
	return r, nil
}
