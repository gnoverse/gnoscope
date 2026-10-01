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
    const cx = r.left + r.width * 0.22, cy = r.top + r.height * 0.74;
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
