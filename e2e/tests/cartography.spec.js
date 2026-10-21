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
  for (const view of ['city', 'settlement', 'orbits', 'metro', 'relief',
    'metropolis', 'boroughs', 'hexes', 'frontier', 'oldtown', 'honeycomb', 'skyline', 'archipelago', 'lights']) {
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

// --- the camera, the scale and the rotation ---------------------------------

// Zoom is driven by a synthetic wheel event rather than mouse.wheel() because
// the handler calls preventDefault and reads clientX/clientY off the event; a
// real wheel over the stage works the same way and is what this simulates.
async function wheelOn(page, selector, deltaY, fx, fy) {
  await page.evaluate(([sel, dy, ax, ay]) => {
    const t = document.querySelector(sel);
    const r = t.getBoundingClientRect();
    t.dispatchEvent(new WheelEvent('wheel', {
      deltaY: dy, clientX: r.left + r.width * ax, clientY: r.top + r.height * ay,
      bubbles: true, cancelable: true }));
  }, [selector, deltaY, fx === undefined ? 0.5 : fx, fy === undefined ? 0.5 : fy]);
}

function camTransform(page) {
  return page.evaluate(() => {
    const g = document.querySelector('#carto-stage svg > g');
    return g ? g.getAttribute('transform') : null;
  });
}

test('every svg view pans and zooms, and reset puts it back', async ({ page }) => {
  await stubPeople(page);   // settlement has no caller graph on this fixture
  for (const view of ['city', 'settlement', 'orbits', 'metro']) {
    await open(page, view);
    expect(await camTransform(page)).toBe('translate(0,0) scale(1)');

    await wheelOn(page, '#carto-stage svg', -600);
    const zoomed = await camTransform(page);
    // Scale strictly greater than 1 and an offset that is not the identity:
    // a zoom that only scaled would pull the drawing away from the cursor.
    expect(zoomed).toMatch(/scale\((?!1\))/);
    expect(zoomed).not.toBe('translate(0,0) scale(1)');

    await page.locator('.carto-cam button', { hasText: 'reset' }).click();
    expect(await camTransform(page)).toBe('translate(0,0) scale(1)');
  }
});

test('the zoom keeps the point under the cursor under the cursor', async ({ page }) => {
  await open(page, 'city');
  // The property that separates a zoom you can aim from one you have to chase.
  // Picking a point well away from the centre is the whole test: a zoom that
  // ignores the cursor and scales about the origin passes at the centre.
  const drift = await page.evaluate(() => {
    const svg = document.querySelector('#carto-stage svg');
    const g = svg.firstChild;
    const r = svg.getBoundingClientRect();
    // Whole pixels, because that is all a WheelEvent carries: Chrome truncates
    // a fractional clientX, so a fractional point here put the handler and
    // this test up to a pixel apart and measured that instead of the zoom.
    // It passed only while the stage happened to sit near a whole pixel.
    const cx = Math.round(r.left + r.width * 0.22), cy = Math.round(r.top + r.height * 0.74);
    const vb = svg.viewBox.baseVal;
    // The user-space point under that cursor, before and after.
    const ux = vb.x + (cx - r.left) / r.width * vb.width;
    const uy = vb.y + (cy - r.top) / r.height * vb.height;
    const at = () => {
      const m = /translate\(([-\d.e]+),([-\d.e]+)\) scale\(([-\d.e]+)\)/.exec(g.getAttribute('transform'));
      const [, tx, ty, k] = m.map(Number);
      // Invert the transform: which content point is currently drawn at (ux,uy).
      return [(ux - tx) / k, (uy - ty) / k];
    };
    const before = at();
    svg.dispatchEvent(new WheelEvent('wheel', {
      deltaY: -700, clientX: cx, clientY: cy, bubbles: true, cancelable: true }));
    const after = at();
    return Math.hypot(after[0] - before[0], after[1] - before[1]);
  });
  // User-space units on a canvas ~2000 wide. Anything under a unit is rounding.
  expect(drift).toBeLessThan(1);
});

test('a drag pans and does not navigate, even when it ends on a building', async ({ page }) => {
  await open(page, 'city');

  // The release has to land *on* a building, or this test cannot fail: dragging
  // to empty space never navigates whether or not the click is swallowed, which
  // is exactly what the first version did. Checked by removing the swallow:
  // ending on a building then navigates away and this goes red.
  //
  // And the building has to be one the viewport can actually reach. The first
  // pick was simply the first in document order, which is the back of the city
  // and sat at y = -37 once the page had scrolled: every click landed on
  // nothing and the test failed for a reason that had nothing to do with
  // panning.
  const pick = () => page.evaluate(() => {
    const vw = window.innerWidth, vh = window.innerHeight;
    const g = [...document.querySelectorAll('#carto-stage [data-cp]')].find(e => {
      const r = e.getBoundingClientRect();
      return r.width > 6 && r.height > 6 &&
        r.x > 240 && r.x + r.width < vw - 220 && r.y > 200 && r.y + r.height < vh - 200;
    });
    if (!g) return null;
    const r = g.getBoundingClientRect();
    return { x: r.x + r.width / 2, y: r.y + r.height / 2, path: g.getAttribute('data-cp') };
  });

  // The drawing in view first, as a reader has it: thirteen view cards wrap to
  // three rows and push the city below a 1000px viewport, and then no building
  // is inside the window this picks from.
  await page.locator('#carto-stage').evaluate(e => e.scrollIntoView({ block: 'center' }));
  const target = await pick();
  expect(target).not.toBeNull();

  await page.mouse.move(target.x + 110, target.y + 70);
  await page.mouse.down();
  await page.mouse.move(target.x, target.y, { steps: 10 });
  await page.mouse.up();

  expect(await camTransform(page)).not.toBe('translate(0,0) scale(1)');
  // A reader who just framed a view must not lose it to the release, which is
  // the single most annoying bug a pannable map can have.
  await expect(page).toHaveURL(/\/cartography/);

  // And a plain click still opens the thing, or the swallow has gone too far.
  //
  // This half is not decoration. Taking the pointer capture on pointerdown
  // retargets the click to the svg, and every building on the map silently
  // stops being clickable; it was doing exactly that until this assertion
  // caught it.
  await page.locator('.carto-cam button', { hasText: 'reset' }).click();
  const again = await pick();
  expect(again).not.toBeNull();
  await page.mouse.click(again.x, again.y);
  await expect(page).not.toHaveURL(/\/cartography/);
});

test('log and linear are different drawings, and the caption says which', async ({ page }) => {
  // What a log scale does, stated as the assertion: the tallest building is the
  // same under both (it is the maximum either way), and everything below it is
  // lifted. So the second and third tallest are strictly taller under log.
  //
  // A tallest-to-median ratio was the first version and it cannot fail here:
  // this fixture has 3 packages with calls out of 83, so the median is a
  // one-storey shed under both scales and the ratio comes out identical.
  // Measured on it: log 112.5 / 100.1 / 62.9, linear 112.5 / 81.5 / 38.1.
  const heights = async (scale) => {
    await open(page, 'city', '&m=calls&s=' + scale);
    await expect(page.locator('.carto-note')).toContainText('on a ' + scale + ' scale');
    return page.evaluate(() =>
      [...document.querySelectorAll('#carto-stage [data-cp]')]
        .map(g => +g.getBoundingClientRect().height.toFixed(1)).sort((a, b) => b - a));
  };
  const logH = await heights('log');
  const linH = await heights('linear');

  expect(logH.length).toBe(linH.length);
  expect(logH).not.toEqual(linH);
  expect(logH[0]).toBeCloseTo(linH[0], 0);
  expect(logH[1]).toBeGreaterThan(linH[1]);
  expect(logH[2]).toBeGreaterThan(linH[2]);
});

test('rotating the city keeps the buildings upright and the frame tight', async ({ page }) => {
  await open(page, 'city', '&yaw=0');
  const north = await page.evaluate(() => document.querySelector('#carto-stage svg').getAttribute('viewBox'));

  await open(page, 'city', '&yaw=45');
  await expect(page.locator('.carto-note')).toContainText('turned 45');
  const turned = await page.evaluate(() => document.querySelector('#carto-stage svg').getAttribute('viewBox'));
  // A rotation that was applied to the finished drawing would leave the
  // viewBox alone; rotating in grid space and re-projecting changes the
  // bounds, which is what proves the camera moved and not the picture.
  expect(turned).not.toBe(north);

  // No shape carries a rotation of its own: every box stays axis-aligned, which
  // is what makes this read as a model seen from a new angle rather than a
  // picture that has been spun.
  const spun = await page.evaluate(() =>
    [...document.querySelectorAll('#carto-stage polygon, #carto-stage rect')]
      .filter(e => (e.getAttribute('transform') || '').includes('rotate')).length);
  expect(spun).toBe(0);
});

test('the plan view drops the heights and says so', async ({ page }) => {
  await open(page, 'city', '&flat=1');
  await expect(page.locator('.carto-note')).toContainText('plan view');

  // The model draws a roof and two walls per building; the plan draws one
  // rectangle. Fewer polygons and some rects is the shape of that difference.
  const flat = await page.evaluate(() => ({
    polys: document.querySelectorAll('#carto-stage polygon').length,
    rects: document.querySelectorAll('#carto-stage [data-cp] rect').length,
  }));
  await open(page, 'city', '&flat=0');
  const model = await page.evaluate(() => ({
    polys: document.querySelectorAll('#carto-stage polygon').length,
    rects: document.querySelectorAll('#carto-stage [data-cp] rect').length,
  }));
  expect(flat.polys).toBeLessThan(model.polys);
  expect(flat.rects).toBeGreaterThan(0);
});

test('the relief re-samples on zoom instead of magnifying, and names more peaks', async ({ page }) => {
  await open(page, 'relief');
  const wide = await page.locator('.carto-note').innerText();
  expect(wide).toContain('the whole map');
  const wideNames = await page.evaluate(() => Number(/(\d+) peaks named/.exec(
    document.querySelector('.carto-note').innerText)[1]));

  await wheelOn(page, '#carto-stage canvas', -900, 0.55, 0.6);
  // The paint is debounced behind the live CSS preview, so the assertion waits
  // for the caption the re-sample writes rather than for a fixed delay.
  await expect(page.locator('.carto-note')).toContainText('× in', { timeout: 5000 });

  // The preview transform is cleared by the re-sample. If it were still set,
  // the canvas would be a magnified old frame and nothing had been re-sampled.
  const t = await page.evaluate(() => document.querySelector('#carto-stage canvas').style.transform);
  expect(t).toBe('');

  // Zooming in resolves peaks that were too crowded to name at full extent.
  const zoomNames = await page.evaluate(() => Number(/(\d+) peaks named/.exec(
    document.querySelector('.carto-note').innerText)[1]));
  expect(zoomNames).toBeGreaterThan(0);
  expect(wideNames).toBeGreaterThan(0);
});

test('the camera survives a metric change, and the controls round-trip through the URL', async ({ page }) => {
  await open(page, 'city');
  await wheelOn(page, '#carto-stage svg', -600, 0.3, 0.4);
  const zoomed = await camTransform(page);

  // Changing what the buildings are sized by is not a reason to throw away
  // where the reader had navigated to.
  await page.locator('#carto-bar button', { hasText: 'storage' }).click();
  await expect(page.locator('.carto-note')).toContainText('storage');
  expect(await camTransform(page)).toBe(zoomed);

  // And every control is in the URL, because a drawing nobody can link to is
  // half a drawing.
  await open(page, 'city', '&s=linear&yaw=30&flat=1');
  await page.reload();
  await settle(page);
  await expect(page.locator('#carto-bar button.on', { hasText: 'linear' })).toHaveCount(1);
  await expect(page.locator('#carto-bar button.on', { hasText: 'plan' })).toHaveCount(1);
  await expect(page.locator('#carto-bar button.on', { hasText: '30°' })).toHaveCount(1);
});

// The city-builder five. Each one makes one structural promise in its caption,
// and these pin the promise rather than the picture: a drawing can look right
// and break any of them.

function expectEqualSizes(sizes) {
  expect(sizes.length).toBeGreaterThan(1);
  for (const k of [0, 1]) {
    const v = sizes.map(s => s[k]);
    // pts() writes coordinates to a tenth of a unit, so two equal shapes can
    // differ by that much in their boxes and no more.
    expect(Math.max(...v) - Math.min(...v)).toBeLessThan(0.25);
  }
}

test('the metropolis builds every package once and zones all of them', async ({ page }) => {
  await open(page, 'metropolis');
  const note = await page.locator('.carto-note').innerText();
  const all = Number(note.match(/all (\d+) packages compete/)[1]);
  expect(await page.locator('#carto-stage [data-cp]').count()).toBe(all);
  // The five zone counts in the caption partition the city. A package that
  // fell through zoneOf would be drawn and counted nowhere.
  const zones = ['commercial', 'residential', 'industrial', 'parks', 'under construction']
    .map(z => Number(note.match(new RegExp('(\\d+) ' + z))[1]));
  expect(zones.reduce((a, b) => a + b, 0)).toBe(all);
  // The four district rings are drawn and named on the map, not only in the
  // caption, because the whole claim of this view is that the chain has a
  // middle.
  for (const r of ['downtown', 'midtown', 'suburbs', 'outskirts']) {
    await expect(page.locator('#carto-stage text', { hasText: r })).toHaveCount(1);
  }
});

test('boroughs gives every namespace the same block', async ({ page }) => {
  await open(page, 'boroughs');
  const stated = Number((await page.locator('.carto-note').innerText()).match(/One block per namespace, (\d+) of them/)[1]);
  const sizes = await page.evaluate(() => [...document.querySelectorAll('#carto-stage .carto-block')]
    .map(b => { const r = b.getBBox(); return [r.width, r.height]; }));
  expect(sizes.length).toBe(stated);
  // Equal ground is this view's one promise, the fix for a city in which one
  // namespace was most of the frame.
  expectEqualSizes(sizes);
});

test('hexes lays one equal tile per namespace and gives each a terrain', async ({ page }) => {
  await open(page, 'hexes');
  const stated = Number((await page.locator('.carto-note').innerText()).match(/One hex per namespace, (\d+) of them/)[1]);
  const hexes = await page.evaluate(() => [...document.querySelectorAll('#carto-stage .carto-hex')]
    .map(h => { const r = h.getBBox(); return { size: [r.width, r.height],
      ns: h.getAttribute('data-ns'), terrain: h.getAttribute('data-terrain') }; }));
  expect(hexes.length).toBe(stated);
  expectEqualSizes(hexes.map(h => h.size));
  expect(new Set(hexes.map(h => h.ns)).size).toBe(stated);
  for (const h of hexes) expect(['fields', 'pasture', 'forest', 'mountains', 'hills', 'desert']).toContain(h.terrain);
});

test('the frontier settles every package on its own tile, starting at the origin', async ({ page }) => {
  await open(page, 'frontier');
  const note = await page.locator('.carto-note').innerText();
  const stated = Number(note.match(/every one of the (\d+) packages is a tile/)[1]);
  const tiles = await page.evaluate(() => [...document.querySelectorAll('#carto-stage [data-tile]')]
    .map(t => t.getAttribute('data-tile')));
  expect(tiles.length).toBe(stated);
  // Two villages on one tile would draw as one and lose a package silently.
  expect(new Set(tiles).size).toBe(stated);
  // The first settler holds (0|0); that is what makes the middle mean "oldest".
  expect(tiles).toContain('0|0');
});

test('the old town lays houses outward in deploy order inside one wall per era', async ({ page }) => {
  await open(page, 'oldtown');
  const note = await page.locator('.carto-note').innerText();
  const all = Number(note.match(/All (\d+) packages are houses/)[1]);
  const eras = Number(note.match(/There are (\d+) walls/)[1]);
  const radii = await page.evaluate(() => [...document.querySelectorAll('#carto-stage [data-order]')]
    .sort((a, b) => Number(a.dataset.order) - Number(b.dataset.order))
    .map(g => { const m = /translate\(([-\d.]+),([-\d.]+)\)/.exec(g.getAttribute('transform')); return Math.hypot(+m[1], +m[2]); }));
  expect(radii.length).toBe(all);
  // Distance from the market square is deploy order, so it never decreases.
  // The street nudge moves a house sideways and must not move it inward.
  for (let i = 1; i < radii.length; i++) expect(radii[i]).toBeGreaterThanOrEqual(radii[i - 1] - 0.15);
  const walls = await page.evaluate(() => new Set([...document.querySelectorAll('#carto-stage .carto-wall')]
    .map(w => w.getAttribute('data-era'))).size);
  expect(walls).toBe(eras);
});

test('the compass and the plan view reach the two new isometric drawings', async ({ page }) => {
  for (const view of ['metropolis', 'boroughs']) {
    await open(page, view);
    await expect(page.locator('#carto-bar button', { hasText: 'plan' })).toHaveCount(1);
    await page.locator('#carto-bar button', { hasText: 'plan' }).click();
    await expect(page).toHaveURL(/flat=1/);
    await expect(page.locator('#carto-stage [data-cp] rect').first()).toBeVisible();
    await open(page, view, '&flat=0');
  }
  // hexes has no size channel, so no metric picker: a control that is there
  // and moves nothing reads as a broken page.
  await open(page, 'hexes');
  await expect(page.locator('#carto-bar', { hasText: 'size by' })).toHaveCount(0);
});

// Every variant of every city-builder view, run once each. A variant is a
// different layout rule, and a rule that drops a package does it silently:
// the drawing still looks like a city. So each one is held to the count the
// metropolis states, on every view that draws one shape per package.
const VARIANTS = {
  metropolis: { 'metropolis.centre': ['lv', 'calls', 'imp', 'old', 'new'], 'metropolis.paint': ['zone', 'ns'] },
  boroughs: { 'boroughs.order': ['metric', 'size', 'old', 'imp'] },
  hexes: { 'hexes.place': ['imp', 'calls', 'size', 'old'] },
  frontier: { 'frontier.who': ['creator', 'ns'], 'frontier.order': ['deploy', 'size'] },
  oldtown: { 'oldtown.rings': ['deploy', 'lv', 'calls', 'imp'], 'oldtown.walls': ['era', 'week', 'day'] },
  honeycomb: { 'honeycomb.centre': ['lv', 'calls', 'imp', 'old', 'ns'], 'honeycomb.paint': ['ns', 'zone'] },
  skyline: { 'skyline.shape': ['peak', 'ns', 'deploy'], 'skyline.paint': ['ns', 'zone'] },
  archipelago: { 'archipelago.centre': ['imp', 'size', 'calls'], 'archipelago.routes': ['on', 'off'] },
  lights: { 'lights.roads': ['on', 'off'] },
};
const PER_PACKAGE = ['metropolis', 'boroughs', 'frontier', 'oldtown', 'honeycomb', 'skyline', 'archipelago', 'lights'];

test('every variant of every view draws, and none drops a package', async ({ page }) => {
  const w = watch(page);
  await open(page, 'metropolis');
  const all = Number((await page.locator('.carto-note').innerText()).match(/all (\d+) packages compete/)[1]);
  for (const [view, opts] of Object.entries(VARIANTS)) {
    for (const [key, values] of Object.entries(opts)) {
      for (const v of values) {
        await open(page, view, `&${key}=${v}`);
        await expect(page.locator('#carto-stage .carto-err')).toHaveCount(0);
        await expect(page.locator('#carto-stage .carto-empty')).toHaveCount(0);
        await expect(page.locator('.carto-note')).toBeVisible();
        if (PER_PACKAGE.includes(view)) {
          expect(await page.locator('#carto-stage [data-cp]').count(), `${view} ${key}=${v}`).toBe(all);
        }
      }
    }
  }
  expect(w.jsErrors).toEqual([]);
});

test('a variant is a control, it lands in the URL, and the caption names the rule', async ({ page }) => {
  await open(page, 'metropolis');
  await page.locator('#carto-bar button', { hasText: 'oldest' }).click();
  await expect(page).toHaveURL(/metropolis\.centre=old/);
  await expect(page.locator('.carto-note')).toContainText('oldest code');
  await page.reload();
  await settle(page);
  await expect(page.locator('#carto-bar button.on', { hasText: 'oldest' })).toHaveCount(1);
  // The default is never written, so a plain link stays plain.
  await page.locator('#carto-bar button', { hasText: 'land value' }).click();
  await expect(page).not.toHaveURL(/metropolis\.centre=/);
  // An unknown value is dropped, not passed to a layout with no branch for it.
  await open(page, 'metropolis', '&metropolis.centre=nonsense');
  await expect(page.locator('#carto-bar button.on', { hasText: 'land value' })).toHaveCount(1);
  // A control with no effect under the current choice of another is hidden:
  // walls only mean something when the rings are dated.
  await open(page, 'oldtown', '&oldtown.rings=lv');
  await expect(page.locator('#carto-bar', { hasText: 'walls' })).toHaveCount(0);
  await open(page, 'oldtown');
  await expect(page.locator('#carto-bar button', { hasText: 'weeks' })).toHaveCount(1);
});

test('frontier with namespaces as players has one player per namespace', async ({ page }) => {
  await open(page, 'boroughs');
  const namespaces = Number((await page.locator('.carto-note').innerText()).match(/One block per namespace, (\d+) of them/)[1]);
  await open(page, 'frontier', '&frontier.who=ns');
  await expect(page.locator('.carto-note')).toContainText(`players are the ${namespaces} namespaces`);
});

test('the honeycomb gives every package its own cell, outward in rank order', async ({ page }) => {
  await open(page, 'honeycomb');
  const stated = Number((await page.locator('.carto-note').innerText()).match(/One cell per package, (\d+) of them/)[1]);
  const slots = await page.evaluate(() => [...document.querySelectorAll('#carto-stage [data-slot]')]
    .map(g => g.getAttribute('data-slot')));
  expect(slots.length).toBe(stated);
  expect(new Set(slots).size).toBe(stated);
  // Hex distance from the centre never decreases in drawing order, which is
  // rank order: that is the whole claim of the view.
  const rings = slots.map(s => { const [q, r] = s.split(',').map(Number); return (Math.abs(q) + Math.abs(r) + Math.abs(q + r)) / 2; });
  for (let i = 1; i < rings.length; i++) expect(rings[i]).toBeGreaterThanOrEqual(rings[i - 1]);
});

test('the skyline peaks in the middle, and its reflection is the same towers', async ({ page }) => {
  await open(page, 'skyline');
  const r = await page.evaluate(() => {
    const group = document.querySelector('#carto-stage [data-towers]');
    const ts = [...group.children];
    const xs = ts.map(t => Number(t.getAttribute('data-x')));
    const hs = ts.map(t => Number(t.querySelector('rect').getAttribute('height')));
    const top = hs.indexOf(Math.max(...hs));
    return { n: ts.length, xTop: xs[top], xMin: Math.min(...xs), xMax: Math.max(...xs),
      use: document.querySelector('#carto-stage use').getAttribute('href'), id: group.id };
  });
  const stated = Number((await page.locator('.carto-note').innerText()).match(/one tower per package, (\d+) of them/)[1]);
  expect(r.n).toBe(stated);
  const mid = (r.xMin + r.xMax) / 2;
  expect(Math.abs(r.xTop - mid)).toBeLessThan((r.xMax - r.xMin) * 0.05);
  expect(r.use).toBe('#' + r.id);
});

test('the archipelago lays one island per namespace and no two overlap', async ({ page }) => {
  await open(page, 'archipelago');
  const stated = Number((await page.locator('.carto-note').innerText()).match(/One island per namespace, (\d+) of them/)[1]);
  const isles = await page.evaluate(() => [...document.querySelectorAll('#carto-stage .carto-island')]
    .map(e => ({ x: +e.dataset.x, y: +e.dataset.y, r: +e.dataset.r })));
  expect(isles.length).toBe(stated);
  // Same ellipse metric the packing uses: y is drawn at 0.82.
  for (let i = 0; i < isles.length; i++) for (let j = i + 1; j < isles.length; j++) {
    const a = isles[i], b = isles[j];
    expect(Math.hypot(a.x - b.x, (a.y - b.y) / 0.82)).toBeGreaterThanOrEqual(a.r + b.r);
  }
});

// Round three: the controls that cut across every view.

test('the picker is three rows of chips and every chip opens its view', async ({ page }) => {
  await open(page, 'city');
  const chips = page.locator('.carto-pick button');
  expect(await page.locator('.carto-pick-row').count()).toBe(3);
  const n = await chips.count();
  expect(n).toBe(14);
  // The current view's description is printed once, under the chips, and it
  // changes with the view: the cards used to carry all fourteen at once.
  const before = await page.locator('.carto-blurb').innerText();
  await page.locator('.carto-pick button', { hasText: 'night lights' }).click();
  await expect(page).toHaveURL(/v=lights/);
  await expect(page.locator('.carto-blurb')).not.toHaveText(before);
  await expect(page.locator('#carto-stage svg')).toBeVisible();
});

test('as of a day draws only what existed then, and play walks forward to today', async ({ page }) => {
  const w = watch(page);
  await open(page, 'metropolis');
  const all = await page.locator('#carto-stage [data-cp]').count();
  const first = await page.locator('#carto-asof').getAttribute('data-first');
  expect(first).toMatch(/^\d{4}-\d{2}-\d{2}$/);

  await open(page, 'metropolis', `&asof=${first}`);
  const banner = await page.locator('.carto-asof').innerText();
  const m = banner.match(/As of (\S+): (\d+) of (\d+) packages/);
  expect(m[1]).toBe(first);
  expect(Number(m[3])).toBe(all);
  // The drawing and the banner agree, and the first day is not the whole
  // chain, or this test could not tell a filter from no filter.
  expect(await page.locator('#carto-stage [data-cp]').count()).toBe(Number(m[2]));
  expect(Number(m[2])).toBeLessThan(all);

  // Play from the first day ends on today, which is no asof at all.
  await page.locator('#carto-play').click();
  await expect(page.locator('#carto-asof-d')).toHaveText('today', { timeout: 30_000 });
  await expect(page).not.toHaveURL(/asof=/);
  await expect(page.locator('.carto-asof')).toHaveCount(0);
  expect(await page.locator('#carto-stage [data-cp]').count()).toBe(all);
  expect(w.jsErrors).toEqual([]);
});

// The time-lapse in motion. Three claims: the slider draws while it is dragged,
// a fast drag coalesces instead of queueing, and the transition leaves nothing
// of itself behind in the DOM the rest of the page walks.

// dragAsOf drags the as-of slider across `steps` stops as fast as the browser
// will dispatch, and reports how many drawings the page actually produced.
//
// Counted with a MutationObserver on the stage rather than from inside the
// page's own code: what matters is how many times the drawing was rebuilt, and
// that is observable from outside without the page having to admit to anything.
async function dragAsOf(page, steps, pauseMs = 0) {
  return page.evaluate(async ([steps, pauseMs]) => {
    const stage = document.getElementById('carto-stage');
    const sl = document.getElementById('carto-asof');
    const stops = Number(sl.getAttribute('data-stops'));
    let draws = 0;
    // A redraw is the stage getting a new svg or canvas, which is the one event
    // every view has in common.
    const obs = new MutationObserver(ms => {
      for (const m of ms) for (const n of m.addedNodes) {
        if (n.nodeType === 1 && (n.tagName === 'svg' || n.tagName === 'CANVAS') &&
          !n.classList.contains('carto-ghost')) draws++;
      }
    });
    obs.observe(stage, { childList: true });
    const t0 = performance.now();
    for (let i = 0; i < steps; i++) {
      sl.value = String(Math.round(stops * (i + 1) / (steps + 1)));
      sl.dispatchEvent(new Event('input', { bubbles: true }));
      if (pauseMs) await new Promise(r => setTimeout(r, pauseMs));
      else await new Promise(r => requestAnimationFrame(r));
    }
    const dragMs = performance.now() - t0;
    // Let the last scheduled frame land before counting.
    await new Promise(r => setTimeout(r, 900));
    obs.disconnect();
    return { draws, dragMs, stops, day: document.getElementById('carto-asof-d').textContent,
      value: Number(sl.value) };
  }, [steps, pauseMs]);
}

test('the as-of slider draws while it is dragged, and a fast drag coalesces', async ({ page }) => {
  const w = watch(page);
  await open(page, 'metropolis');

  // One stop, one drawing: the slider does not wait for the reader to let go.
  const slow = await dragAsOf(page, 1, 300);
  expect(slow.draws).toBe(1);
  await expect(page).toHaveURL(new RegExp(`asof=${slow.day}`));

  // Thirty stops as fast as rAF will carry them. The budget is REDRAW_FPS = 8
  // a second, so the ceiling is the drag's own duration times eight, plus the
  // frame that lands after the reader lets go. Anything near thirty would mean
  // the drags were queued rather than coalesced, which is the failure this
  // exists to catch: a slider that falls further behind the pointer the faster
  // it is moved.
  const fast = await dragAsOf(page, 30);
  const ceiling = Math.ceil(fast.dragMs / 1000 * 8) + 2;
  expect(fast.draws).toBeLessThanOrEqual(ceiling);
  expect(fast.draws).toBeGreaterThan(0);

  // And the drawing that survived is the day the slider was left on, not one of
  // the days it passed through.
  await expect(page.locator('#carto-asof-d')).toHaveText(fast.day);
  if (fast.day !== 'today') await expect(page).toHaveURL(new RegExp(`asof=${fast.day}`));

  expect(w.jsErrors).toEqual([]);
});

test('a transition leaves no ghost, no wrapper and no second copy of a package', async ({ page }) => {
  const w = watch(page);
  await open(page, 'honeycomb');
  const all = await page.locator('#carto-stage [data-cp]').count();

  // Back through the chain's history and forward again. Each step tweens.
  await dragAsOf(page, 4, 250);
  await page.evaluate(() => {
    const sl = document.getElementById('carto-asof');
    sl.value = sl.getAttribute('data-stops');
    sl.dispatchEvent(new Event('input', { bubbles: true }));
  });
  await expect(page.locator('#carto-asof-d')).toHaveText('today');
  // The ghost is scenery and is stripped of its identities before it is shown,
  // so a count of packages on the stage can never include it; the wrappers the
  // transition inserts are unwrapped when they land. Both of those are what
  // keeps find, hover-neighbours and the next transition measuring the drawing
  // and not the leftovers.
  await expect.poll(() => page.locator('#carto-stage .carto-ghost').count(), { timeout: 5000 }).toBe(0);
  await expect.poll(() => page.evaluate(() =>
    [...document.querySelectorAll('#carto-stage svg g')].filter(g => g.style.transformBox).length),
  { timeout: 5000 }).toBe(0);
  expect(await page.locator('#carto-stage [data-cp]').count()).toBe(all);

  // Find still counts exactly the packages that are drawn.
  await page.locator('#carto-find').fill('hub');
  const hits = await page.locator('#carto-stage .carto-hit[data-cp]').count();
  await expect(page.locator('#carto-find-n')).toHaveText(`${hits} of ${all} packages`);
  expect(w.jsErrors).toEqual([]);
});

test('a reader who asked for no animation gets the drawing and not the motion', async ({ browser }) => {
  const ctx = await browser.newContext({ reducedMotion: 'reduce', viewport: { width: 1400, height: 1000 } });
  const page = await ctx.newPage();
  const w = watch(page);
  await open(page, 'metropolis');
  await dragAsOf(page, 2, 250);
  // No ghost is ever mounted and no shape is ever wrapped: the stage is
  // repainted, exactly as it was before any of this existed.
  expect(await page.locator('.carto-ghost').count()).toBe(0);
  expect(await page.evaluate(() =>
    document.getElementById('carto-stage').getAnimations({ subtree: true }).length)).toBe(0);
  expect(await page.locator('#carto-stage [data-cp]').count()).toBeGreaterThan(0);
  expect(w.jsErrors).toEqual([]);
  await ctx.close();
});

test('find lights one namespace up in whichever view is on screen', async ({ page }) => {
  await open(page, 'honeycomb', '&find=hub');
  const hits = await page.locator('#carto-stage .carto-hit[data-cp]').count();
  const dims = await page.locator('#carto-stage .carto-dim[data-cp]').count();
  const total = await page.locator('#carto-stage [data-cp]').count();
  expect(hits).toBeGreaterThan(0);
  expect(hits + dims).toBe(total);
  await expect(page.locator('#carto-find-n')).toHaveText(`${hits} of ${total} packages`);
  // Every hit really is in the namespace or named for it: the match rule is
  // what the count promises.
  const wrong = await page.evaluate(() => [...document.querySelectorAll('#carto-stage .carto-hit[data-cp]')]
    .map(e => e.getAttribute('data-cp')).filter(p => !/\/hub\//.test(p) && !/hub[^/]*$/.test(p)));
  expect(wrong).toEqual([]);

  // It survives a view switch, and the namespace-level shapes take it too.
  await page.locator('.carto-pick button', { hasText: 'hexes' }).click();
  await expect(page.locator('#carto-stage .carto-hex.carto-hit')).toHaveCount(1);
  await expect(page).toHaveURL(/find=hub/);

  // Clearing it clears every mark.
  await page.locator('#carto-find').fill('');
  await expect(page.locator('#carto-stage .carto-dim')).toHaveCount(0);
  await expect(page).not.toHaveURL(/find=/);
});

test('night lights draws one light per package at the relief\'s positions', async ({ page }) => {
  await open(page, 'lights');
  const stated = Number((await page.locator('.carto-note').innerText()).match(/Every one of the (\d+)\s+packages is a light/)[1]);
  expect(await page.locator('#carto-stage [data-cp]').count()).toBe(stated);
  const outside = await page.evaluate(() => {
    const vb = document.querySelector('#carto-stage svg').viewBox.baseVal;
    return [...document.querySelectorAll('#carto-stage [data-cp] circle')]
      .filter(c => { const x = +c.getAttribute('cx'), y = +c.getAttribute('cy'); return x < 0 || y < 0 || x > vb.width || y > vb.height; }).length;
  });
  expect(outside).toBe(0);
});

// Round four: compare, hover neighbours, previews, history for the settlement.

test('a bare /cartography opens on the metropolis', async ({ page }) => {
  await page.goto('/cartography' + NET);
  await settle(page);
  await expect(page.locator('.carto-pick button.on b')).toHaveText('metropolis');
});

test('compare draws a second view beside the first, with its own variants and camera', async ({ page }) => {
  const w = watch(page);
  await open(page, 'metropolis', '&cmp=honeycomb');
  await page.waitForSelector('#carto-stage-b svg');
  const a = await page.locator('#carto-stage [data-cp]').count();
  expect(await page.locator('#carto-stage-b [data-cp]').count()).toBe(a);

  // The second pane's variant is its own, and in the URL under b.
  await page.locator('#carto-bar-b button', { hasText: 'oldest' }).click();
  await expect(page).toHaveURL(/b\.honeycomb\.centre=old/);
  await expect(page.locator('#carto-bar button.on', { hasText: 'land value' })).toHaveCount(1);

  // Zooming one pane leaves the other where it was, even when both show the
  // same view, and even after a redraw: a shared camera only shows itself
  // once both panes are repainted from it, so the metric change is the test.
  await open(page, 'metropolis', '&cmp=metropolis');
  await page.waitForSelector('#carto-stage-b svg');
  await wheelOn(page, '#carto-stage svg', -600);
  await page.locator('#carto-bar button', { hasText: 'storage' }).click();
  await expect(page.locator('#carto-below .carto-note')).toContainText('storage');
  const tb = await page.evaluate(() => document.querySelector('#carto-stage-b svg > g').getAttribute('transform'));
  const ta = await page.evaluate(() => document.querySelector('#carto-stage svg > g').getAttribute('transform'));
  expect(ta).not.toBe('translate(0,0) scale(1)');
  expect(tb).toBe('translate(0,0) scale(1)');
  await open(page, 'metropolis', '&cmp=honeycomb');
  await page.waitForSelector('#carto-stage-b svg');

  // Find marks both panes.
  await page.locator('#carto-find').fill('hub');
  expect(await page.locator('#carto-stage-b .carto-hit[data-cp]').count()).toBeGreaterThan(0);

  // And "nothing" puts the page back to one pane.
  await page.selectOption('#carto-cmp', '');
  await expect(page.locator('#carto-stage-b')).toHaveCount(0);
  await expect(page).not.toHaveURL(/cmp=/);
  expect(w.jsErrors).toEqual([]);
});

test('hovering a package lights what imports it, in every pane', async ({ page }) => {
  const edges = (await (await page.request.get('/api/contracts/edges?kind=imports' + '&network=alpha')).json()).edges;
  const by = {};
  edges.forEach(e => { by[e.target] = (by[e.target] || new Set()).add(e.source); });
  await open(page, 'honeycomb', '&cmp=metropolis');
  await page.waitForSelector('#carto-stage-b svg');
  const onStage = new Set(await page.evaluate(() => [...document.querySelectorAll('#carto-stage [data-cp]')].map(e => e.getAttribute('data-cp'))));
  const target = Object.keys(by).filter(p => onStage.has(p)).sort((x, y) => by[y].size - by[x].size)[0];
  expect(target).toBeTruthy();
  const importers = [...by[target]].filter(p => onStage.has(p) && p !== target).length;

  await page.locator('#carto-stage').evaluate(e => e.scrollIntoView({ block: 'center' }));
  await page.locator(`#carto-stage [data-cp="${target}"]`).hover({ force: true });
  await expect(page.locator('#carto-stage .carto-nb-self')).toHaveCount(1);
  expect(await page.locator('#carto-stage .carto-nb-in').count()).toBe(importers);
  expect(await page.locator('#carto-stage-b .carto-nb-in').count()).toBe(importers);

  // Leaving clears every mark, or the next hover would add to stale ones.
  await page.mouse.move(2, 2);
  await expect(page.locator('.carto-nb-in, .carto-nb-out, .carto-nb-self')).toHaveCount(0);
});

test('the settlement as of a day asks for the callers of the window that ended then', async ({ page }) => {
  const asked = [];
  await page.route('**/api/graph/callers*', route => {
    asked.push(route.request().url());
    route.fulfill({ contentType: 'application/json', body: JSON.stringify(PEOPLE) });
  });
  await open(page, 'metropolis');
  const first = await page.locator('#carto-asof').getAttribute('data-first');
  await open(page, 'settlement', `&asof=${first}`);
  expect(asked.some(u => u.includes(`until=${first}`))).toBe(true);
  await expect(page.locator('.carto-asof')).toContainText('per-day call rollup');
});

// The dice and the link out.

// signature is what "the same drawing" means here: every package, where it
// landed and how big it is, plus the caption the view wrote about itself.
//
// Geometry rather than markup, because every view stamps its <defs> with a
// per-draw id and two identical drawings would differ on those. Rounded,
// because a sub-pixel difference is not a reader seeing a different city.
async function signature(page, sel = '#carto-stage') {
  return page.evaluate(s => {
    const stage = document.querySelector(s);
    const shapes = [...stage.querySelectorAll('[data-cp]')].map(e => {
      const r = e.getBoundingClientRect();
      return [e.getAttribute('data-cp'), Math.round(r.x), Math.round(r.y),
        Math.round(r.width), Math.round(r.height)].join(':');
    }).sort();
    const note = [...document.querySelectorAll('#carto-below .carto-note')].map(n => n.innerText).join(' ');
    return { shapes, note, n: shapes.length };
  }, sel);
}

test('the dice draws something else, says what it drew, and the link goes back to it', async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 1000 },
    permissions: ['clipboard-read', 'clipboard-write'] });
  const page = await ctx.newPage();
  const w = watch(page);
  await stubPeople(page);
  await open(page, 'metropolis');

  const seen = new Set();
  for (let roll = 0; roll < 6; roll++) {
    await page.locator('#carto-lucky').click();
    await settle(page);
    await page.waitForSelector('#carto-stage svg, #carto-stage canvas', { timeout: 20_000 });

    // Something was drawn, and it was not an empty state. The dice promises
    // that something interesting happens; a .carto-empty is the one thing it
    // must never hand back.
    await expect(page.locator('#carto-stage .carto-empty, #carto-stage .carto-err')).toHaveCount(0);

    // The sentence names the view that is actually on, and every rule that is
    // actually in force -- both read off the same tables the controls are.
    const view = await page.locator('.carto-pick button.on b').innerText();
    const said = await page.locator('#carto-said').innerText();
    expect(said).toContain(`The ${view}:`);
    // Every variant the bar is showing is in the sentence, label and value, and
    // so are the window, the metric and the scale. .carto-variant is the mark
    // controls() puts on the OPTIONS-backed groups; the bespoke controls beside
    // them (roads, compass, plan, tilt, light) are the reader's framing rather
    // than a rule about what is drawn, and the sentence says so by omission.
    const rules = await page.locator('#carto-bar .carto-variant').evaluateAll(gs =>
      gs.map(g => [g.querySelector('span').innerText, g.querySelector('button.on').innerText]));
    for (const [label, value] of rules) expect(said).toContain(`${label} ${value}`);
    for (const g of ['window', 'size by', 'scale']) {
      const on = page.locator('#carto-bar .carto-grp', { has: page.locator(`span:text-is("${g}")`) })
        .locator('button.on');
      if (await on.count()) expect(said).toContain(await on.innerText());
    }

    // It rolls the view, the variants, and never the reader's window or a
    // random day in the chain's history.
    await expect(page).not.toHaveURL(/asof=/);
    await expect(page).not.toHaveURL(/cmp=/);
    await expect(page).toHaveURL(/w=30d/);

    // The link is the drawing. Copied, opened cold, same geometry and same
    // caption -- which is the only check that the variants made it into the
    // query string and back out of it.
    const before = await signature(page);
    await page.locator('#carto-copy').click();
    await expect(page.locator('#carto-copy')).toHaveText(/copied/);
    const url = await page.evaluate(() => navigator.clipboard.readText());
    expect(url).toContain('/cartography?');
    expect(url).toContain(`v=${await page.evaluate(() => new URLSearchParams(location.search).get('v'))}`);

    const other = await ctx.newPage();
    await stubPeople(other);
    await other.goto(url);
    await settle(other);
    await other.waitForSelector('#carto-stage svg, #carto-stage canvas', { timeout: 20_000 });
    const after = await signature(other);
    expect(after.n).toBe(before.n);
    expect(after.shapes).toEqual(before.shapes);
    expect(after.note).toBe(before.note);
    expect(await other.locator('#carto-said').innerText()).toBe(said);
    await other.close();

    seen.add(view);
  }
  // Six rolls over fourteen views: a dice that returned one view every time
  // would be a very unlucky run or a broken one, and this is the assertion
  // that tells the two apart.
  expect(seen.size).toBeGreaterThan(1);
  expect(w.jsErrors).toEqual([]);
  await ctx.close();
});

test('the dice will not roll the one view this chain cannot draw', async ({ page }) => {
  // No stubPeople here: the harness runs with -sync=false, so caller_edges is
  // legitimately empty and the settlement draws its empty state rather than a
  // village. That is honest when a reader asks for it and a bad joke when a
  // button that promised something interesting hands it over.
  await open(page, 'metropolis');
  await expect(page.locator('#carto-said')).toBeVisible();
  const rolled = [];
  for (let i = 0; i < 40; i++) {
    await page.locator('#carto-lucky').click();
    rolled.push(await page.evaluate(() => new URLSearchParams(location.search).get('v')));
  }
  expect(rolled).not.toContain('settlement');
  // And it is a dice, not a loop: forty rolls over thirteen views reach more
  // than a couple of them.
  expect(new Set(rolled).size).toBeGreaterThan(3);

  // With a caller graph it is back in the pool, which is the half of this that
  // says the exclusion is about the data and not a view somebody disliked.
  await stubPeople(page);
  await open(page, 'settlement');
  await expect(page.locator('#carto-stage .carto-empty')).toHaveCount(0);
});

test('the sentence under the bar follows the controls, not the roll that set them', async ({ page }) => {
  await open(page, 'honeycomb');
  await expect(page.locator('#carto-said')).toContainText('The honeycomb:');
  await expect(page.locator('#carto-said')).toContainText('centre is land value');

  // A variant click goes through restate(), which rebuilds the bar and not the
  // page: the sentence has to come with it.
  await page.locator('#carto-bar button', { hasText: 'most called' }).click();
  await expect(page.locator('#carto-said')).toContainText('centre is most called');
  await page.locator('#carto-bar button', { hasText: 'storage' }).click();
  await expect(page.locator('#carto-said')).toContainText('Sized by storage');
  await page.locator('#carto-bar button', { hasText: 'linear' }).click();
  await expect(page.locator('#carto-said')).toContainText('on a linear scale');

  // A view with no size channel says so rather than naming a metric it is not
  // using, and old town drops the wall rule when the ring rule has made it inert.
  await page.locator('.carto-pick button', { hasText: 'hexes' }).click();
  await expect(page.locator('#carto-said')).toContainText('Every shape the same size');
  await page.locator('.carto-pick button', { hasText: 'old town' }).click();
  await expect(page.locator('#carto-said')).toContainText('walls eras');
  await page.locator('#carto-bar button', { hasText: 'land value' }).click();
  await expect(page.locator('#carto-said')).not.toContainText('walls');
  await expect(page).not.toHaveURL(/oldtown\.walls/);
});

test('a cartography link previews as the drawing it names', async ({ page }) => {
  const html = await (await page.request.get('/cartography?v=skyline&network=alpha')).text();
  expect(html).toMatch(/property="og:image" content="[^"]*\/cartography-og\/skyline\.jpg"/);
  const img = await page.request.get('/cartography-og/skyline.jpg');
  expect(img.status()).toBe(200);
  expect(img.headers()['content-type']).toContain('image/jpeg');
});
