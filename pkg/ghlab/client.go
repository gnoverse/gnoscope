package ghlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// apiBase is GitHub's REST root, overridden by tests with a local server.
const apiBase = "https://api.github.com"

// ErrNoToken is returned by NewClient when no credential is available.
//
// Refusing to run unauthenticated is deliberate. GitHub allows 60 requests an
// hour without a token and 10 search requests a *minute* with one; a single
// pass over the seed list needs more than sixty, so an unauthenticated
// instance would not fail, it would half-fill its tables and serve a picture
// of the ecosystem missing whichever repositories happened to come last. A
// blank section that says why is better than a plausible wrong one.
var ErrNoToken = errors.New("ghlab: no GitHub token (set -github-token or GITHUB_TOKEN)")

// Client is a small GitHub REST client: the four endpoint families this
// package needs, paging, and the rate-limit arithmetic.
//
// It is deliberately not a general GitHub library. Everything it reads is
// public, it never writes, and it holds one token for the life of the process.
type Client struct {
	http  *http.Client
	token string
	base  string

	// mu guards the pacing state below. Every request goes through one
	// gate, so the two limit families (core and search) cannot race each
	// other into a 403.
	mu sync.Mutex
	// nextAt is the earliest a request may leave. Search is limited per
	// minute rather than per hour, and GitHub counts a burst against the
	// whole window, so the cheapest correct thing is to space requests out
	// rather than to sprint and then sleep off a 403.
	nextAt time.Time

	// Spacing is the minimum gap between requests of each family. Exposed
	// so a test can set it to zero.
	CoreSpacing   time.Duration
	SearchSpacing time.Duration

	// lastCore and lastSearch are the most recent rate-limit headers seen,
	// reported on the overview so an operator can see the budget rather
	// than guess at it.
	lastCore    RateState
	lastSearch  RateState
	lastGraphQL RateState
}

// RateState is what GitHub's rate-limit headers last said.
type RateState struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Reset     time.Time `json:"reset"`
	Seen      time.Time `json:"seen"`
}

// NewClient builds a client, or returns ErrNoToken.
//
// The token needs no scopes: everything read here is public, and a classic
// token with nothing ticked already lifts the budget from 60 requests an hour
// to 5,000. Granting more than that buys nothing and risks something.
func NewClient(token string) (*Client, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrNoToken
	}
	return &Client{
		http:          &http.Client{Timeout: 30 * time.Second},
		token:         strings.TrimSpace(token),
		base:          apiBase,
		CoreSpacing:   120 * time.Millisecond,
		SearchSpacing: 2500 * time.Millisecond,
	}, nil
}

// Rates returns the last seen budget for both families.
func (c *Client) Rates() (core, search RateState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastCore, c.lastSearch
}

// HTTPError is a non-2xx answer, kept typed so a caller can tell a missing
// repository (404, expected: repos get renamed and deleted) from a budget
// problem (403/429, which must stop the pass rather than skip a row).
type HTTPError struct {
	Status int
	Path   string
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("github %s: %d %s", e.Path, e.Status, e.Body)
}

// IsNotFound reports whether err is a 404, which for this package means the
// repository moved or was deleted and the row should be left alone.
func IsNotFound(err error) bool {
	var he *HTTPError
	return errors.As(err, &he) && he.Status == http.StatusNotFound
}

// IsRateLimited reports whether err is GitHub refusing on budget grounds.
func IsRateLimited(err error) bool {
	var he *HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return he.Status == http.StatusTooManyRequests ||
		(he.Status == http.StatusForbidden && strings.Contains(strings.ToLower(he.Body), "rate limit"))
}

func (c *Client) get(ctx context.Context, path string, q url.Values, out any) error {
	search := strings.HasPrefix(path, "/search/")
	c.pace(ctx, search)

	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "gnoscope-ghlab")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	fam := famCore
	if search {
		fam = famSearch
	}
	c.recordRate(resp.Header, fam)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet := string(body)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return &HTTPError{Status: resp.StatusCode, Path: path, Body: snippet}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// pace blocks until the next request may leave.
func (c *Client) pace(ctx context.Context, search bool) {
	c.mu.Lock()
	gap := c.CoreSpacing
	if search {
		gap = c.SearchSpacing
	}
	now := time.Now()
	wait := time.Duration(0)
	if c.nextAt.After(now) {
		wait = c.nextAt.Sub(now)
	}
	c.nextAt = now.Add(wait + gap)
	c.mu.Unlock()
	if wait <= 0 {
		return
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// The three budgets GitHub keeps separately: REST core, REST search, and
// GraphQL, which is metered in points rather than requests.
const (
	famCore = iota
	famSearch
	famGraphQL
)

// GraphQLRate returns the last seen GraphQL budget.
func (c *Client) GraphQLRate() RateState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastGraphQL
}

func (c *Client) recordRate(h http.Header, fam int) {
	st := RateState{Seen: time.Now().UTC()}
	st.Limit, _ = strconv.Atoi(h.Get("X-RateLimit-Limit"))
	rem := h.Get("X-RateLimit-Remaining")
	if rem == "" {
		return
	}
	st.Remaining, _ = strconv.Atoi(rem)
	if sec, err := strconv.ParseInt(h.Get("X-RateLimit-Reset"), 10, 64); err == nil && sec > 0 {
		st.Reset = time.Unix(sec, 0).UTC()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch fam {
	case famSearch:
		c.lastSearch = st
	case famGraphQL:
		c.lastGraphQL = st
	default:
		c.lastCore = st
	}
	// Nearly out of budget: hold every later request until the window
	// resets rather than spending the last few on a partial pass. A
	// half-written table is the failure this whole package tries not to
	// have, and it is cheaper to be late than to be wrong.
	if st.Remaining <= 1 && !st.Reset.IsZero() {
		if until := st.Reset.Add(2 * time.Second); until.After(c.nextAt) {
			c.nextAt = until
		}
	}
}

// --- endpoints ---

type ghRepo struct {
	FullName string `json:"full_name"`
	Name     string `json:"name"`
	// Private and Visibility are a leak guard, not metadata.
	//
	// A code search runs as the token, and a token that can read a private
	// repository gets that repository's files back among the results.
	// Measured 2026-09-29: the very first discovery run on this machine
	// returned a private repository of the operator's, with the matching
	// file's path spelled out in the evidence column of a public page.
	// Repository search is fixed with `is:public` in the query; code search
	// has no such qualifier and is filtered on the hit's own repository
	// object. These two fields are the last check before a row is stored,
	// and they are why UpsertRepo refuses rather than skips.
	Private     bool                   `json:"private"`
	Visibility  string                 `json:"visibility"`
	Owner       struct{ Login string } `json:"owner"`
	Description string                 `json:"description"`
	Homepage    string                 `json:"homepage"`
	Language    string                 `json:"language"`
	License     *struct {
		SPDXID string `json:"spdx_id"`
	} `json:"license"`
	Stars      int    `json:"stargazers_count"`
	Forks      int    `json:"forks_count"`
	OpenIssues int    `json:"open_issues_count"`
	Archived   bool   `json:"archived"`
	Fork       bool   `json:"fork"`
	CreatedAt  string `json:"created_at"`
	PushedAt   string `json:"pushed_at"`
}

func (g ghRepo) toRepo(source, kind, evidence string) Repo {
	lic := ""
	if g.License != nil && g.License.SPDXID != "NOASSERTION" {
		lic = g.License.SPDXID
	}
	return Repo{
		Private:  g.Private || (g.Visibility != "" && g.Visibility != "public"),
		FullName: g.FullName, Owner: g.Owner.Login, Name: g.Name,
		Source: source, Kind: kind, Evidence: evidence,
		Description: g.Description, Homepage: g.Homepage, Language: g.Language, License: lic,
		Stars: g.Stars, Forks: g.Forks, OpenIssues: g.OpenIssues,
		Archived: g.Archived, Fork: g.Fork,
		CreatedAt: g.CreatedAt, PushedAt: g.PushedAt,
	}
}

// FetchRepo reads one repository's metadata.
//
// GitHub follows a rename, so the returned FullName may differ from the one
// asked for: `gnolang/docs` answers as `gnolang/docs.gno.land`. Callers must
// key on what comes back, never on what they sent, or a renamed repository
// gets a second row that never updates.
func (c *Client) FetchRepo(ctx context.Context, fullName, source, kind, evidence string) (Repo, error) {
	var g ghRepo
	if err := c.get(ctx, "/repos/"+fullName, nil, &g); err != nil {
		return Repo{}, err
	}
	if g.FullName == "" {
		return Repo{}, fmt.Errorf("github /repos/%s: empty full_name", fullName)
	}
	return g.toRepo(source, kind, evidence), nil
}

type ghContributor struct {
	Login         string `json:"login"`
	Contributions int    `json:"contributions"`
	AvatarURL     string `json:"avatar_url"`
	Type          string `json:"type"`
}

// FetchContributors reads all-time commit counts, up to maxPages of 100.
//
// Bots are dropped here rather than at read time: dependabot outranks every
// human on several of these repositories, and a "top contributors" table led
// by a bot is worse than no table.
func (c *Client) FetchContributors(ctx context.Context, fullName string, maxPages int) ([]Contributor, error) {
	var out []Contributor
	for page := 1; page <= maxPages; page++ {
		q := url.Values{}
		q.Set("per_page", "100")
		q.Set("page", strconv.Itoa(page))
		q.Set("anon", "0")
		var batch []ghContributor
		if err := c.get(ctx, "/repos/"+fullName+"/contributors", q, &batch); err != nil {
			return out, err
		}
		for _, g := range batch {
			if g.Login == "" || IsBot(g.Login) || g.Type == "Bot" {
				continue
			}
			out = append(out, Contributor{
				FullName: fullName, Login: g.Login,
				Commits: g.Contributions, Avatar: g.AvatarURL,
			})
		}
		if len(batch) < 100 {
			break
		}
	}
	return out, nil
}

// SearchHit is one repository a discovery query pointed at, plus the file
// that matched when the query was a code search.
type SearchHit struct {
	FullName string
	Path     string
}

type ghCodeSearch struct {
	TotalCount int  `json:"total_count"`
	Incomplete bool `json:"incomplete_results"`
	Items      []struct {
		Path       string `json:"path"`
		Repository struct {
			FullName string `json:"full_name"`
			// The leak guard for code search, which has no is:public: see
			// the note on DiscoveryQueries. These two fields are the only
			// thing standing between a token's private repositories and the
			// evidence column of a public page.
			Private    bool   `json:"private"`
			Visibility string `json:"visibility"`
		} `json:"repository"`
	} `json:"items"`
}

// SearchCode runs a code search and returns distinct repositories.
//
// Private repositories are dropped here rather than in the query, because
// code search has no `is:public` and appending one matches nothing at all
// (see DiscoveryQueries). Reported total_count still counts them, which is
// one more reason the total printed beside the table is not its length.
//
// The result is capped by GitHub at 1,000 items however many pages are asked
// for, and the count it reports is an estimate over the whole index rather
// than over what it will hand back. Both are why this returns repositories
// and a total separately: the total is worth printing, and it is not the
// length of the list.
func (c *Client) SearchCode(ctx context.Context, query string, maxPages int) ([]SearchHit, int, error) {
	seen := map[string]bool{}
	var out []SearchHit
	total := 0
	for page := 1; page <= maxPages; page++ {
		q := url.Values{}
		q.Set("q", query)
		q.Set("per_page", "100")
		q.Set("page", strconv.Itoa(page))
		var res ghCodeSearch
		if err := c.get(ctx, "/search/code", q, &res); err != nil {
			return out, total, err
		}
		total = res.TotalCount
		for _, it := range res.Items {
			fn := it.Repository.FullName
			if fn == "" || seen[fn] {
				continue
			}
			if it.Repository.Private ||
				(it.Repository.Visibility != "" && it.Repository.Visibility != "public") {
				// Dropped without being marked seen, so a later public hit on
				// the same name is not silently swallowed by this skip. In
				// practice a repository is one or the other, but the cheap
				// ordering is the one that cannot lose a row.
				continue
			}
			seen[fn] = true
			out = append(out, SearchHit{FullName: fn, Path: it.Path})
		}
		if len(res.Items) < 100 {
			break
		}
	}
	return out, total, nil
}

type ghRepoSearch struct {
	TotalCount int      `json:"total_count"`
	Items      []ghRepo `json:"items"`
}

// SearchRepos runs a repository search. Unlike code search the items already
// carry full metadata, so these need no follow-up /repos call.
func (c *Client) SearchRepos(ctx context.Context, query string, maxPages int) ([]Repo, int, error) {
	var out []Repo
	total := 0
	for page := 1; page <= maxPages; page++ {
		q := url.Values{}
		q.Set("q", query)
		q.Set("per_page", "100")
		q.Set("page", strconv.Itoa(page))
		q.Set("sort", "updated")
		var res ghRepoSearch
		if err := c.get(ctx, "/search/repositories", q, &res); err != nil {
			return out, total, err
		}
		total = res.TotalCount
		for _, g := range res.Items {
			out = append(out, g.toRepo("discovered", "", ""))
		}
		if len(res.Items) < 100 {
			break
		}
	}
	return out, total, nil
}
