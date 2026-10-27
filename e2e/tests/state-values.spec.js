import { expect, test } from '@playwright/test';

import { HUB, HUB_ROUTE } from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

// The state tab's reading of a realm: struct fields by name, map entries,
// a link to any value, and the branches a deadline-bound read did not reach.
//
// The harness has no RPC (see EXPECTED_FAILURES), so /api/state is stubbed
// with a payload shaped like the one mainnet's bubblerumble6 returns:
// measured 2026-10-27, its pools rendered as [0]..[33] tuples, its maps as
// "empty" while holding 37 KB, and nothing on the page had a URL.

const PKG = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';
const POOL = HUB + '.Pool';
const HOSTILE = '<img src=x onerror=alert(1)>"].x';

const prim = (name, type, value, extra) => Object.assign({ name, type, kind: 'primitive', value }, extra || {});
const pool = (i, name) => ({
  name: '[' + i + ']', type: 'Pool', type_id: POOL, kind: 'struct', object_id: PKG + ':' + (100 + i), size: 900,
  children: [
    prim('ID', 'int64', String(i + 1)),
    prim('Name', 'string', name),
    prim('Creator', 'address', 'g1manfred47kzduec920z88wfr64ylksmdcedlf5'),
    {
      name: 'CreatedAt', type: 'Time', type_id: 'time.Time', kind: 'struct', value: '2026-10-02T18:02:26Z',
      children: [prim('sec', 'int64', '63926560946'), prim('nsec', 'int32', '0')],
    },
  ],
});

function statePayload() {
  const pools = [pool(0, '🏁 speedrun'), pool(1, 'second')];
  // A struct whose type declaration the read did not get to: by position,
  // and saying so.
  pools.push({
    name: '[2]', type: 'Pool', type_id: POOL, kind: 'struct', positional: 'unreached',
    children: [prim('[0]', 'int64', '3'), prim('[1]', 'string', 'third')],
  });
  // The rest were not reached at all.
  for (let i = 3; i < 14; i++) {
    pools.push({ name: '[' + i + ']', type: 'Pool', type_id: POOL, kind: 'ref', object_id: PKG + ':' + (100 + i), unresolved: 'unreached' });
  }
  return {
    path: HUB, network: 'alpha', partial: true, can_retry_full: true,
    partial_reason: 'the fetch deadline passed before every reference resolved',
    objects: 9, node_count: 60,
    nodes: [
      { name: 'pools', type: '[]*Pool', kind: 'slice', value: 'len 14', children: pools },
      {
        name: 'players', type: 'map[address]*Player', kind: 'map', value: 'len 2', size: 37474, object_id: PKG + ':13',
        children: [
          { name: 'g1manfred47kzduec920z88wfr64ylksmdcedlf5', type: 'Player', kind: 'struct', children: [prim('Flag', 'string', 'FR')] },
          prim(HOSTILE, 'string', HOSTILE),
        ],
      },
      {
        name: 'commits', type: '[]string', kind: 'slice', value: 'len 1',
        children: [prim('[0]', 'string', 'I\u000f��', { lossy: 2 })],
      },
      {
        name: 'posts', type: 'Tree', kind: 'map', value: '1 entry, partially loaded',
        children: [
          prim('a', 'string', 'alpha'),
          { kind: 'ref', subtree: true, type: 'Node', type_id: 'gno.land/p/nt/avl/v0.Node', object_id: PKG + ':90', unresolved: 'unreached' },
        ],
      },
    ],
  };
}

// readObject answers a ?object= read the way the endpoint does: one node per
// object, in order.
function readObject(oid) {
  if (oid === PKG + ':90') {
    return { kind: 'map', value: '1 entry', children: [prim('b', 'string', 'beta')] };
  }
  const i = Number(oid.split(':')[1]) - 100;
  return pool(i, 'pool ' + i);
}

async function stubState(page) {
  const reads = [];
  await page.route('**/api/state/**', async (route) => {
    const url = new URL(route.request().url());
    const oids = url.searchParams.getAll('object');
    let body;
    if (oids.length) {
      reads.push(oids);
      body = { path: HUB, network: 'alpha', object_ids: oids, nodes: oids.map(readObject), objects: oids.length, node_count: 5 };
    } else {
      body = statePayload();
    }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  });
  return reads;
}

const row = (page, p) => page.locator(`#tab-state [data-state-path=${JSON.stringify(p)}]`);
const head = (page, p) => row(page, p).locator(':scope > .state-head');

test('a struct reads by field name, a map by its entries, and what is not known says so', async ({ page }) => {
  const seen = watch(page);
  await stubState(page);
  await page.goto(`/realm/${HUB_ROUTE}?network=alpha&tab=state`);
  await settle(page);

  // pools is open at depth 0; open pools[0] and read a field by its name.
  await head(page, 'pools[0]').click();
  await expect(head(page, 'pools[0].Name')).toContainText('speedrun');
  await expect(head(page, 'pools[0].CreatedAt')).toContainText('2026-10-02T18:02:26Z');

  // A map is its entries, not "empty". An address key is a link.
  await expect(head(page, 'players')).toContainText('2 entries');
  await expect(head(page, 'players')).not.toContainText('empty');
  await expect(head(page, 'players["g1manfred47kzduec920z88wfr64ylksmdcedlf5"]').locator('a[data-hl]')).toBeVisible();

  // A hostile key renders as text, never as markup.
  await expect(head(page, `players[${JSON.stringify(HOSTILE)}]`)).toContainText('<img');
  expect(await page.locator('#tab-state img').count()).toBe(0);

  // The row says which kind of name it is showing, and why.
  await expect(head(page, 'pools[2]').locator('.state-badge')).toHaveText('fields by position');
  await expect(head(page, 'pools[2]').locator('.state-badge')).toHaveAttribute('title', /declaration of .*Pool was fetched/);

  // Binary in a string is escaped and flagged rather than shown as mojibake.
  await expect(head(page, 'commits[0]').locator('.state-badge')).toHaveText('bytes lost');
  await expect(head(page, 'commits[0]').locator('.state-escaped')).toContainText('\\u000f');

  // An unread branch says so, not "ref 0123…:85".
  await expect(head(page, 'pools[3]')).toContainText('not read: the read ended before reaching it');
  await expect(head(page, 'pools[3]')).not.toContainText(PKG);

  expect(unexpected(seen.failedRequests)).toEqual([]);
  expect(seen.jsErrors).toEqual([]);
});

test('a value has a link, and the link survives a reload', async ({ page }) => {
  const seen = watch(page);
  await stubState(page);
  await page.goto(`/realm/${HUB_ROUTE}?network=alpha&tab=state`);
  await settle(page);

  await head(page, 'pools[0]').click();
  const link = head(page, 'pools[0].Name').locator('.state-link');
  await expect(link).toHaveAttribute('href', /[?&]value=pools%5B0%5D\.Name/);
  await head(page, 'pools[0].Name').hover();
  await link.click();
  await expect(page).toHaveURL(/[?&]value=pools%5B0%5D\.Name/);
  await expect(page).toHaveURL(/[?&]tab=state/);

  // A fresh load lands on the value: every ancestor open, the row marked and
  // on screen.
  await page.reload();
  await settle(page);
  await expect(row(page, 'pools[0].Name')).toHaveClass(/state-row-on/);
  await expect(head(page, 'pools[0].Name')).toBeInViewport();

  // A map key that holds `"`, `]` and `.` round-trips through the quoted step.
  await page.goto(`/realm/${HUB_ROUTE}?network=alpha&tab=state&value=` +
    encodeURIComponent(`players[${JSON.stringify(HOSTILE)}]`));
  await settle(page);
  await expect(row(page, `players[${JSON.stringify(HOSTILE)}]`)).toHaveClass(/state-row-on/);

  // Leaving the tab drops the value from the URL: it means nothing on info.
  await page.locator('#realm-tabs .tab[data-tab="info"]').click();
  await expect(page).not.toHaveURL(/[?&]value=/);

  expect(seen.jsErrors).toEqual([]);
});

test('a link the state no longer matches says where it stopped', async ({ page }) => {
  const seen = watch(page);
  await stubState(page);
  await page.goto(`/realm/${HUB_ROUTE}?network=alpha&tab=state&value=` + encodeURIComponent('pools[99].Name'));
  await settle(page);
  await expect(page.locator('#tab-state .state-notice')).toContainText('the linked value pools[99].Name is not here');
  await expect(row(page, 'pools')).toHaveClass(/state-row-on/);

  // A field link into a struct this read could only number names the reason.
  await page.goto(`/realm/${HUB_ROUTE}?network=alpha&tab=state&value=` + encodeURIComponent('pools[2].Name'));
  await settle(page);
  await expect(page.locator('#tab-state .state-notice')).toContainText('could not name the fields of pools[2]');

  expect(seen.jsErrors).toEqual([]);
});

test('a link into a branch the read did not reach reads that branch and lands', async ({ page }) => {
  const seen = watch(page);
  const reads = await stubState(page);
  await page.goto(`/realm/${HUB_ROUTE}?network=alpha&tab=state&value=` + encodeURIComponent('pools[5].Name'));
  await settle(page);
  await expect(row(page, 'pools[5].Name')).toHaveClass(/state-row-on/);
  await expect(head(page, 'pools[5].Name')).toContainText('pool 5');
  expect(reads).toEqual([[PKG + ':105']]);
  expect(seen.jsErrors).toEqual([]);
});

test('an unread branch can be read on its own, one or all at once', async ({ page }) => {
  const seen = watch(page);
  const reads = await stubState(page);
  await page.goto(`/realm/${HUB_ROUTE}?network=alpha&tab=state`);
  await settle(page);

  // One branch.
  await head(page, 'pools[3]').getByRole('button', { name: 'read it' }).click();
  await expect(head(page, 'pools[3].Name')).toContainText('pool 3');
  expect(reads[0]).toEqual([PKG + ':103']);

  // Every remaining one, in one request.
  const all = head(page, 'pools').locator('.state-read-all');
  await expect(all).toHaveText('read 10 unread');
  await all.click();
  await expect(head(page, 'pools[13]')).not.toContainText('not read');
  expect(reads[1]).toHaveLength(10);
  await expect(all).toBeHidden();

  // A part of a large map: its entries take its place, in key order.
  const posts = row(page, 'posts');
  await posts.locator('button', { hasText: 'read it' }).click();
  await expect(row(page, 'posts["b"]')).toBeVisible();
  const keys = await posts.locator(':scope > .state-body > .state-row').evaluateAll(rs => rs.map(r => r.getAttribute('data-state-path')));
  expect(keys).toEqual(['posts["a"]', 'posts["b"]']);

  expect(seen.jsErrors).toEqual([]);
});

// A filter is page state, not URL state, so a link never lands in a filtered
// list; what has to hold is that filtering leaves every row's link alone.
test('a long container can be filtered without breaking its links', async ({ page }) => {
  const seen = watch(page);
  await stubState(page);
  await page.goto(`/realm/${HUB_ROUTE}?network=alpha&tab=state`);
  await settle(page);

  const filter = row(page, 'pools').locator('.state-filter-input');
  await filter.fill('speedrun');
  await expect(row(page, 'pools').locator('.state-filter')).toContainText('showing 1 of 14');
  await expect(row(page, 'pools[0]')).toBeVisible();
  await expect(row(page, 'pools[1]')).toBeHidden();

  await head(page, 'pools[0]').click();
  await expect(head(page, 'pools[0].Name').locator('.state-link')).toHaveAttribute('href', /value=pools%5B0%5D\.Name/);

  await filter.fill('');
  await expect(row(page, 'pools[1]')).toBeVisible();

  expect(seen.jsErrors).toEqual([]);
});
