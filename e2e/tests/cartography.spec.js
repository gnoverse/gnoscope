// Cartography: the /lab surface that draws the chain as a place.
//
// Every one of the five views is pure client-side geometry over endpoints that
// already have Go tests, so what a backend test can vouch for here is nothing:
// /api/contracts/map can return perfect JSON while the city draws an empty box,
// a traveller can be appended and never given a coordinate, and a layout can
// collapse every package onto one point and still "render". All three of those
// happened while this was being built. These are the assertions that would have
// caught them.
import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

const NET = '?network=alpha';

async function open(page, view, extra = '') {
  await page.goto(`/cartography?v=${view}&network=alpha${extra}`);
  await settle(page);
  await page.waitForSelector('#carto-stage svg, #carto-stage canvas', { timeout: 20_000 });
}

test('the lab index offers cartography and the card opens it', async ({ page }) => {
  const w = watch(page);
  await page.goto('/lab' + NET);
  await settle(page);

  const card = page.locator('.gh-lab-card', { hasText: 'cartography' });
  await expect(card.locator('.badge')).toHaveText('experimental');
  await card.locator('.gh-lab-card-foot a').click();
  await expect(page).toHaveURL(/\/cartography/);
  await settle(page);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

// The people layer, stubbed.
//
// caller_edges is built by the syncer and the harness runs with -sync=false, so
// /api/graph/callers is legitimately empty here and settlement has nothing to
// draw. That empty state is asserted on its own below; these tests are about
// the drawing, not about where the rows come from, so they feed it a small
// deterministic graph over realms the fixture really has.
const PEOPLE = {
  nodes: [],
  edges: [
    { caller: 'g1walker000000000000000000000000000001', pkg_path: 'gno.land/r/hub/core', calls: 120 },
    { caller: 'g1walker000000000000000000000000000001', pkg_path: 'gno.land/r/consumer00/app', calls: 9 },
    { caller: 'g1walker000000000000000000000000000002', pkg_path: 'gno.land/r/consumer00/app', calls: 44 },
    { caller: 'g1walker000000000000000000000000000002', pkg_path: 'gno.land/r/consumer01/app', calls: 3 },
    { caller: 'g1walker000000000000000000000000000003', pkg_path: 'gno.land/r/hub/core', calls: 7 },
  ],
};

async function stubPeople(page) {
  await page.route('**/api/graph/callers*', route =>
    route.fulfill({ contentType: 'application/json', body: JSON.stringify(PEOPLE) }));
}

test('every view draws, and none of them throws', async ({ page }) => {
  const w = watch(page);
  await stubPeople(page);
  for (const view of ['city', 'settlement', 'orbits', 'metro', 'relief']) {
    await open(page, view);
    // A view that cannot draw paints .carto-empty or .carto-err instead, and
    // both are legitimate on a chain with no data, but not on this fixture,
    // which has packages and imports, and not with the people layer stubbed.
    await expect(page.locator('#carto-stage .carto-err')).toHaveCount(0);
    await expect(page.locator('#carto-stage .carto-empty')).toHaveCount(0);
    // Every view owes the reader a caption naming its channels. The captions
    // are the difference between a picture and a decoration.
    await expect(page.locator('.carto-note')).toBeVisible();
  }
  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('settlement says why it is empty rather than drawing a blank box', async ({ page }) => {
  // The real fixture state: no syncer, so no caller_edges, so no people. A
  // reader who lands here has to be told that and told what to try, which is
  // the difference between an answer and a page that looks broken.
  await page.goto('/cartography?v=settlement&network=alpha');
  await settle(page);
  await expect(page.locator('#carto-stage .carto-empty')).toContainText('no caller graph');
  await expect(page.locator('#carto-stage .carto-empty')).toContainText('try a longer window');
});

test('the city draws one building per package and lights the ones that were called', async ({ page }) => {
  await open(page, 'city');

  // Three polygons per building (roof and two walls), so the count is a
  // multiple of three and the building count is what the caption claims.
  const stated = Number((await page.locator('.carto-note').innerText()).match(/(\d+) of them/)[1]);
  expect(stated).toBeGreaterThan(0);
  expect(await page.locator('#carto-stage [data-cp]').count()).toBe(stated);

  // Lit windows are the channel this drawing exists for, and the caption
  // states the count. If the two ever disagree the picture is lying about the
  // one number a reader takes away from it.
  const note = await page.locator('.carto-note').innerText();
  const lit = Number(note.match(/(\d+) of \d+ are lit/)[1]);
  expect(lit).toBeGreaterThan(0);
  expect(lit).toBeLessThanOrEqual(stated);
});

test('the city flattens pure packages under calls and lets them rise under imported', async ({ page }) => {
  // The rule this pins: gas and calls are not quantities a pure package has,
  // so a tower built from them would be the drawing lying in its own height
  // channel. But being imported is the *only* quantity some pure packages
  // have, and flattening those would hide the chain's load-bearing code.
  await open(page, 'city', '&m=calls');
  await expect(page.locator('.carto-note')).toContainText('single-storey sheds');

  await open(page, 'city', '&m=importers');
  await expect(page.locator('.carto-note')).toContainText('rise here like everything else');
});

test('settlement places every traveller without a single animation frame', async ({ page }) => {
  // The hidden-tab case, reproduced honestly.
  //
  // requestAnimationFrame does not fire while document.hidden, so a traveller
  // positioned only inside the frame loop has no coordinate at all until the
  // reader looks at the page: 744 dots appended and never drawn. The fix is to
  // measure and place once at mount, and this is what proves it. Asserting on
  // a normally-rendered page does not, because there rAF runs and the dots get
  // placed by the loop whether or not the mount-time pass exists. Checked: with
  // the mount-time placement removed this test fails and the naive one passes.
  await stubPeople(page);
  await page.addInitScript(() => {
    const real = window.requestAnimationFrame.bind(window);
    window.requestAnimationFrame = cb => (window.__freezeRaf ? 0 : real(cb));
  });

  // Land on a view with no animation, freeze frames, then switch: the
  // settlement draw runs synchronously inside the click and no frame follows.
  await open(page, 'city');
  await page.evaluate(() => { window.__freezeRaf = true; });
  await page.locator('.carto-pick button', { hasText: 'settlement' }).click();
  await page.waitForSelector('#carto-stage svg circle', { timeout: 20_000 });

  const placed = await page.evaluate(() => {
    const dots = [...document.querySelectorAll('#carto-stage circle')]
      .filter(c => c.getAttribute('r') === '1.9');
    return { total: dots.length, without: dots.filter(c => !c.getAttribute('cx')).length };
  });
  expect(placed.total).toBeGreaterThan(0);
  expect(placed.without).toBe(0);
});

test('leaving cartography stops the settlement animation', async ({ page }) => {
  await stubPeople(page);
  await open(page, 'settlement');

  // The svg stays in the DOM when the view is deactivated, so a loop left
  // running holds a core at 60 Hz for as long as the tab is open, on a page
  // the reader has left. Verified by breaking the teardown: the positions do
  // keep changing without it.
  await page.evaluate(() => window.navigate('/blocks'));
  await page.waitForTimeout(300);
  const before = await page.evaluate(() =>
    [...document.querySelectorAll('#carto-stage circle')]
      .filter(c => c.getAttribute('r') === '1.9').slice(0, 5).map(c => c.getAttribute('cx')));
  await page.waitForTimeout(900);
  const after = await page.evaluate(() =>
    [...document.querySelectorAll('#carto-stage circle')]
      .filter(c => c.getAttribute('r') === '1.9').slice(0, 5).map(c => c.getAttribute('cx')));
  expect(after).toEqual(before);
});

test('the relief spreads packages instead of collapsing them onto a hub', async ({ page }) => {
  await open(page, 'relief');

  // The first draft pulled every package toward what it imported with nothing
  // pushing back, and everything that shared a dependency landed on one point:
  // renormalising then drew an ocean with a single island in it.
  //
  // The threshold comes from measuring both, not from taste. On this fixture
  // the collapsed layout paints 1.05% of the canvas above the sea floor and
  // the settled one paints 21.6%, so 8% sits an order of magnitude clear of
  // both. A "how many quadrants are non-empty" assertion was tried first and
  // is worthless: the collapsed layout still leaves a speck in three of four.
  const land = await page.evaluate(() => {
    const cv = document.querySelector('#carto-stage canvas');
    const ctx = cv.getContext('2d');
    const { data, width, height } = ctx.getImageData(0, 0, cv.width, cv.height);
    let above = 0, total = 0;
    for (let y = 0; y < height; y += 4) {
      for (let x = 0; x < width; x += 4) {
        const i = (y * width + x) * 4;
        total++;
        if (data[i] + data[i + 1] + data[i + 2] > 40) above++; // #07070b is the sea floor
      }
    }
    return above / total;
  });
  expect(land).toBeGreaterThan(0.08);
});

test('the window and the view survive a reload, and the chain is always named', async ({ page }) => {
  await open(page, 'metro', '&w=7d');
  // A drawing whose controls are not in its URL cannot be shared, and this
  // page's whole value is being able to send someone the picture you are
  // looking at.
  await page.reload();
  await settle(page);
  await expect(page.locator('.carto-pick button.on b')).toHaveText('metro');
  await expect(page.locator('#carto-bar .carto-grp button.on').first()).toHaveText('7d');

  // These endpoints resolve "all networks" to one chain of their own accord,
  // so which chain is on screen is not derivable from the header selector and
  // has to be stated.
  await expect(page.locator('.carto-net')).toContainText('alpha');
});
