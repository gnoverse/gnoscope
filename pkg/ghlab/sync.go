package ghlab

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultInterval is how often a pass runs. GitHub's core budget is 5,000
	// requests an hour and a full pass costs under 200, so this is paced by
	// how fast the answer changes rather than by what is affordable: nobody
	// reading "who contributed this month" needs it fresher than three hours.
	DefaultInterval = 3 * time.Hour

	// prWindow is how far back a pull-request walk goes. Long enough that the
	// 90-day windows the page offers are complete, with a month of slack for
	// a PR whose merge lands long after it was opened.
	prWindow = 180 * 24 * time.Hour

	// prMaxPages and contributorMaxPages bound one repository's cost.
	//
	// 12, not 6, and the difference is not caution. Measured 2026-09-29:
	// gnolang/gno alone filled 6 pages and reached only 2026-06-11, 110 days,
	// so the cap and not prWindow was deciding how far back the table went.
	// A window bounded by a page count is the quiet kind of wrong: every
	// count still adds up, over a period nobody stated. Every other tracked
	// repository stops on its own well before page 3.
	prMaxPages          = 12
	contributorMaxPages = 3

	// searchMaxPages bounds each discovery query. GitHub caps any search at
	// 1,000 results however many pages are asked for, so 3 is not the reason
	// a query is incomplete; the cap is.
	searchMaxPages = 3

	// newRepoBudget caps how many previously unseen repositories get a
	// metadata fetch in one pass. Discovery finds a few hundred on its first
	// run, and spreading them over several passes keeps a cold start from
	// spending an hour's core budget in ninety seconds. The rest are picked
	// up next pass: the queries are deterministic and return them again.
	newRepoBudget = 120
)

// Syncer refreshes the GitHub tables on a timer.
type Syncer struct {
	store  *Store
	client *Client
	// extra are repositories added by the operator with -github-repos, on top
	// of Seeds. Tracked, because someone naming a repository by hand wants its
	// pull requests, not its star count.
	extra    []string
	Interval time.Duration
}

func NewSyncer(store *Store, client *Client, extra []string) *Syncer {
	return &Syncer{store: store, client: client, extra: extra, Interval: DefaultInterval}
}

// Run passes immediately, then on the interval, until ctx is done.
func (s *Syncer) Run(ctx context.Context) {
	if err := s.Once(ctx); err != nil && ctx.Err() == nil {
		log.Printf("github: sync failed: %v", err)
	}
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Once(ctx); err != nil && ctx.Err() == nil {
				log.Printf("github: sync failed: %v", err)
			}
		}
	}
}

// Once runs one full pass: curated repositories first, discovery second.
//
// The order matters on a cold start. Discovery is the part that can exhaust a
// budget, and a pass that runs out of requests halfway should have the
// curated repositories already written, because they are what the page is
// mostly about. A discovery query that does not run this pass runs the next.
func (s *Syncer) Once(ctx context.Context) error {
	start := time.Now().UTC()
	_ = s.store.SetMeta("sync_started", start.Format(time.RFC3339))

	var problems []string
	note := func(format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		log.Printf("github: %s", msg)
		problems = append(problems, msg)
	}

	tracked, err := s.syncCurated(ctx, note)
	if err != nil {
		s.finish(start, problems, err)
		return err
	}

	found, err := s.syncDiscovery(ctx, note)
	if err != nil && !IsRateLimited(err) {
		s.finish(start, problems, err)
		return err
	}
	if err != nil {
		note("discovery stopped early on the rate limit; it resumes next pass")
	}

	_ = s.store.SetMeta("tracked_repos", strconv.Itoa(tracked))
	_ = s.store.SetMeta("discovered_this_pass", strconv.Itoa(found))
	s.finish(start, problems, nil)
	return nil
}

func (s *Syncer) finish(start time.Time, problems []string, err error) {
	_ = s.store.SetMeta("sync_finished", time.Now().UTC().Format(time.RFC3339))
	_ = s.store.SetMeta("sync_seconds", strconv.FormatFloat(time.Since(start).Seconds(), 'f', 1, 64))
	if err != nil {
		problems = append([]string{err.Error()}, problems...)
	}
	// Written even when empty, so a page can tell "the last pass was clean"
	// from "there has never been a pass".
	_ = s.store.SetMeta("sync_problems", strings.Join(problems, " · "))
	core, search := s.client.Rates()
	_ = s.store.SetMeta("rate_core", fmt.Sprintf("%d/%d", core.Remaining, core.Limit))
	_ = s.store.SetMeta("rate_search", fmt.Sprintf("%d/%d", search.Remaining, search.Limit))
	if !core.Reset.IsZero() {
		_ = s.store.SetMeta("rate_core_reset", core.Reset.Format(time.RFC3339))
	}
}

// syncCurated refreshes Seeds plus -github-repos, and returns how many
// repositories got the full pull-request and contributor walk.
func (s *Syncer) syncCurated(ctx context.Context, note func(string, ...any)) (int, error) {
	type want struct {
		name    string
		kind    string
		tracked bool
	}
	list := make([]want, 0, len(Seeds)+len(s.extra))
	for _, sd := range Seeds {
		list = append(list, want{sd.FullName, sd.Kind, sd.Tracked})
	}
	for _, e := range s.extra {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		list = append(list, want{e, "operator", true})
	}

	since := time.Now().Add(-prWindow)
	tracked := 0
	for _, w := range list {
		if ctx.Err() != nil {
			return tracked, ctx.Err()
		}
		repo, err := s.client.FetchRepo(ctx, w.name, "seed", w.kind, "")
		if err != nil {
			if IsRateLimited(err) {
				return tracked, err
			}
			// A 404 here is a rename or a deletion, and the row we already
			// have is better than nothing. Named rather than swallowed: the
			// seed list is a file somebody has to edit.
			note("seed %s: %v", w.name, err)
			continue
		}
		if repo.Private {
			// A seed that has gone private, or was always private and only
			// this token can see it. Either way it must not be published.
			note("seed %s is private; skipped", repo.FullName)
			continue
		}
		repo.Tracked = w.tracked
		if err := s.store.UpsertRepo(repo, time.Now()); err != nil {
			return tracked, fmt.Errorf("store repo %s: %w", repo.FullName, err)
		}
		if !w.tracked {
			continue
		}
		// repo.FullName, not w.name: GitHub answers a renamed repository
		// under its current name and the rows have to agree with each other.
		prs, err := s.client.FetchPulls(ctx, repo.FullName, since, prMaxPages)
		if err != nil {
			if IsRateLimited(err) {
				return tracked, err
			}
			note("pulls %s: %v", repo.FullName, err)
		}
		if err := s.store.UpsertPRs(prs); err != nil {
			return tracked, fmt.Errorf("store pulls %s: %w", repo.FullName, err)
		}
		cs, err := s.client.FetchContributors(ctx, repo.FullName, contributorMaxPages)
		if err != nil {
			if IsRateLimited(err) {
				return tracked, err
			}
			note("contributors %s: %v", repo.FullName, err)
		} else if len(cs) > 0 {
			if err := s.store.ReplaceContributors(repo.FullName, cs); err != nil {
				return tracked, fmt.Errorf("store contributors %s: %w", repo.FullName, err)
			}
		}
		tracked++
	}
	return tracked, nil
}

// syncDiscovery runs every DiscoveryQuery and writes the repositories they
// point at. Returns how many rows were written this pass.
func (s *Syncer) syncDiscovery(ctx context.Context, note func(string, ...any)) (int, error) {
	known, err := s.knownRepos()
	if err != nil {
		return 0, err
	}
	budget := newRepoBudget
	written := 0

	for _, dq := range DiscoveryQueries {
		if ctx.Err() != nil {
			return written, ctx.Err()
		}
		if !dq.Code {
			repos, total, err := s.client.SearchRepos(ctx, dq.Query, searchMaxPages)
			if err != nil {
				if IsRateLimited(err) {
					return written, err
				}
				note("search %q: %v", dq.Query, err)
				continue
			}
			_ = s.store.SetMeta("query_total:"+dq.Query, strconv.Itoa(total))
			for _, r := range repos {
				if skipRepo(r.FullName) || r.Private {
					continue
				}
				r.Kind = dq.Kind
				r.Evidence = dq.Why
				if err := s.store.UpsertRepo(r, time.Now()); err != nil {
					return written, err
				}
				written++
			}
			continue
		}

		hits, total, err := s.client.SearchCode(ctx, dq.Query, searchMaxPages)
		if err != nil {
			if IsRateLimited(err) {
				return written, err
			}
			note("search %q: %v", dq.Query, err)
			continue
		}
		_ = s.store.SetMeta("query_total:"+dq.Query, strconv.Itoa(total))
		for _, h := range hits {
			if skipRepo(h.FullName) {
				continue
			}
			evidence := dq.Why + " (" + h.Path + ")"
			if known[h.FullName] {
				// Already have the metadata; refresh only the claim, which
				// costs no request at all. Stars going stale for a repository
				// we are not tracking is the cheapest thing to be wrong about.
				if err := s.store.TouchDiscovery(h.FullName, dq.Kind, evidence, time.Now()); err != nil {
					return written, err
				}
				written++
				continue
			}
			if budget <= 0 {
				continue
			}
			budget--
			repo, err := s.client.FetchRepo(ctx, h.FullName, "discovered", dq.Kind, evidence)
			if err != nil {
				if IsRateLimited(err) {
					return written, err
				}
				note("repo %s: %v", h.FullName, err)
				continue
			}
			if repo.Private {
				// Reached only when a search returned it despite is:public.
				// Belt and braces, and cheap: one comparison.
				continue
			}
			if err := s.store.UpsertRepo(repo, time.Now()); err != nil {
				return written, err
			}
			known[repo.FullName] = true
			written++
		}
	}
	if budget <= 0 {
		note("new-repo budget of %d spent; the rest are picked up next pass", newRepoBudget)
	}
	return written, nil
}

func (s *Syncer) knownRepos() (map[string]bool, error) {
	rows, err := s.store.db.Query(`SELECT full_name FROM gh_repos`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

// skipRepo drops results that are gno's own code wearing a different name.
//
// `-org:gnolang` in a query excludes the monorepo but not a fork of it, and a
// fork of gnolang/gno matching "imports github.com/gnolang/gno in a go.mod" is
// true and useless: it is the same go.mod. The fork flag from the API is the
// general answer, applied at read time; this is the cheap one, applied before
// a request is spent.
func skipRepo(fullName string) bool {
	owner, _, ok := strings.Cut(fullName, "/")
	if !ok {
		return true
	}
	switch strings.ToLower(owner) {
	case "gnolang":
		return true
	}
	return false
}
