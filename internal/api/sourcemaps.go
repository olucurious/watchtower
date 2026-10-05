package api

import (
	"net/http"
	"strings"

	"github.com/olucurious/watchtower/internal/store"
)

func (a *API) listSourceMaps(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	bundles, err := a.Store.Bundles(r.Context(), slug, 50)
	if err != nil {
		a.internal(w, err)
		return
	}
	tokens, err := a.Store.Tokens(r.Context(), slug)
	if err != nil {
		a.internal(w, err)
		return
	}
	if bundles == nil {
		bundles = []store.BundleRow{}
	}
	if tokens == nil {
		tokens = []store.APIToken{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bundles": bundles, "tokens": tokens, "public_url": a.PublicURL})
}

// createUploadToken issues a token scoped to one project, shown once.
func (a *API) createUploadToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Source map uploads"
	}
	slug := r.PathValue("slug")
	token, err := a.Store.CreateToken(r.Context(), name, slug, currentUser(r.Context()).ID)
	if err != nil {
		a.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token": token,
		"settings": map[string]string{
			"SENTRY_URL":        a.PublicURL,
			"SENTRY_AUTH_TOKEN": token,
			"SENTRY_ORG":        "watchtower", // any value; Watchtower has no organizations
			"SENTRY_PROJECT":    slug,
		},
	})
}

func (a *API) revokeUploadToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := a.Store.RevokeToken(r.Context(), id); err != nil {
		a.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deleteBundle(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := a.Store.DeleteBundle(r.Context(), r.PathValue("slug"), id); err != nil {
		a.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
