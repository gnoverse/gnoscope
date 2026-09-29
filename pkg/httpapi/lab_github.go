package httpapi

import (
	"net/http"

	"github.com/gnoverse/gnoscope/pkg/ghlab"
)

// The GitHub lab: what the chain cannot answer.
//
// Every other endpoint in this package reads a gno.land chain and is keyed by
// network. These are not. GitHub is one place, a repository is the same
// repository whichever network its realms end up on, and passing ?network=
// here would be a parameter that changes nothing, which is worse than no
// parameter at all.
//
// All four return a body with `enabled:false` rather than a 404 when no
// database is configured, so the page can say the section is off and why,
// instead of rendering an error that reads like a bug.

// SetGitHub hands the API its GitHub store, for the same reason SetTraffic
// exists: it is built after the API in main.
func (a *API) SetGitHub(g *ghlab.Store, reason string) {
	a.github = g
	a.githubOff = reason
}

type ghDisabled struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

func (a *API) ghOffline(w http.ResponseWriter) bool {
	if a.github != nil {
		return false
	}
	reason := a.githubOff
	if reason == "" {
		reason = "no GitHub database configured (-github-db)"
	}
	JSONResponse(w, ghDisabled{Enabled: false, Reason: reason})
	return true
}

// ghWindow reads ?days=, defaulting to 30 and capped at 365.
//
// Capped rather than free because the pull-request walk only reaches 180 days
// back (ghlab.prWindow): a 2-year window would return a real number computed
// over data this instance never fetched, and a wrong number with a plausible
// shape is the failure mode worth spending a clamp on.
func ghWindow(r *http.Request) int {
	return intParam(r.URL.Query(), "days", 30, 365)
}

// HandleLabGitHubOverview answers GET /api/lab/github/overview?days=
func (a *API) HandleLabGitHubOverview(w http.ResponseWriter, r *http.Request) {
	if a.ghOffline(w) {
		return
	}
	ov, err := a.github.Overview(ghWindow(r))
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, struct {
		Enabled bool `json:"enabled"`
		ghlab.Overview
	}{true, ov})
}

// HandleLabGitHubContributors answers GET /api/lab/github/contributors?days=&limit=
//
// Both cohorts in one response on purpose: they are read side by side and
// splitting them would put a second round trip between two halves of one
// sentence ("this many people are new, out of these who are here every day").
func (a *API) HandleLabGitHubContributors(w http.ResponseWriter, r *http.Request) {
	if a.ghOffline(w) {
		return
	}
	days := ghWindow(r)
	limit := intParam(r.URL.Query(), "limit", 50, 500)
	fresh, err := a.github.NewContributors(days, limit)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	top, err := a.github.TopContributors(days, limit)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, struct {
		Enabled bool                   `json:"enabled"`
		Days    int                    `json:"days"`
		New     []ghlab.NewContributor `json:"new"`
		Top     []ghlab.TopContributor `json:"top"`
	}{true, days, fresh, top})
}

// HandleLabGitHubPRs answers GET /api/lab/github/prs?days=&limit=&state=
func (a *API) HandleLabGitHubPRs(w http.ResponseWriter, r *http.Request) {
	if a.ghOffline(w) {
		return
	}
	days := ghWindow(r)
	limit := intParam(r.URL.Query(), "limit", 100, 500)
	state := r.URL.Query().Get("state")
	switch state {
	case "", "merged", "open":
	default:
		jsonError(w, "state must be merged, open or empty", 400)
		return
	}
	activity, err := a.github.RepoActivity(days)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	recent, err := a.github.RecentPRs(days, limit, state)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, struct {
		Enabled  bool                 `json:"enabled"`
		Days     int                  `json:"days"`
		Repos    []ghlab.RepoActivity `json:"repos"`
		Recent   []ghlab.PR           `json:"recent"`
		Filtered string               `json:"state,omitempty"`
	}{true, days, activity, recent, state})
}

// HandleLabGitHubRepos answers GET /api/lab/github/repos?kind=&source=&limit=
//
// The query totals ride along because the list is a sample of them and does
// not say so on its own: GitHub caps any search at 1,000 results, so a table
// of 312 rows drawn from a 1,504-file haystack looks complete and is not.
func (a *API) HandleLabGitHubRepos(w http.ResponseWriter, r *http.Request) {
	if a.ghOffline(w) {
		return
	}
	q := r.URL.Query()
	repos, err := a.github.Repos(q.Get("kind"), q.Get("source"), intParam(q, "limit", 500, 2000))
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	totals, err := a.github.QueryTotals()
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, struct {
		Enabled bool               `json:"enabled"`
		Repos   []ghlab.Repo       `json:"repos"`
		Queries []ghlab.QueryTotal `json:"queries"`
	}{true, repos, totals})
}
