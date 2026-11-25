import { expect, test } from '@playwright/test';

import { HUB, HUB_ROUTE } from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

// Page titles.
//
// Every tab, bookmark and history entry used to read "gnoscope". The server
// now writes a title into each document (pkg/web/title.go) and route() sets
// one on each navigation (pageTitle in index.html). Two implementations of one
// format, so this loads one URL of every shape and fails if the title the
// server sent is not the title the page ends up with.

const titleOf = (html) => {
  const m = html.match(/<title>([^<]*)<\/title>/);
  return m ? m[1].replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&#34;/g, '"').replace(/&#39;/g, "'") : null;
};

const PATHS = [
  '/', '/realms', '/gas/realms', '/govdao/options', '/params', '/tokens', '/apps/games',
  '/cartography', '/discover', '/code', `/realm/${HUB_ROUTE}`, '/block/1', '/govdao/1',
  '/address/g1jg8mtutu9khhfwc4nxmuhcpftf0pajdhfvsqf5', '/stdlib/strings', '/search',
  '/no-such-page',
];

for (const path of PATHS) {
  test(`${path}: the client keeps the title the server sent`, async ({ page, request }) => {
    const sep = path.includes('?') ? '&' : '?';
    const url = `${path}${sep}network=alpha`;
    const served = titleOf(await (await request.get(url)).text());
    expect(served, 'the document has a <title>').toBeTruthy();

    await page.goto(url);
    await settle(page);
    expect(await page.title()).toBe(served);
  });
}

test('a realm leads with the realm', async ({ request }) => {
  const html = await (await request.get(`/realm/${HUB_ROUTE}?network=alpha`)).text();
  expect(titleOf(html)).toBe(`${HUB} on gnoscope`);
});

test('navigating in the app retitles the tab, and going back restores it', async ({ page }) => {
  const seen = watch(page);
  await page.goto('/?network=alpha');
  await settle(page);
  expect(await page.title()).toBe('gnoscope · gno.land block explorer');

  await page.locator('#nav-realms').click();
  await expect(page).toHaveTitle('realms · contracts on gnoscope');

  await page.goBack();
  await expect(page).toHaveTitle('gnoscope · gno.land block explorer');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

// The bundle is a file of its own now, and the reason for that is that a
// browser may keep it. Asserted on what the browser actually got.
test('the bundle arrives as a cacheable, compressed asset', async ({ page }) => {
  const assets = [];
  page.on('response', (res) => {
    if (new URL(res.url()).pathname.startsWith('/assets/')) assets.push(res);
  });
  const doc = await page.goto('/?network=alpha');
  await settle(page);

  const html = await doc.text();
  expect(html.length, 'the document is small').toBeLessThan(100_000);

  // Ordered by extension, not by name: the hash decides which name sorts
  // first, so a plain sort passes or fails with the build.
  const ext = (p) => p.slice(p.lastIndexOf('.'));
  const paths = assets.map(r => new URL(r.url()).pathname)
    .sort((a, b) => ext(a).localeCompare(ext(b)));
  expect(paths).toEqual([
    expect.stringMatching(/^\/assets\/app\.[0-9a-f]{12}\.css$/),
    expect.stringMatching(/^\/assets\/app\.[0-9a-f]{12}\.js$/),
  ]);
  for (const res of assets) {
    expect(res.status()).toBe(200);
    const h = await res.allHeaders();
    expect(h['cache-control']).toContain('immutable');
    expect(h['cache-control']).toContain('max-age=31536000');
    expect(['br', 'gzip']).toContain(h['content-encoding']);
  }
});
