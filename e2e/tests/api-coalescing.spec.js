// api() coalesces identical GETs that are in flight at the same moment.
//
// route() runs twice on initial page load, on purpose: once synchronously at
// the bottom of the script, and again when loadProposerMonikers() lands so a
// late proposer name re-renders. Both passes read the same endpoints, so a
// landing page used to issue each of them twice. Measured against production
// on 2026-09-24, /govdao/proposals issued /api/govdao/overview twice and both
// copies cost the full 2.4s, because both missed a cold server cache at the
// same instant.
//
// Coalescing fixes the overlapping case, which is the expensive one: two
// requests that are in flight together become one. It cannot fix a second pass
// that starts after the first has already finished, and the last test here
// pins that limit so it is a known shape rather than a surprise.

import { expect, test } from '@playwright/test';

import { settle, watch } from './helpers.js';

// Count requests per /api path, ignoring the streaming endpoint, which is a
// long-lived SSE connection rather than a fetch.
function countApiRequests(page) {
  const counts = new Map();
  page.on('request', req => {
    const u = new URL(req.url());
    if (!u.pathname.startsWith('/api/') || u.pathname === '/api/live') return;
    counts.set(u.pathname + u.search, (counts.get(u.pathname + u.search) || 0) + 1);
  });
  return counts;
}

test('concurrent api() calls for one path make one request', async ({ page }) => {
  await page.goto('/');
  await settle(page);

  const counts = countApiRequests(page);
  const results = await page.evaluate(async () => {
    // Six at once, which is what a page with several panels reading the same
    // endpoint looks like.
    const all = await Promise.all(Array.from({ length: 6 }, () => api('govdao/overview')));
    return all.map(r => (r && typeof r === 'object') ? 'object' : typeof r);
  });

  expect(results).toEqual(Array(6).fill('object'));
  const overview = [...counts.entries()].filter(([k]) => k.startsWith('/api/govdao/overview'));
  const total = overview.reduce((n, [, c]) => n + c, 0);
  expect(total, `six concurrent callers issued ${total} requests: ${JSON.stringify(overview)}`).toBe(1);
});

test('a rejected request does not pin a poisoned entry', async ({ page }) => {
  await page.goto('/');
  await settle(page);

  // A path that 404s, twice in a row. The second attempt must reach the
  // network rather than be served a cached rejection.
  const counts = countApiRequests(page);
  const outcomes = await page.evaluate(async () => {
    const out = [];
    for (let i = 0; i < 2; i++) {
      try { await api('definitely/not/a/route'); out.push('resolved'); }
      catch (_) { out.push('rejected'); }
    }
    return out;
  });

  expect(outcomes).toEqual(['rejected', 'rejected']);
  const tries = [...counts.entries()].filter(([k]) => k.includes('definitely/not/a/route'));
  const total = tries.reduce((n, [, c]) => n + c, 0);
  expect(total, 'a failed request must not be cached as a rejection').toBe(2);
});

test('the govdao overview is fetched once on /govdao, and the page renders', async ({ page }) => {
  const seen = watch(page);
  const counts = countApiRequests(page);

  await page.goto('/govdao');
  await settle(page);

  const overview = [...counts.entries()].filter(([k]) => k.startsWith('/api/govdao/overview'));
  const total = overview.reduce((n, [, c]) => n + c, 0);
  expect(total, `govdao/overview requests: ${JSON.stringify(overview)}`).toBe(1);

  await expect(page.locator('#govdao-content')).toBeVisible();
  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

// The limit, pinned deliberately.
//
// loadGovDAOProposals awaits its one apiSWR call, so on this page the two
// route() passes do not overlap: the second starts after the first has
// resolved, and there is nothing in flight to join. The duplicate is therefore
// still two requests, and coalescing is the wrong layer to fix it -- the fix is
// to stop re-running the whole router just to re-render proposer names.
//
// It is cheap now (the server answers a warm overview in ~12ms) which is why
// this is recorded rather than fixed here. If this test starts failing because
// the count dropped to 1, that is the router fix landing: delete this test.
test('a sequential second route() pass still refetches, and that is known', async ({ page }) => {
  const counts = countApiRequests(page);

  await page.goto('/govdao/proposals');
  await settle(page);

  const overview = [...counts.entries()].filter(([k]) => k.startsWith('/api/govdao/overview'));
  const total = overview.reduce((n, [, c]) => n + c, 0);
  expect(total, 'expected the known sequential duplicate').toBeLessThanOrEqual(2);
  await expect(page.locator('#govdao-proposals-content')).toBeVisible();
});
