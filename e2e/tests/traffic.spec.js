import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

// /traffic, asserted on what it draws rather than on what it fails to complain
// about.
//
// pages.spec.js covers every route with the same weak assertion: no JS error,
// no failed request. That is the right shape for a smoke sweep and it cannot
// see this page break, because both of its failure modes are polite. An empty
// window prints "nothing recorded in this window yet" and a missing request log
// prints "traffic is not being recorded on this instance". Neither throws and
// neither fetches anything that 404s, so 424 green tests would stay green over
// a page showing nothing at all. That exact shape hid a blank /dashboards in
// gnoverse/gnoscope#411. So this file asserts on drawn output.

async function open(page, path) {
  await page.goto(path);
  await settle(page);
}

test.describe('/traffic', () => {
  test('draws the panels, from traffic the suite itself generated', async ({ page }) => {
    const seen = watch(page);

    // Generate something to report on, rather than depending on which spec file
    // ran first. The suite's own browsing is the fixture here.
    await open(page, '/realms');
    await open(page, '/packages');
    await open(page, '/realms');

    await open(page, '/traffic');

    const content = page.locator('#traffic-content');

    // The two polite failures, named so a regression says which one it is.
    await expect(content).not.toContainText('nothing recorded in this window yet');
    await expect(content).not.toContainText('traffic is not being recorded');

    // Stat tiles exist and the headline is a real number, not a zero.
    const stats = content.locator('.stat');
    expect(await stats.count()).toBeGreaterThan(5);
    const requests = await content.locator('.stat').first().innerText();
    const n = parseInt(requests.replace(/[^0-9]/g, ''), 10);
    expect(n).toBeGreaterThan(0);

    // Panels drew. The exact set depends on what was requested, so this pins
    // that several of them rendered rather than which.
    // Lowercased before comparing: .section-title is text-transform: uppercase,
    // so innerText comes back as "OVER TIME" while the source says "over time".
    const titles = (await content.locator('.section-title').allInnerTexts()).map(t => t.toLowerCase());
    expect(titles).toContain('over time');
    expect(titles.length).toBeGreaterThan(3);

    // The bars are drawn elements, not text. A panel with a title and no rows
    // is the failure this catches.
    const bars = content.locator('.section div[style*="width"]');
    expect(await bars.count()).toBeGreaterThan(0);

    expect(unexpected(seen.jsErrors), 'js errors').toEqual([]);
    expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
    expect(unexpected(seen.failedRequests), 'failed requests').toEqual([]);
  });

  test('no chart library is fetched', async ({ page }) => {
    // The page is deliberately drawn with CSS widths. gnoverse/gnoscope#411 took
    // 676 KB of CDN JavaScript off the first paint, and this pins that /traffic
    // never quietly puts it back.
    const cdn = [];
    page.on('request', r => {
      const u = r.url();
      if (u.includes('cdn.jsdelivr.net') || u.includes('d3js.org') || u.includes('unpkg.com')) cdn.push(u);
    });

    await open(page, '/traffic');
    expect(cdn).toEqual([]);
  });

  test('the window pills change the reported window', async ({ page }) => {
    const seen = [];
    page.on('request', r => {
      const u = r.url();
      if (u.includes('/api/traffic?')) seen.push(new URL(u).searchParams.get('window'));
    });

    await open(page, '/traffic');
    await page.locator('#traffic-content button', { hasText: '24h' }).click();
    await settle(page);

    expect(seen).toContain('24h');
  });

  test('who is four rows of filters, and each one narrows', async ({ page }) => {
    // The predecessor was a single include-crawlers toggle. It could answer
    // "with or without bots" and neither of the questions people actually
    // have: how much of this is machines, and which machines.
    await open(page, '/traffic');
    const content = page.locator('#traffic-content');

    const labels = await content.locator('.section-title').first().textContent();
    expect(labels).toBeTruthy();

    // Every filter row is present and independent.
    const rows = await content.locator('> div').first().innerText();
    for (const want of ['when', 'who', 'what', 'status', 'host']) {
      expect(rows).toContain(want);
    }
    for (const want of ['non-crawlers', 'all', 'crawlers', 'people', 'agents', 'unknown']) {
      expect(rows).toContain(want);
    }

    // The default view has rows, because the suite's own browsing produced them.
    expect(await content.locator('.stat').count()).toBeGreaterThan(0);

    // Narrowing must change the request and must not widen the result.
    //
    // Deliberately *not* asserting which bucket this suite's own traffic lands
    // in. The first version of this test assumed Playwright is classified
    // `browser` and asserted `crawlers` came back empty; it passed locally and
    // failed in CI, where the bundled Chromium reports HeadlessChrome and is
    // therefore a crawler. Which bucket the harness occupies is a property of
    // the machine, not of the feature.
    const sent = [];
    page.on('request', r => {
      const u = r.url();
      if (u.includes('/api/traffic?')) sent.push(new URL(u).searchParams.get('who'));
    });

    const count = async () => {
      if (await content.locator('.stat').count() === 0) return 0;
      return parseInt((await content.locator('.stat').first().innerText()).replace(/[^0-9]/g, ''), 10);
    };

    // This dashboard logs its own reads, so every click here adds a row to the
    // thing being measured, in whichever bucket this harness occupies. A count
    // taken before a click is therefore not comparable to one taken after.
    // Read `all` *after* each bucket: counts only ever grow, so
    // bucket(t1) <= all(t2) holds for t2 > t1 whatever the harness is
    // classified as.
    const readBucket = async (name) => {
      await content.locator('button', { hasText: new RegExp('^' + name + '$') }).first().click();
      await settle(page);
      return count();
    };

    let narrowed = false;
    for (const bucket of ['crawlers', 'people', 'agents']) {
      const n = await readBucket(bucket);
      expect(sent).toContain(bucket);
      const allAfter = await readBucket('all');
      expect(n).toBeLessThanOrEqual(allAfter);
      if (n < allAfter) narrowed = true;
    }
    // At least one bucket must be strictly smaller, or the pills render and
    // filter nothing, which is the failure this test exists for.
    expect(narrowed).toBe(true);
  });

  test('a filter that matches nothing says so, and says which kind of nothing', async ({ page }) => {
    // Driven through the URL rather than through clicks, so the empty state is
    // reached deterministically on any machine: no host was ever this one.
    await open(page, '/traffic?who=all&host=nowhere.invalid');
    const content = page.locator('#traffic-content');

    await expect(content).toContainText('nothing matches these filters');
    // The distinction matters: "your filters match nothing" and "the log is
    // off" send someone to look at two completely different things.
    await expect(content).not.toContainText('traffic is not being recorded');
    expect(await content.locator('.stat').count()).toBe(0);
  });

  test('filters round-trip through the URL', async ({ page }) => {
    await open(page, '/traffic?window=24h&who=crawlers&kind=api&errors=1');
    const content = page.locator('#traffic-content');
    const active = await content.locator('button.active').allInnerTexts();
    expect(active).toContain('24h');
    expect(active).toContain('crawlers');
    expect(active).toContain('api');
    expect(active).toContain('errors only');

    // And clicking writes back, so a filtered view is shareable.
    await content.locator('button', { hasText: /^7d$/ }).click();
    await settle(page);
    expect(new URL(page.url()).searchParams.get('window')).toBe('7d');
    expect(new URL(page.url()).searchParams.get('who')).toBe('crawlers');
  });

  test('the host filter defaults to this host and can be lifted', async ({ page }) => {
    const seen = [];
    page.on('request', r => {
      const u = r.url();
      if (u.includes('/api/traffic?')) seen.push(new URL(u).searchParams.get('host'));
    });

    await open(page, '/traffic');
    // Served from somewhere, so the first request pins that name rather than
    // silently blending every name the server answers to.
    expect(seen[0]).toBeTruthy();

    await page.locator('#traffic-content button', { hasText: 'every host' }).click();
    await settle(page);
    expect(seen[seen.length - 1]).toBeNull();

    // And the hosts panel is drawn, since it is the filter's own control.
    const titles = (await page.locator('#traffic-content .section-title').allInnerTexts()).map(t => t.toLowerCase());
    expect(titles).toContain('hosts');
  });

  test('the network selector does not filter this page', async ({ page }) => {
    // Network is a facet here, one of the panels, not a filter. Filtering by it
    // as well silently deletes whole categories: an MCP tool call carries its
    // network inside the JSON-RPC body rather than the query string, so a
    // selected network drops every one of them with nothing on screen saying so.
    const nets = [];
    page.on('request', r => {
      const u = r.url();
      if (u.includes('/api/traffic?')) nets.push(new URL(u).searchParams.get('network'));
    });

    await open(page, '/traffic');
    expect(nets.length).toBeGreaterThan(0);
    for (const n of nets) expect(n).toBe('all');
  });
});

test('a page view survives the navigation that causes it', async ({ page }) => {
  // The beacon fires at the moment the page changes, which is the exact moment
  // a browser is entitled to cancel in-flight requests. With fetch it did:
  // four of six navigations came back net::ERR_ABORTED and those page views
  // were lost, in proportion to how fast someone clicks. navigator.sendBeacon
  // hands the request to the browser to deliver regardless of what the page
  // does next.
  const aborted = [];
  page.on('requestfailed', r => {
    if (r.url().includes('/api/traffic/pageview')) aborted.push(r.url());
  });

  await page.goto('/');
  await settle(page);
  // Navigate several times in quick succession, which is what loses them.
  for (const p of ['/realms', '/packages', '/apps', '/txs']) {
    await page.locator(`nav a[href="${p}"]`).first().click();
  }
  await settle(page);

  expect(aborted, 'page view beacons must not be cancelled by the navigation that fires them').toEqual([]);
});

test('an in-content link is a real link and still counts as a page view', async ({ page }) => {
  // The two halves of the same request: a link people can hover, copy and
  // cmd-click, that still navigates instantly and still gets counted.
  // Seed two page views so /traffic has a realms panel to click through, and
  // so the test does not depend on which rows the fixture happens to render.
  await page.goto('/');
  await page.evaluate(() => Promise.all([
    fetch('/api/traffic/pageview?path=/realm/gno.land/r/moul/home', { method: 'POST' }),
    fetch('/api/traffic/pageview?path=/realm/r/moul/home', { method: 'POST' }),
  ]));
  await page.goto('/traffic?who=all&kind=page&host=');
  await settle(page);

  const link = page.locator('#traffic-content a[href^="/realm/"]').first();
  await expect(link).toBeVisible();

  const href = await link.getAttribute('href');
  expect(href, 'an in-content link must carry a real destination, not #').toMatch(/^\/realm\//);
  expect(await link.evaluate(a => !!a.closest('nav')), 'this must be a content link, not the rail').toBe(false);

  const sent = [];
  page.on('request', r => {
    if (r.url().includes('/api/traffic/pageview')) sent.push(new URL(r.url()).searchParams.get('path'));
  });

  await link.click();
  await settle(page);

  // Client-side: the path changed and the document did not reload.
  expect(new URL(page.url()).pathname).toBe(href);
  expect(sent, 'clicking a link in the page must report a page view').toContain(href);
});
