// Package stdlibs fetches the gno standard library from a node.
//
// Stdlib ships inside the node binary rather than as a MsgAddPackage, so it
// never appears in the transaction stream and the indexer cannot produce it.
// The only way to get it is to ask a node directly, which is what this does.
package stdlibs

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/gnoverse/gnoscope/pkg/store"
)

// Fetcher is the node read this package needs. One method, so the crawler can
// be tested without a chain.
type Fetcher interface {
	// QFile runs `vm/qfile`. On a package path it returns a newline-separated
	// file list; on a file path it returns that file's source.
	QFile(ctx context.Context, path string) (string, error)
	// QPaths runs `vm/qpaths` with the given prefix.
	QPaths(ctx context.Context, prefix string) (string, error)
}

// Crawler walks the standard library and stores it.
type Crawler struct {
	DB      *store.DB
	Fetch   Fetcher
	Network string
	// Workers bounds concurrency against the node. Modest on purpose: this
	// runs against an RPC nobody is paying for, and stdlib is ~50 packages,
	// so there is nothing to gain by being aggressive.
	Workers int
}

// Result is what one crawl did.
type Result struct {
	Packages int
	Files    int
	Skipped  int
}

// Run fetches every stdlib package and stores its files.
//
// Idempotent: stores are upserts, so a second run over an unchanged node is a
// no-op in content and only costs the reads. Worth re-running when the node
// version changes, because stdlib is versioned with the binary and not with
// the chain.
func (c *Crawler) Run(ctx context.Context) (Result, error) {
	var res Result

	paths, err := c.Packages(ctx)
	if err != nil {
		return res, err
	}
	if len(paths) == 0 {
		return res, fmt.Errorf("no stdlib packages found: the node listed none, which is never correct")
	}

	workers := c.Workers
	if workers <= 0 {
		workers = 4
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, p := range paths {
		wg.Add(1)
		go func(pkg string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			n, err := c.crawlPackage(ctx, pkg)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// One unreadable package must not lose the other forty-nine.
				// Logged and counted rather than returned, the same way the
				// sync loop treats a bad package.
				log.Printf("stdlib: %s: %v", pkg, err)
				res.Skipped++
				return
			}
			res.Packages++
			res.Files += n
		}(p)
	}
	wg.Wait()
	return res, ctx.Err()
}

// Packages lists the stdlib package paths a node holds.
//
// `vm/qpaths` with an empty prefix returns *everything* the node knows, stdlib
// and on-chain together: 586 paths on mainnet 2026-09-28, of which 536 carry
// the `gno.land/` domain and 50 do not. The ones without it are the standard
// library, which is why no hardcoded list is needed and why a new stdlib
// package appears here the day the node ships it.
func (c *Crawler) Packages(ctx context.Context) ([]string, error) {
	all, err := c.Fetch.QPaths(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("list paths: %w", err)
	}
	var out []string
	for _, p := range strings.Split(all, "\n") {
		p = strings.TrimSpace(p)
		if p == "" || !store.IsStdlibPath(p) {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// crawlPackage fetches one package's files and stores them.
func (c *Crawler) crawlPackage(ctx context.Context, pkg string) (int, error) {
	listing, err := c.Fetch.QFile(ctx, pkg)
	if err != nil {
		return 0, fmt.Errorf("list files: %w", err)
	}

	n := 0
	for _, name := range strings.Split(listing, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		body, err := c.Fetch.QFile(ctx, pkg+"/"+name)
		if err != nil {
			// A file that will not read is skipped, not fatal: the rest of the
			// package is still worth having and searchable.
			log.Printf("stdlib: %s/%s: %v", pkg, name, err)
			continue
		}
		if err := c.DB.UpsertStdlibFile(c.Network, pkg, name, body); err != nil {
			return n, fmt.Errorf("store %s: %w", name, err)
		}
		n++
	}
	if n == 0 {
		return 0, fmt.Errorf("no files stored")
	}
	return n, nil
}
