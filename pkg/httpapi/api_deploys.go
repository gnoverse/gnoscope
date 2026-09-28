package httpapi

import (
	"net/http"
	"strconv"
	"strings"
)

// HandleRealmDeploys answers "when did this change", which for a package means
// its submission history and nothing else.
//
// Separate from /api/realm/{path...} rather than a field on it: the detail
// response is already the largest payload the site serves (it carries every
// file body), it is what the realm page fetches on every open, and a history
// that is one row for almost every path would be dead weight on all of them.
// The one page that wants it asks for it.
func (a *API) HandleRealmDeploys(w http.ResponseWriter, r *http.Request) {
	network := a.networkParam(r)
	path := strings.TrimRight("gno.land/"+r.PathValue("path"), "/")

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}

	deploys, err := a.db.PackageDeploys(network, path, limit)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	total, err := a.db.CountPackageDeploys(network, path)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}

	JSONResponse(w, map[string]any{
		"path":    path,
		"network": network,
		"deploys": deploys,
		"total":   total,
		// Said rather than left to be inferred from len(deploys) == limit,
		// which is ambiguous exactly when it matters.
		"truncated": total > len(deploys),
	})
}
