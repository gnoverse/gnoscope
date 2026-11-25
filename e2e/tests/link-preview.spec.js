// Link previews for a realm.
//
// A crawler does not run the SPA, so the only thing it can read is the bytes it
// is handed. Every route gets its own <title> and og:url; only a realm (and a
// cartography view) gets a picture, and the app under the head is the same
// document everywhere.
import { expect, test } from '@playwright/test';

import { HUB, HUB_ROUTE } from '../harness/fixture.mjs';

const metaOf = (html, key, attr = 'property') => {
  const m = html.match(new RegExp(`<meta ${attr}="${key}" content="([^"]*)"`));
  return m ? m[1] : null;
};

test('a realm URL carries its own card', async ({ request }) => {
  const res = await request.get(`/realm/${HUB_ROUTE}?network=alpha`);
  expect(res.status()).toBe(200);
  const html = await res.text();

  expect(metaOf(html, 'og:title')).toContain(HUB);
  expect(metaOf(html, 'og:description')).toContain(HUB);
  expect(metaOf(html, 'twitter:card', 'name')).toBe('summary_large_image');

  const img = metaOf(html, 'og:image');
  expect(img, 'og:image').toBeTruthy();
  // Absolute, or a crawler cannot fetch it.
  expect(img).toMatch(/^https?:\/\//);
  expect(img).toContain('/api/shot?');
  expect(img).toContain('size=og');
  expect(metaOf(html, 'og:image:width')).toBe('1200');
});

// The image a crawler is pointed at has to actually be there.
test('the advertised image resolves', async ({ request }) => {
  const html = await (await request.get(`/realm/${HUB_ROUTE}`)).text();
  const img = metaOf(html, 'og:image');
  const res = await request.get(img.replace(/&amp;/g, '&'));
  expect(res.status()).toBe(200);
  expect(res.headers()['content-type']).toContain('image');
  expect((await res.body()).length).toBeGreaterThan(64);
});

// The picture stays one route wide, and what differs between two documents is
// their head and nothing after it.
test('every other route is the same app with its own head', async ({ request }) => {
  const realm = await (await request.get(`/realm/${HUB_ROUTE}`)).text();
  const bodies = await Promise.all(
    ['/', '/txs', '/blocks', '/realms', '/packages', '/address/g1abc']
      .map(async p => [p, await (await request.get(p)).text()]));

  const afterHead = (html) => html.slice(html.indexOf('</head>'));
  const base = bodies[0][1];
  for (const [path, body] of bodies) {
    expect(afterHead(body) === afterHead(base), `${path} differs from / after </head>`).toBe(true);
    expect(body.includes('og:image'), `${path} grew a per-path card`).toBe(false);
    expect(metaOf(body, 'og:url'), `${path} has no og:url`).toBeTruthy();
  }
  expect(realm.includes('og:image')).toBe(true);
});

// A per-path body still has to cost nothing on a repeat visit, and two realms
// must never share a tag or a cache serves one the other's preview.
test('realm documents revalidate and do not collide', async ({ request }) => {
  const first = await request.get(`/realm/${HUB_ROUTE}`);
  const etag = first.headers()['etag'];
  expect(etag).toBeTruthy();

  const again = await request.get(`/realm/${HUB_ROUTE}`, { headers: { 'If-None-Match': etag } });
  expect(again.status()).toBe(304);

  const other = await request.get('/realm/r/rumble/game');
  expect(other.headers()['etag']).not.toBe(etag);
});
