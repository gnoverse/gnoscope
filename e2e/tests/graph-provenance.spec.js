// Graph provenance, and the caller graph's drill-down.
//
// Every graph payload now says the block it was computed at and the live tip
// beside it. What a Go test cannot see is whether that reaches a reader: the
// JSON can carry the height while the drawing under it says nothing. These
// stub the graph endpoints with a fixed, stamped payload, because the harness
// runs with -sync=false and no RPC: no stored blocks, no edge rollups and no
// chain id, so the real server legitimately has nothing to stamp. The stamp
// itself is pinned by pkg/httpapi/api_graph_test.go.
import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

const STAMP = {
  network: 'alpha',
  chain_id: 'alpha-1',
  indexed_height: 4200,
  indexed_time: new Date(Date.now() - 120_000).toISOString(),
  live_tip_height: 4212,
  live_tip_checked_at: new Date(Date.now() - 4_000).toISOString(),
};

const CALLERS = {
  nodes: [
    { id: 'g1walker000000000000000000000000000001', type: 'caller', calls: 129 },
    { id: 'g1walker000000000000000000000000000002', type: 'caller', calls: 44 },
    { id: 'gno.land/r/hub/core', type: 'realm', calls: 120 },
    { id: 'gno.land/r/consumer00/app', type: 'realm', calls: 53 },
  ],
  edges: [
    { caller: 'g1walker000000000000000000000000000001', pkg_path: 'gno.land/r/hub/core', calls: 120 },
    { caller: 'g1walker000000000000000000000000000001', pkg_path: 'gno.land/r/consumer00/app', calls: 9 },
    { caller: 'g1walker000000000000000000000000000002', pkg_path: 'gno.land/r/consumer00/app', calls: 44 },
  ],
  ...STAMP,
  days: 7, window: '7d', granularity: 'hourly', until: '2026-10-26', top_n: 300, min_calls: 0, ego: '',
};

// The neighbourhood of one realm, cut at topN: two callers drawn, three not.
const EGO = {
  nodes: [
    { id: 'gno.land/r/consumer00/app', type: 'realm', calls: 60 },
    { id: 'g1walker000000000000000000000000000002', type: 'caller', calls: 44 },
    { id: 'g1walker000000000000000000000000000001', type: 'caller', calls: 9 },
  ],
  edges: [
    { caller: 'g1walker000000000000000000000000000002', pkg_path: 'gno.land/r/consumer00/app', calls: 44 },
    { caller: 'g1walker000000000000000000000000000001', pkg_path: 'gno.land/r/consumer00/app', calls: 9 },
  ],
  ...STAMP,
  days: 7, window: '7d', granularity: 'hourly', until: '2026-10-26', top_n: 2, min_calls: 0,
  ego: 'gno.land/r/consumer00/app', dropped: 3,
};

async function stubCallers(page, asked) {
  await page.route('**/api/graph/callers*', route => {
    const url = route.request().url();
    asked.push(url);
    const body = url.includes('ego=') ? EGO : CALLERS;
    route.fulfill({ contentType: 'application/json', body: JSON.stringify(body) });
  });
}

// The canvas the caller graph draws into, and its echarts instance. The
// overview is WebGL, where a pointer click lands on a point mesh with no DOM
// of its own, so the click is delivered through the instance: what this pins
// is the handler, the request it makes and the controls it leaves, and the
// handler's acceptance of a hit with no dataType, which is what graphGL sends.
async function clickNode(page, id, dataType) {
  await page.evaluate(([nodeId, type]) => {
    const host = document.getElementById('dash-chart-caller-graph');
    const inst = window.echarts.getInstanceByDom(host);
    inst.trigger('click', { dataType: type, data: { id: nodeId, name: nodeId } });
  }, [id, dataType]);
}

async function openCallerGraph(page) {
  await page.goto('/dashboards?network=alpha&section=network');
  await settle(page);
  await page.waitForSelector('#dash-chart-caller-graph canvas', { timeout: 30_000 });
}

test('the caller graph says which block it was computed at', async ({ page }) => {
  const w = watch(page);
  await stubCallers(page, []);
  await openCallerGraph(page);

  const prov = page.locator('#dash-prov-caller-graph');
  await expect(prov).toContainText('computed at block');
  await expect(prov).toContainText('4,200');
  await expect(prov).toContainText('live tip 12 blocks ahead');
  // The height is drawn with its age, like every other height on the site.
  await expect(prov.locator('[data-age]')).toHaveCount(1);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the caller graph drills into a node and comes back', async ({ page }) => {
  const w = watch(page);
  const asked = [];
  await stubCallers(page, asked);
  await openCallerGraph(page);

  const card = page.locator('.dash-card', { has: page.locator('#dash-chart-caller-graph') });
  await expect(card.locator('.info-pop')).not.toContainText('no ego drill-down');
  await expect(page.locator('#dash-controls-caller-graph button')).toHaveCount(0);

  // graphGL reports a point hit without a dataType.
  await clickNode(page, 'gno.land/r/consumer00/app', undefined);
  await expect(page.locator('#dash-controls-caller-graph button')).toHaveText(/back to overview/);
  const egoURL = asked.find(u => u.includes('ego='));
  expect(egoURL).toBeTruthy();
  expect(decodeURIComponent(egoURL)).toContain('ego=gno.land/r/consumer00/app');
  expect(egoURL).not.toContain('topN=');

  // The cut is said under the drawing, not left to look complete.
  await expect(page.locator('#dash-prov-caller-graph')).toContainText('3 more past the top 2 not drawn');

  // An edge is not a node to drill into.
  const before = asked.length;
  await clickNode(page, 'ignored', 'edge');
  expect(asked.length).toBe(before);

  await page.locator('#dash-controls-caller-graph button').click();
  await expect(page.locator('#dash-controls-caller-graph button')).toHaveCount(0);
  expect(asked[asked.length - 1]).toContain('topN=300');

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the transfer graph says which block it was computed at', async ({ page }) => {
  const w = watch(page);
  await page.route('**/api/graph/transfers*', route => route.fulfill({
    contentType: 'application/json',
    body: JSON.stringify({
      nodes: [{ id: 'g1walker000000000000000000000000000001', volume: 10 },
        { id: 'g1walker000000000000000000000000000002', volume: 10 }],
      edges: [{ from: 'g1walker000000000000000000000000000001', to: 'g1walker000000000000000000000000000002', value: 10, tx_count: 1 }],
      ...STAMP, live_tip_height: 4200,
      days: 7, window: '7d', granularity: 'hourly', top_n: 100, min_value: 0, ego: '',
    }),
  }));
  await page.goto('/dashboards?network=alpha&section=network');
  await settle(page);

  const prov = page.locator('#dash-prov-transfer-graph');
  await expect(prov).toContainText('computed at block');
  await expect(prov).toContainText('at the live tip');

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

// The cartography caption already names the window, the metric and the day;
// the block is the fourth thing, and it is read from the map payload.
test('the cartography caption names the block the drawing is true at', async ({ page }) => {
  const w = watch(page);
  await page.route('**/api/contracts/map*', async route => {
    const res = await route.fetch();
    const body = await res.json();
    route.fulfill({ response: res, json: { ...body, ...STAMP } });
  });
  await page.goto('/cartography?v=metropolis&network=alpha');
  await settle(page);
  await page.waitForSelector('#carto-stage svg, #carto-stage canvas', { timeout: 20_000 });

  const prov = page.locator('#carto-below .carto-prov');
  await expect(prov).toHaveCount(1);
  await expect(prov).toContainText('computed at block 4,200');
  await expect(prov).toContainText('live tip 12 blocks ahead');

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

// Absent is absent: a payload the server could not stamp draws no line at all
// rather than "block 0".
test('an unstamped map draws no provenance line', async ({ page }) => {
  await page.goto('/cartography?v=metropolis&network=alpha');
  await settle(page);
  await page.waitForSelector('#carto-stage svg, #carto-stage canvas', { timeout: 20_000 });
  await expect(page.locator('#carto-below .carto-prov')).toHaveCount(0);
});

// The real server, unstubbed: the echo is there whatever the harness can or
// cannot stamp, and a radius outside 1..5 is refused with the range.
test('the graph endpoints echo what they read and refuse a bad radius', async ({ page }) => {
  const res = await page.request.get('/api/graph/transfers?network=alpha&ego=g1nobody&hops=2');
  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(Object.keys(body).slice(0, 2)).toEqual(['nodes', 'edges']);
  expect(body).toMatchObject({ network: 'alpha', ego: 'g1nobody', hops: 2, dropped: 0, top_n: 100, days: 30 });

  const bad = await page.request.get('/api/graph/transfers?network=alpha&ego=g1nobody&hops=6');
  expect(bad.status()).toBe(400);
  expect(await bad.text()).toContain('1 to 5');
});
