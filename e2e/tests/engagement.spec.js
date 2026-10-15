import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

// The engagement beacon, asserted on what the server actually recorded.
//
// Every failure mode of this instrumentation is silent by design: it is
// fire-and-forget, it never awaits, and it swallows its own errors so a bug in
// analytics cannot break the explorer. Nothing on the page ever says the
// beacon stopped working, so something has to check that it still does.
//
// Asserted through /api/traffic/engagement rather than by intercepting the
// POST. The beacon is sent with navigator.sendBeacon from a page the browser
// is in the middle of tearing down, and Playwright's request events do not see
// it: the first version of this file collected zero requests while the server
// was recording every event, which is a test that cannot fail for the reason
// it claims to check. The endpoint is the honest oracle, and it exercises the
// whole path.

async function open(page, path) {
  await page.goto(path);
  await settle(page);
}

// recorded reads back what the server stored. who=all because which client
// class this harness lands in is a property of the machine: CI's bundled
// Chromium reports HeadlessChrome and is classified a crawler, a headed run is
// a browser, and neither is the feature under test.
async function recorded(page) {
  return page.evaluate(async () => {
    const r = await fetch('/api/traffic/engagement?window=24h&who=all&limit=100');
    return r.json();
  });
}

test.describe('engagement beacon', () => {
  test('a visit reports how long it lasted', async ({ page }) => {
    const seen = watch(page);

    await open(page, '/realms');
    // Past five seconds, so this visit lands in a dwell bucket the rest of the
    // suite does not fill: every other spec navigates in well under a second.
    //
    // Asserting on a percentile instead was the first version, and it failed
    // in the full run while passing alone: the traffic database is shared by
    // all 649 tests, so p90 is a statistic about the suite's browsing and came
    // back at 844ms. A test in a shared fixture may only assert on evidence it
    // put there itself. The percentile arithmetic is pinned where it can be:
    // TestEngagementReadsVisits in pkg/traffic.
    await page.waitForTimeout(5500);
    // Leaving the page is what emits the visit: the event carries how long it
    // lasted, so it cannot exist before it ends.
    await open(page, '/packages');

    const eng = await recorded(page);
    expect(eng.visits, 'a completed visit').toBeGreaterThan(0);
    const bucket = (eng.dwell_buckets || []).find(b => b.label === '5-15s');
    expect(bucket, 'the 5-15s bucket this visit belongs in').toBeTruthy();
    expect(bucket.hits).toBeGreaterThan(0);
    expect(eng.median_scroll_pct).toBeGreaterThanOrEqual(0);
    expect(eng.median_scroll_pct).toBeLessThanOrEqual(100);
    // A visit nobody touched, under ten seconds, is a bounce. This one is,
    // which pins that the rate is computed rather than left at zero.
    expect(eng.bounce_rate).toBeGreaterThan(0);
    expect(eng.engaged_rate).toBeLessThanOrEqual(1);

    expect(unexpected(seen.jsErrors), 'js errors').toEqual([]);
  });

  test('a click is named from structure, never from on-chain text', async ({ page }) => {
    await open(page, '/realms');
    await page.locator('nav a#nav-packages').click();
    await settle(page);
    // Queued events are flushed when the page goes away.
    await open(page, '/traffic');

    const eng = await recorded(page);
    const labels = (eng.actions || []).map(a => a.label);
    expect(labels, 'the rail click').toContain('nav:packages');
    // The whole point of deriving labels structurally: this explorer renders
    // attacker-controlled on-chain text, and a label read off an element would
    // carry it into the dashboard. Every label is one of the known shapes.
    for (const l of labels) {
      expect(l, `label ${l} is not one of the structural shapes`).toMatch(/^(nav|tab|out|button):/);
    }
  });

  test('a search is recorded once it settles, and a term that finds nothing says so', async ({ page }) => {
    // A term no other spec can produce, for the same reason the dwell test
    // uses a bucket of its own: the traffic database is shared by the whole
    // suite, and an assertion about "how many rows say gno" is an assertion
    // about what every other file happened to type.
    const term = 'zzzz-no-such-realm';

    await open(page, '/realms');
    await page.locator('#search-input').fill(term);
    // Past the popup's 200ms debounce and the 2s settle, so one search is
    // recorded rather than one per keystroke.
    await page.waitForTimeout(2600);
    await open(page, '/packages');

    const eng = await recorded(page);
    const terms = (eng.searches || []).map(s => s.label);
    expect(terms, 'the settled term').toContain(term);
    // Typing z, zz, zzz would file three searches. The settle exists so it
    // files one, and fill() types the whole string at once, so the prefixes
    // never reach the popup at all: what this pins is that one keystroke burst
    // is one row.
    expect(terms.filter(t => t === term).length).toBe(1);
    // The count is recorded, not just the term, which is what makes the panel
    // a list of missing features rather than a list of words.
    expect((eng.zero_searches || []).map(s => s.label), 'a term that found nothing').toContain(term);
  });

  test('/traffic draws the engagement section from what the suite did', async ({ page }) => {
    const seen = watch(page);

    await open(page, '/realms');
    await page.waitForTimeout(600);
    await open(page, '/packages');
    await page.waitForTimeout(600);

    // who=all through the URL rather than by clicking, for the same reason
    // recorded() uses it: the harness's client class is a property of the
    // machine and the default view filters crawlers out.
    await open(page, '/traffic?who=all');
    const content = page.locator('#traffic-content');
    // The section is appended after a second request resolves, so it is the
    // one part of this page that can be missing without anything saying so.
    await expect(content.locator('.section-title', { hasText: /^engagement$/i })).toBeVisible();
    await expect(content).not.toContainText('nothing recorded in this window. a visit is reported');

    const titles = (await content.locator('.section-title').allInnerTexts()).map(t => t.toLowerCase());
    expect(titles).toContain('how long they stayed');
    expect(titles).toContain('how far they scrolled');

    expect(unexpected(seen.jsErrors), 'js errors').toEqual([]);
    expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
  });
});
