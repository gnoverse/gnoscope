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

// ghWindow reads ?window=all|<year>, defaulting to all time. ?days= is the
// older trailing window, still honoured when it is the only one given, capped
// at 365.
//
// All time is the default because the pull-request walk is all time since
// 2026-10-01, so the question the page opens on, who built gno, is one the
// data answers whole. A year is the next question, and a calendar year is
// what a reader means by it, not the last 365 days.
func ghWindow(w http.ResponseWriter, r *http.Request) (ghlab.Window, bool) {
	q := r.URL.Query()
	if q.Get("window") == "" && q.Get("days") != "" {
		return ghlab.LastDays(intParam(q, "days", 30, 365)), true
	}
	win, err := ghlab.ParseWindow(q.Get("window"))
	if err != nil {
		jsonError(w, err.Error(), 400)
		return win, false
	}
	return win, true
}

// HandleLabGitHubOverview answers GET /api/lab/github/overview?window=
func (a *API) HandleLabGitHubOverview(w http.ResponseWriter, r *http.Request) {
	if a.ghOffline(w) {
		return
	}
	win, ok := ghWindow(w, r)
	if !ok {
		return
	}
	ov, err := a.github.Overview(win)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, struct {
		Enabled bool `json:"enabled"`
		ghlab.Overview
	}{true, ov})
}

// HandleLabGitHubContributors answers GET /api/lab/github/contributors?window=&limit=
//
// Both cohorts in one response on purpose: they are read side by side and
// splitting them would put a second round trip between two halves of one
// sentence ("this many people are new, out of these who are here every day").
func (a *API) HandleLabGitHubContributors(w http.ResponseWriter, r *http.Request) {
	if a.ghOffline(w) {
		return
	}
	win, ok := ghWindow(w, r)
	if !ok {
		return
	}
	limit := intParam(r.URL.Query(), "limit", 50, 500)
	fresh, err := a.github.NewContributors(win, limit)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	top, err := a.github.TopContributors(win, limit)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, struct {
		Enabled bool                   `json:"enabled"`
		Window  ghlab.Window           `json:"window"`
		New     []ghlab.NewContributor `json:"new"`
		Top     []ghlab.TopContributor `json:"top"`
		Scoring ghlab.Scoring          `json:"scoring"`
	}{true, win, fresh, top, ghlab.ScoringRules()})
}

// HandleLabGitHubPRs answers GET /api/lab/github/prs?window=&limit=&state=
func (a *API) HandleLabGitHubPRs(w http.ResponseWriter, r *http.Request) {
	if a.ghOffline(w) {
		return
	}
	win, ok := ghWindow(w, r)
	if !ok {
		return
	}
	limit := intParam(r.URL.Query(), "limit", 100, 500)
	state := r.URL.Query().Get("state")
	switch state {
	case "", "merged", "open":
	default:
		jsonError(w, "state must be merged, open or empty", 400)
		return
	}
	activity, err := a.github.RepoActivity(win)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	recent, err := a.github.RecentPRs(win, limit, state)
	if err != nil {
		jsonError(w, err.Error(), 500)
		return
	}
	JSONResponse(w, struct {
		Enabled  bool                 `json:"enabled"`
		Window   ghlab.Window         `json:"window"`
		Repos    []ghlab.RepoActivity `json:"repos"`
		Recent   []ghlab.PR           `json:"recent"`
		Filtered string               `json:"state,omitempty"`
	}{true, win, activity, recent, state})
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
