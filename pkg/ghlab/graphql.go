package ghlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The pull-request walk is GraphQL rather than REST for one reason: the
// score counts reviews and comments, and REST hands those out one pull
// request at a time. Measured 2026-10-01 on gnolang/gno: a page of 50 pull
// requests with up to 100 reviews and 100 comments each costs 1 point of the
// 5,000-an-hour GraphQL budget and answers in ~3s, so the whole history
// (4,548 pull requests) is ~91 points. The REST equivalent is three requests
// per pull request, ~14,000, almost three hours of the core budget.

// A walk page is 50 pull requests rather than GitHub's maximum of 100 because
// each one carries two nested connections of up to 100 nodes, and the bigger
// page is the one that times out.
const prWalkQuery = `query($owner:String!,$name:String!,$cursor:String){
  repository(owner:$owner,name:$name){
    pullRequests(first:50, after:$cursor, orderBy:{field:UPDATED_AT,direction:DESC}){
      pageInfo{hasNextPage endCursor}
      nodes{
        number title state isDraft createdAt updatedAt mergedAt closedAt author{login __typename}
        reviews(first:100){pageInfo{hasNextPage endCursor} nodes{author{login __typename} submittedAt}}
        comments(first:100){pageInfo{hasNextPage endCursor} nodes{author{login __typename} createdAt}}
      }
    }
  }
}`

// The follow-ups for a pull request whose reviews or comments did not fit in
// the first 100. Rare (a handful in gnolang/gno), and skipping them would
// undercount exactly the most argued-over pull requests.
const prReviewsQuery = `query($owner:String!,$name:String!,$number:Int!,$cursor:String){
  repository(owner:$owner,name:$name){ pullRequest(number:$number){
    reviews(first:100, after:$cursor){pageInfo{hasNextPage endCursor} nodes{author{login __typename} submittedAt}}
  }}
}`

const prCommentsQuery = `query($owner:String!,$name:String!,$number:Int!,$cursor:String){
  repository(owner:$owner,name:$name){ pullRequest(number:$number){
    comments(first:100, after:$cursor){pageInfo{hasNextPage endCursor} nodes{author{login __typename} createdAt}}
  }}
}`

type gqlPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type gqlActor struct {
	Login    string `json:"login"`
	Typename string `json:"__typename"`
}

// name is the login the way REST spells it. GraphQL gives a GitHub App as
// plain "netlify" with __typename Bot, where REST says "netlify[bot]"; every
// row written before the walk moved to GraphQL, and IsBot's suffix rule, use
// the REST form. Measured 2026-10-01: netlify, vercel, sonarqubecloud,
// kody-ai and google-labs-jules were five of the twelve busiest commenters
// before this, each one a bot that the name rule could not see.
func (a *gqlActor) name() string {
	if a == nil {
		return ""
	}
	if a.Typename == "Bot" && !strings.HasSuffix(a.Login, "[bot]") {
		return a.Login + "[bot]"
	}
	return a.Login
}

// gqlEvent is a review or a comment node: the two connections differ only in
// what they call the timestamp.
type gqlEvent struct {
	Author      *gqlActor `json:"author"`
	SubmittedAt string    `json:"submittedAt"`
	CreatedAt   string    `json:"createdAt"`
}

type gqlEventConn struct {
	PageInfo gqlPageInfo `json:"pageInfo"`
	Nodes    []gqlEvent  `json:"nodes"`
}

type gqlPull struct {
	Number    int          `json:"number"`
	Title     string       `json:"title"`
	State     string       `json:"state"` // OPEN | CLOSED | MERGED
	IsDraft   bool         `json:"isDraft"`
	CreatedAt string       `json:"createdAt"`
	UpdatedAt string       `json:"updatedAt"`
	MergedAt  string       `json:"mergedAt"`
	ClosedAt  string       `json:"closedAt"`
	Author    *gqlActor    `json:"author"`
	Reviews   gqlEventConn `json:"reviews"`
	Comments  gqlEventConn `json:"comments"`
}

type gqlError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// graphql runs one query. A GraphQL error comes back as a 200 with an errors
// array, so it is mapped onto HTTPError here: RATE_LIMITED as a 429 and
// NOT_FOUND as a 404, which is what lets IsRateLimited stop a pass and
// IsNotFound skip a renamed repository exactly as they do for REST.
func (c *Client) graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	c.pace(ctx, false)
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/graphql", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "gnoscope-ghlab")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	c.recordRate(resp.Header, famGraphQL)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet := string(body)
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		return &HTTPError{Status: resp.StatusCode, Path: "/graphql", Body: snippet}
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("github /graphql: %w", err)
	}
	if len(env.Errors) > 0 {
		e := env.Errors[0]
		switch e.Type {
		case "RATE_LIMITED":
			return &HTTPError{Status: http.StatusTooManyRequests, Path: "/graphql", Body: "rate limit: " + e.Message}
		case "NOT_FOUND":
			return &HTTPError{Status: http.StatusNotFound, Path: "/graphql", Body: e.Message}
		}
		return fmt.Errorf("github /graphql: %s", e.Message)
	}
	return json.Unmarshal(env.Data, out)
}

// WalkPage is one page of a pull-request walk, ready to store.
type WalkPage struct {
	PRs       []PR
	Events    []PREvent
	EndCursor string
	// Oldest is the least recently updated pull request on the page, which
	// is what the walk compares against its stop time.
	Oldest time.Time
}

// WalkPulls reads a repository's pull requests most recently updated first,
// from cursor ("" for the top), handing each page to onPage before asking for
// the next. It stops after maxPages, or once a page reaches pull requests not
// updated since stopBefore (zero: never, read the whole history), and reports
// whether it reached the end of what it was asked to read.
//
// A page is handed over complete: every pull request on it carries all of its
// reviews and comments, follow-ups included, so a store that writes page by
// page never holds half a thread.
func (c *Client) WalkPulls(ctx context.Context, fullName, cursor string, stopBefore time.Time, maxPages int,
	onPage func(WalkPage) error) (complete bool, err error) {
	owner, name, ok := strings.Cut(fullName, "/")
	if !ok {
		return false, fmt.Errorf("ghlab: bad repository name %q", fullName)
	}
	for page := 0; page < maxPages; page++ {
		vars := map[string]any{"owner": owner, "name": name, "cursor": nil}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		var res struct {
			Repository *struct {
				PullRequests struct {
					PageInfo gqlPageInfo `json:"pageInfo"`
					Nodes    []gqlPull   `json:"nodes"`
				} `json:"pullRequests"`
			} `json:"repository"`
		}
		if err := c.graphql(ctx, prWalkQuery, vars, &res); err != nil {
			return false, err
		}
		if res.Repository == nil {
			return false, &HTTPError{Status: http.StatusNotFound, Path: "/graphql", Body: fullName}
		}
		conn := res.Repository.PullRequests
		wp := WalkPage{EndCursor: conn.PageInfo.EndCursor}
		for _, n := range conn.Nodes {
			if n.Reviews.PageInfo.HasNextPage {
				more, err := c.moreEvents(ctx, owner, name, n.Number, prReviewsQuery, "reviews", n.Reviews.PageInfo.EndCursor)
				if err != nil {
					return false, err
				}
				n.Reviews.Nodes = append(n.Reviews.Nodes, more...)
			}
			if n.Comments.PageInfo.HasNextPage {
				more, err := c.moreEvents(ctx, owner, name, n.Number, prCommentsQuery, "comments", n.Comments.PageInfo.EndCursor)
				if err != nil {
					return false, err
				}
				n.Comments.Nodes = append(n.Comments.Nodes, more...)
			}
			pr, evs := n.toRows(fullName)
			wp.PRs = append(wp.PRs, pr)
			wp.Events = append(wp.Events, evs...)
			if t, err := time.Parse(time.RFC3339, n.UpdatedAt); err == nil && (wp.Oldest.IsZero() || t.Before(wp.Oldest)) {
				wp.Oldest = t
			}
		}
		if err := onPage(wp); err != nil {
			return false, err
		}
		if !conn.PageInfo.HasNextPage {
			return true, nil
		}
		if !stopBefore.IsZero() && !wp.Oldest.IsZero() && wp.Oldest.Before(stopBefore) {
			return true, nil
		}
		cursor = conn.PageInfo.EndCursor
	}
	return false, nil
}

func (c *Client) moreEvents(ctx context.Context, owner, name string, number int, query, field, cursor string) ([]gqlEvent, error) {
	var out []gqlEvent
	for {
		var res struct {
			Repository *struct {
				PullRequest map[string]gqlEventConn `json:"pullRequest"`
			} `json:"repository"`
		}
		vars := map[string]any{"owner": owner, "name": name, "number": number, "cursor": cursor}
		if err := c.graphql(ctx, query, vars, &res); err != nil {
			return out, err
		}
		if res.Repository == nil {
			return out, nil
		}
		conn := res.Repository.PullRequest[field]
		out = append(out, conn.Nodes...)
		if !conn.PageInfo.HasNextPage || conn.PageInfo.EndCursor == "" {
			return out, nil
		}
		cursor = conn.PageInfo.EndCursor
	}
}

// toRows turns a GraphQL pull request into the REST-shaped row gh_prs has
// always held, plus its events. MERGED is a closed pull request with a merge
// time, which is how REST says it and how every existing query reads it.
func (n gqlPull) toRows(fullName string) (PR, []PREvent) {
	author := n.Author.name()
	state := "closed"
	if n.State == "OPEN" {
		state = "open"
	}
	pr := PR{
		FullName: fullName, Number: n.Number, Title: n.Title, Author: author,
		State: state, Draft: n.IsDraft,
		CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, MergedAt: n.MergedAt, ClosedAt: n.ClosedAt,
	}
	var evs []PREvent
	add := func(kind string, e gqlEvent, at string) {
		// No author is a deleted account (GitHub's "ghost"); a pending
		// review has no submit time and is not visible to anyone else yet.
		login := e.Author.name()
		if login == "" || at == "" {
			return
		}
		if login == author || IsBot(login) {
			return
		}
		evs = append(evs, PREvent{FullName: fullName, Number: n.Number, Login: login, Kind: kind, At: at})
	}
	for _, r := range n.Reviews.Nodes {
		add("review", r, r.SubmittedAt)
	}
	for _, cm := range n.Comments.Nodes {
		add("comment", cm, cm.CreatedAt)
	}
	return pr, evs
}
