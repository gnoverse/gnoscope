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

	// walkMaxPages bounds one repository's pull-request walk in one pass, at
	// 50 pull requests a page. The walk is all time, not a window: the score
	// counts every merged pull request a person ever landed, and a 180-day
	// table (what this was until 2026-10-01) ranked by commits because it had
	// nothing older to rank by. 120 pages is 6,000 pull requests, more than
	// gnolang/gno has; anything bigger resumes from its cursor next pass.
	walkMaxPages = 120

	// walkSlack is how far before the watermark an incremental walk keeps
	// reading. GitHub's updatedAt and our clock are not the same clock.
	walkSlack = time.Hour

	// discoveredWalkBudget caps how many discovered repositories get their
	// walk in one pass. A cold start finds ~120; most are one page and one
	// contributors call, so this spreads a first fill over a few passes
	// rather than refusing it.
	discoveredWalkBudget = 60

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

	if err := s.syncDiscoveredWalks(ctx, note); err != nil {
		if !IsRateLimited(err) {
			s.finish(start, problems, err)
			return err
		}
		note("discovered-repository walks stopped on the rate limit; they resume next pass")
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
	if gq := s.client.GraphQLRate(); gq.Limit > 0 {
		_ = s.store.SetMeta("rate_graphql", fmt.Sprintf("%d/%d", gq.Remaining, gq.Limit))
	}
	if !core.Reset.IsZero() {
		_ = s.store.SetMeta("rate_core_reset", core.Reset.Format(time.RFC3339))
	}
}

// syncCurated refreshes Seeds plus -github-repos, walks every one of them for
// the score, and returns how many are tracked (shown in the activity tables).
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
		// repo.FullName, not w.name: GitHub answers a renamed repository
		// under its current name and the rows have to agree with each other.
		// Untracked seeds are walked too: they are staff picks, and tier 3 of
		// the score, whether or not the activity tables list them.
		if err := s.walkRepo(ctx, repo.FullName, note); err != nil {
			return tracked, err
		}
		if w.tracked {
			tracked++
		}
	}
	return tracked, nil
}

// walkRepo brings one repository's pull requests, reviews, comments and
// commit counts up to date. Only a rate limit or a store failure is returned;
// anything else is noted and the pass moves on to the next repository.
func (s *Syncer) walkRepo(ctx context.Context, fullName string, note func(string, ...any)) error {
	st, err := s.store.Walk(fullName)
	if err != nil {
		return err
	}
	if st.Cursor == "" {
		// A fresh walk, not a resumed one. Its start time becomes the next
		// watermark, so anything updated while it runs is read again.
		st.Started = time.Now().UTC().Format(time.RFC3339)
	}
	var stop time.Time
	if st.Watermark != "" {
		if t, err := time.Parse(time.RFC3339, st.Watermark); err == nil {
			stop = t.Add(-walkSlack)
		}
	}
	var storeErr error
	complete, err := s.client.WalkPulls(ctx, fullName, st.Cursor, stop, walkMaxPages, func(p WalkPage) error {
		if err := s.store.StorePage(p.PRs, p.Events); err != nil {
			storeErr = fmt.Errorf("store pulls %s: %w", fullName, err)
			return storeErr
		}
		st.Cursor = p.EndCursor
		if err := s.store.SetWalk(fullName, st); err != nil {
			storeErr = err
		}
		return storeErr
	})
	if storeErr != nil {
		return storeErr
	}
	st.LastWalked = time.Now().UTC().Format(time.RFC3339)
	if complete {
		st.Watermark, st.Cursor = st.Started, ""
	}
	if serr := s.store.SetWalk(fullName, st); serr != nil {
		return serr
	}
	if err != nil {
		if IsRateLimited(err) {
			return err
		}
		note("pulls %s: %v", fullName, err)
	} else if !complete {
		note("pulls %s: %d pages read, the walk resumes next pass", fullName, walkMaxPages)
	}

	cs, err := s.client.FetchContributors(ctx, fullName, contributorMaxPages)
	if err != nil {
		if IsRateLimited(err) {
			return err
		}
		note("contributors %s: %v", fullName, err)
	} else if len(cs) > 0 {
		if err := s.store.ReplaceContributors(fullName, cs); err != nil {
			return fmt.Errorf("store contributors %s: %w", fullName, err)
		}
	}
	return nil
}

// syncDiscoveredWalks walks discovered repositories for tier 4 of the score,
// least recently walked first, up to discoveredWalkBudget a pass.
func (s *Syncer) syncDiscoveredWalks(ctx context.Context, note func(string, ...any)) error {
	queue, err := s.store.ScoreQueue()
	if err != nil {
		return err
	}
	if len(queue) > discoveredWalkBudget {
		queue = queue[:discoveredWalkBudget]
	}
	for _, name := range queue {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.walkRepo(ctx, name, note); err != nil {
			return err
		}
	}
	return nil
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
