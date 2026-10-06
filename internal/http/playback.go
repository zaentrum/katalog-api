package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// The stream services' playback lookups. Like the asset routes they take no
// bearer (the Service is in-cluster only) and apply no rating cap: a stream
// service asks them for what a stream token already authorizes.

// Playback answers where an item's package and its original are: GET
// /api/v1/items/{id}/playback. The package that plays (its folder, the
// version it is, the record to read it by), the superseded versions a running
// session may still be served from, the one superseded last first, and the
// file it was taken in from while there is one. 404 for an id of no item.
func (h *ItemsHandler) Playback(w http.ResponseWriter, r *http.Request) {
	p, err := h.Store.Playback(r.Context(), chi.URLParam(r, "id"))
	if writeStoreErr(w, r, "playback", err) {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// ExtraPlayback answers where an extra's package is: GET
// /api/v1/extras/{extraId}/playback, its folder, the record to read it by, the
// title it belongs to and when it was packaged. 404 unless the extra is
// packaged and not removed.
func (h *ItemsHandler) ExtraPlayback(w http.ResponseWriter, r *http.Request) {
	x, err := h.Store.ExtraPlayback(r.Context(), chi.URLParam(r, "extraId"))
	if writeStoreErr(w, r, "extra playback", err) {
		return
	}
	writeJSON(w, http.StatusOK, x)
}

// PackagedIDs answers which movies and episodes have a package: GET
// /api/v1/packaged-ids, {"ids": [...]} in id order.
func (h *ItemsHandler) PackagedIDs(w http.ResponseWriter, r *http.Request) {
	ids, err := h.Store.PackagedIDs(r.Context())
	if writeStoreErr(w, r, "packaged ids", err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ids": ids})
}
