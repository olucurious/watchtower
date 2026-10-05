package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/olucurious/watchtower/internal/store"
)

func actorName(u store.User) string {
	if u.Name != "" {
		return u.Name
	}
	return u.Email
}

// updateProject sets the repository URL that release SHAs link to.
func (a *API) updateProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoURL string `json:"repo_url"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "expected repo_url")
		return
	}
	repo, ok := normalizeRepoURL(req.RepoURL)
	if !ok {
		writeError(w, http.StatusBadRequest, "repository URL must be an https URL such as https://github.com/owner/repo")
		return
	}
	if err := a.Store.SetProjectRepo(r.Context(), r.PathValue("slug"), repo); err != nil {
		a.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// normalizeRepoURL accepts an empty value (no links) or an https URL
// without credentials, query or fragment, trimming a trailing ".git".
func normalizeRepoURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(raw) > 300 {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimRight(u.String(), "/"), ".git"), true
}

func (a *API) listMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := a.Store.Members(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	if ms == nil {
		ms = []store.Member{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": ms})
}

func (a *API) setAssignee(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		UserID *int64 `json:"user_id"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "expected user_id (a member's ID, or null to unassign)")
		return
	}
	u := currentUser(r.Context())
	if err := a.Store.SetAssignee(r.Context(), id, req.UserID, actorName(u), u.ID); err != nil {
		a.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) addComment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(r, "id")
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	var req struct {
		Body string `json:"body"`
	}
	if err := decode(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "expected body")
		return
	}
	body, err := store.CleanComment(req.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	u := currentUser(r.Context())
	cid, err := a.Store.AddComment(r.Context(), id, body, actorName(u), u.ID)
	if err != nil {
		a.storeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": cid})
}

func (a *API) deleteComment(w http.ResponseWriter, r *http.Request) {
	id, ok1 := pathInt(r, "id")
	cid, ok2 := pathInt(r, "comment")
	if !ok1 || !ok2 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	u := currentUser(r.Context())
	if err := a.Store.DeleteComment(r.Context(), id, cid, u.ID, u.IsAdmin); err != nil {
		a.storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
