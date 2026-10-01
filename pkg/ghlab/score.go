package ghlab

import (
	"database/sql"
	"math"

	"sort"
	"strings"
)

// The contributor score: who built gno, weighed by where and by what.
//
// Until 2026-10-01 the ranking was GitHub's per-repository commit count,
// summed, and that number is not comparable across repositories. It counts
// commits that reach the default branch, so it depends on how a repository
// merges: gnolang/gno squashes, one pull request is one commit, while a
// repository that merges with merge commits keeps every commit of every
// branch. Measured that day: the top contributor of one such repository had
// 1,072 commits from 612 merged pull requests, against 464 commits from 427
// for the top pull-request author on gnolang/gno. The table ranked the merge
// strategy, not the work.
//
// So the unit is the merged pull request, the one thing every repository
// counts the same way, weighed by tier. Commits beyond a person's merged pull
// requests (what a merge-commit repository adds, or direct pushes) still
// count, at a tenth, as do reviews and comments on somebody else's work.

// Tier is how central a repository is to gno.
type Tier int

const (
	// TierCore is the monorepo: GnoVM, tm2, gno.land.
	TierCore Tier = 1
	// TierGnolang is everything else under the gnolang org.
	TierGnolang Tier = 2
	// TierPicked is gnoverse and the staff-picked repositories (Seeds and
	// -github-repos), owned by teams building on gno.
	TierPicked Tier = 3
	// TierOther is a repository nobody curated, found by discovery.
	TierOther Tier = 4
)

// TierWeights multiply every point earned in a repository of that tier.
var TierWeights = map[Tier]float64{
	TierCore:    1.0,
	TierGnolang: 0.5,
	TierPicked:  0.25,
	TierOther:   0.1,
}

// TierOtherCap is the most one person can earn from one tier-4 repository.
//
// Discovery finds anything that mentions gno, and nobody vetted what it
// found. Measured 2026-10-01: one discovered repository held 2,000 pull
// requests opened and merged by its own owner, which ranked them tenth
// overall even at x0.1. A cap keeps "also builds on gno elsewhere" a
// tiebreaker rather than a path to the top that a script can walk.
const TierOtherCap = 25.0

// Points per action, before the tier weight.
const (
	PointsMergedPR    = 1.0
	PointsReview      = 0.1
	PointsComment     = 0.1
	PointsExtraCommit = 0.1
)

// RepoTier places a repository. source is gh_repos.source: "seed" for the
// curated list and operator extras, "discovered" for search results.
func RepoTier(fullName, source string) Tier {
	owner, _, _ := strings.Cut(strings.ToLower(fullName), "/")
	switch {
	case strings.EqualFold(fullName, "gnolang/gno"):
		return TierCore
	case owner == "gnolang":
		return TierGnolang
	case owner == "gnoverse" || source == "seed":
		return TierPicked
	}
	return TierOther
}

// TierRule is one line of the scoring legend, served beside the table so the
// page prints the rules from the code that applies them rather than from a
// copy that can drift.
type TierRule struct {
	Tier   Tier    `json:"tier"`
	Weight float64 `json:"weight"`
	Rule   string  `json:"rule"`
}

// Scoring is the legend: tiers and points.
type Scoring struct {
	Tiers  []TierRule         `json:"tiers"`
	Points map[string]float64 `json:"points"`
}

func ScoringRules() Scoring {
	return Scoring{
		Tiers: []TierRule{
			{TierCore, TierWeights[TierCore], "gnolang/gno"},
			{TierGnolang, TierWeights[TierGnolang], "the rest of gnolang/*"},
			{TierPicked, TierWeights[TierPicked], "gnoverse/* and staff-picked repositories"},
			{TierOther, TierWeights[TierOther], "every other repository discovery found, capped at 25 points a person a repository"},
		},
		Points: map[string]float64{
			"merged_pr":    PointsMergedPR,
			"review":       PointsReview,
			"comment":      PointsComment,
			"extra_commit": PointsExtraCommit,
		},
	}
}

// TopContributor is one person's score and what it is made of.
type TopContributor struct {
	Login string `json:"login"`
	// Score is all time. WindowScore is the same rule over the window, minus
	// the commit term, which carries no dates.
	Score       float64 `json:"score"`
	WindowScore float64 `json:"window_score"`
	// ByTier is the score split by tier, [0] being TierCore.
	ByTier    [4]float64 `json:"by_tier"`
	MergedPRs int        `json:"merged_prs"`
	Reviews   int        `json:"reviews"`
	Comments  int        `json:"comments"`
	Commits   int        `json:"commits"`
	// Repos is where the score comes from, biggest share first.
	Repos     []string `json:"repos"`
	AvatarURL string   `json:"avatar,omitempty"`
	// RecentMerged is how many of their pull requests merged in the window,
	// which is what separates "built this in 2022" from "is here now".
	RecentMerged int `json:"recent_merged"`
}

// repoStat is one person's activity in one repository.
type repoStat struct {
	merged, mergedWin     int
	reviews, reviewsWin   int
	comments, commentsWin int
	commits               int
}

// TopContributors ranks people by score, highest first.
func (s *Store) TopContributors(days, limit int) ([]TopContributor, error) {
	if limit <= 0 {
		limit = 50
	}
	since := cutoff(days)

	sources := map[string]string{}
	if err := s.each(`SELECT full_name, source FROM gh_repos`, nil, func(r *sql.Rows) error {
		var n, src string
		if err := r.Scan(&n, &src); err != nil {
			return err
		}
		sources[n] = src
		return nil
	}); err != nil {
		return nil, err
	}

	stats := map[string]map[string]*repoStat{} // login -> repo -> stat
	at := func(login, repo string) *repoStat {
		m := stats[login]
		if m == nil {
			m = map[string]*repoStat{}
			stats[login] = m
		}
		st := m[repo]
		if st == nil {
			st = &repoStat{}
			m[repo] = st
		}
		return st
	}

	if err := s.each(`SELECT author, full_name, COUNT(*), SUM(CASE WHEN merged_at >= ? THEN 1 ELSE 0 END)
		FROM gh_prs WHERE merged_at <> '' AND author <> '' GROUP BY author, full_name`, []any{since},
		func(r *sql.Rows) error {
			var login, repo string
			var n, win int
			if err := r.Scan(&login, &repo, &n, &win); err != nil {
				return err
			}
			st := at(login, repo)
			st.merged, st.mergedWin = n, win
			return nil
		}); err != nil {
		return nil, err
	}

	if err := s.each(`SELECT login, full_name, kind, COUNT(*), SUM(CASE WHEN at >= ? THEN 1 ELSE 0 END)
		FROM gh_pr_events GROUP BY login, full_name, kind`, []any{since},
		func(r *sql.Rows) error {
			var login, repo, kind string
			var n, win int
			if err := r.Scan(&login, &repo, &kind, &n, &win); err != nil {
				return err
			}
			st := at(login, repo)
			if kind == "review" {
				st.reviews, st.reviewsWin = n, win
			} else {
				st.comments, st.commentsWin = n, win
			}
			return nil
		}); err != nil {
		return nil, err
	}

	avatars := map[string]string{}
	if err := s.each(`SELECT login, full_name, commits, avatar FROM gh_contributors`, nil, func(r *sql.Rows) error {
		var login, repo, avatar string
		var n int
		if err := r.Scan(&login, &repo, &n, &avatar); err != nil {
			return err
		}
		at(login, repo).commits = n
		if avatar != "" {
			avatars[login] = avatar
		}
		return nil
	}); err != nil {
		return nil, err
	}

	out := make([]TopContributor, 0, len(stats))
	for login, repos := range stats {
		if IsBot(login) {
			continue
		}
		t := TopContributor{Login: login, AvatarURL: avatars[login]}
		share := map[string]float64{}
		for repo, st := range repos {
			tier := RepoTier(repo, sources[repo])
			w := TierWeights[tier]
			extra := st.commits - st.merged
			if extra < 0 {
				extra = 0
			}
			pts := w * (PointsMergedPR*float64(st.merged) +
				PointsReview*float64(st.reviews) +
				PointsComment*float64(st.comments) +
				PointsExtraCommit*float64(extra))
			winPts := w * (PointsMergedPR*float64(st.mergedWin) +
				PointsReview*float64(st.reviewsWin) +
				PointsComment*float64(st.commentsWin))
			if tier == TierOther {
				pts = math.Min(pts, TierOtherCap)
				winPts = math.Min(winPts, TierOtherCap)
			}
			t.Score += pts
			t.ByTier[tier-1] += pts
			t.WindowScore += winPts
			t.MergedPRs += st.merged
			t.Reviews += st.reviews
			t.Comments += st.comments
			t.Commits += st.commits
			t.RecentMerged += st.mergedWin
			if pts > 0 {
				share[repo] = pts
			}
		}
		if t.Score <= 0 {
			continue
		}
		for repo := range share {
			t.Repos = append(t.Repos, repo)
		}
		sort.Slice(t.Repos, func(i, j int) bool {
			a, b := share[t.Repos[i]], share[t.Repos[j]]
			if a != b {
				return a > b
			}
			return t.Repos[i] < t.Repos[j]
		})
		t.Score, t.WindowScore = round1(t.Score), round1(t.WindowScore)
		for i := range t.ByTier {
			t.ByTier[i] = round1(t.ByTier[i])
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Login < out[j].Login
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *Store) each(q string, args []any, fn func(*sql.Rows) error) error {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
