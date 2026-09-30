import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

// The /defi section's readability, which is a correctness problem wearing a
// layout problem's clothes.
//
// moul's report on 2026-09-30, against gnoscope.com: "table unreadable ...
// missing ellipsis", "most things in supply are just repeating themselves, same
// value 2 times", "something strange with conversion /1000000", "missing graphs
// in all defi pages". Every row below is one of those, reduced to the shape that
// produced it on mainnet.

// A 50-character "symbol" is not hypothetical: TokenKeyParts used to split the
// event key from the right, so `.../bubble.BUBBLE`, a key with no trailing id,
// parsed as realm "gno" and symbol "land/r/g1leu8d2…/bubble". The split is fixed
// in pkg/store; this keeps the page from being at its mercy.
const LONG_SYMBOL = 'land/r/g1leu8d2vsplhehcfkjg50mwgdpxdkt8tztu95wr/bubble';
// What the cell is allowed to show: the first 17 characters and an ellipsis.
// Matching the row on this rather than on the symbol is itself the assertion:
// a row still findable by its full 50-character text is a row still printing it.
const LONG_SHOWN = LONG_SYMBOL.slice(0, 17);

const ASSETS = {
  network: 'alpha',
  assets: [
    {
      token: 'ugnot', pkg_path: '', symbol: 'GNOT', kind: 'native',
      network: 'alpha', supply: 1333000221686563, locked: 1108981410871186, supply_known: true,
      holders: 1240, holders_basis: 'swept', holders_swept: 4820,
      transfers: 482119, transfers_basis: 'banksend', transfers_24h: 900,
      fungible: true, verified: true, display_symbol: 'GNOT', display_name: 'gno.land', decimals: 6,
    },
    {
      // Live on mainnet 2026-09-30: a supply that has gone under zero, because
      // the ledger window opened after these tokens were minted.
      token: 'gno.land/r/gnoland/wugnot.wugnot.0000000', pkg_path: 'gno.land/r/gnoland/wugnot',
      symbol: 'wugnot', kind: 'grc20', holders_basis: 'replayed', transfers_basis: 'events',
      network: 'alpha', supply: -2377176640553, supply_known: true, holders: 129, fungible: true,
      transfers: 7942, transfers_24h: 673, verified: true,
      display_symbol: 'WUGNOT', display_name: 'Wrapped GNOT', decimals: 6,
    },
    {
      token: 'gno.land/r/g1leu8d2vsplhehcfkjg50mwgdpxdkt8tztu95wr/bubble.BUBBLE',
      pkg_path: 'gno', symbol: LONG_SYMBOL, kind: 'grc20',
      holders_basis: 'replayed', transfers_basis: 'events', supply_known: true,
      network: 'alpha', supply: 10012000000000, holders: 17, fungible: true,
      transfers: 55, transfers_24h: 8, verified: false,
    },
    {
      token: 'gno.land/r/gnoswap/gnft.GNFT.0000000', pkg_path: 'gno.land/r/gnoswap/gnft',
      symbol: 'GNFT', kind: 'grc721', holders_basis: 'replayed', transfers_basis: 'events',
      supply_known: true, network: 'alpha', supply: 0, holders: 76, fungible: false,
      transfers: 1637, transfers_24h: 154, verified: false,
    },
  ],
};

// Thirty days, so every window control has something to draw.
const ACTIVITY = {
  network: 'alpha', days: 30,
  points: Array.from({ length: 30 }, (_, i) => ({
    time: `2026-09-${String(i + 1).padStart(2, '0')}`,
    native_transfers: 100 + i,
    grc20_transfers: 50 + i * 2,
    grc721_transfers: i,
    native_volume: (1000 + i) * 1e6,
  })),
};

async function stub(page) {
  await page.route('**/api/assets/activity*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ACTIVITY) }));
  await page.route('**/api/assets?**', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ASSETS) }));
  await page.route('**/api/assets', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ASSETS) }));
}

// Asks Chart.js what it drew, rather than asserting a canvas exists: a canvas
// has no accessible content, and `Chart` undefined means nothing was drawn at
// all, which is a real failure and not something to skip past.
async function charts(page) {
  return page.evaluate(() => {
    if (typeof Chart === 'undefined') return null;
    return [...document.querySelectorAll('.view:not([style*="none"]) canvas')]
      .map(c => Chart.getChart(c))
      .filter(Boolean)
      .map(chart => ({
        title: chart.options.plugins.title.text,
        datasets: chart.data.datasets.map(d => d.label),
      }));
  });
}

test('one hostile symbol cannot widen the asset table past its container', async ({ page }) => {
  const seen = watch(page);
  await stub(page);

  await page.goto('/defi');
  await settle(page);

  await expect(page.locator('#asset-list tr', { hasText: LONG_SYMBOL })).toHaveCount(0);
  const cell = page.locator('#asset-list tr', { hasText: LONG_SHOWN }).first().locator('td').first();
  const text = (await cell.innerText()).split('\n')[0];
  // Ellipsised, not printed whole. The full value is still reachable.
  expect(text.length, `symbol cell reads ${JSON.stringify(text)}`).toBeLessThanOrEqual(19);
  expect(text).toContain('…');
  expect(await cell.locator('a').first().getAttribute('title')).toBe(LONG_SYMBOL);

  // And the consequence that made the page unreadable: the table used to be
  // 1419px inside a 1160px column, so every row scrolled sideways.
  const fits = await page.evaluate(() => {
    const t = document.getElementById('asset-list');
    return t.scrollWidth <= t.parentElement.clientWidth;
  });
  expect(fits, 'the asset table overflows its container').toBe(true);

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('the supply column does not print the same figure twice', async ({ page }) => {
  const seen = watch(page);
  await stub(page);

  await page.goto('/defi');
  await settle(page);

  const supply = page.locator('#asset-list tr', { hasText: 'GNOT' }).first().locator('td').nth(4);
  const text = await supply.innerText();

  // The exact base units lead, because that is what the chain holds.
  expect(text).toContain('1,333,000,221,686,563');
  // The whole-token line is compact. Printed at full precision it was
  // "1 333 000 221,686563", the same sixteen digits, one glyph apart.
  expect(text).toMatch(/≈\s*1\.33B GNOT/);
  const scaled = text.split('\n').find(l => l.includes('≈')) || '';
  expect(scaled.replace(/[^0-9]/g, ''), 'the scaled line repeats the raw digits')
    .not.toBe('1333000221686563');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

test('a supply that has gone negative says it is a window, not a balance', async ({ page }) => {
  const seen = watch(page);
  await stub(page);

  await page.goto('/defi');
  await settle(page);

  const supply = page.locator('#asset-list tr', { hasText: 'WUGNOT' }).first().locator('td').nth(4);
  await expect(supply).toContainText('-2,377,176,640,553');
  // Marked, not hidden: the number is what this index computed, and suppressing
  // it would leave no way to tell a partial ledger from a real zero.
  const mark = supply.locator('.basis-mark').first();
  await expect(mark).toBeVisible();
  expect(await mark.getAttribute('title')).toContain('NOT a supply');

  // A positive supply carries no such mark.
  const ok = page.locator('#asset-list tr', { hasText: LONG_SHOWN }).first().locator('td').nth(4);
  await expect(ok.locator('.basis-mark')).toHaveCount(0);

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

// The four section pages shipped with tiles and a table and no chart at all:
// /defi drew one, and a reader who clicked through to the kind they cared about
// lost it.
for (const [path, kindLabel] of [['/coins', 'native'], ['/grc20', 'grc20'], ['/nfts', 'nft']]) {
  test(`${path} draws its own activity chart`, async ({ page }) => {
    const seen = watch(page);
    await stub(page);

    await page.goto(path);
    await settle(page);

    const drawn = await charts(page);
    expect(drawn, 'Chart.js did not load, so nothing was drawn').not.toBeNull();
    expect(drawn.length, `${path} drew no chart`).toBeGreaterThan(0);

    // Its own kind, and only its own: three series on /nfts would be the /defi
    // home's chart wearing a section page's headline.
    const activity = drawn.find(c => /transfers per day/.test(c.title || ''));
    expect(activity, `${path} has no transfers chart`).toBeTruthy();
    expect(activity.datasets).toEqual([kindLabel]);

    expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
    expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
  });
}

// The fourth page of the section, and the one where the absence of a picture
// was itself the missing answer: "the top address holds most of it" and "the
// top fifty hold it evenly" are the same table.
const HOLDERS = {
  network: 'alpha', priced_assets: 3, total_assets: 4, native_included: true,
  native_swept: 4820, truncated: false,
  holders: [
    { address: 'g1dexaf6aqkkyr9yfy9d5up69lsn7ra80af34g5v', assets: 4, priced_assets: 4, usd_value: 291461.98, largest: { token: 'gno.land/r/gnoswap/gns.GNS.0000000', symbol: 'GNS', kind: 'grc20', balance: 15682162213155, amount: 15682162.2, usd_value: 286571.35, priced: true, fungible: true } },
    { address: 'g1uv80ae4csts8k6w78prvy8kp7aqe3rtl66k2dq', assets: 1, priced_assets: 1, usd_value: 56020.01, largest: { token: 'gno.land/r/gnoswap/gns.GNS.0000000', symbol: 'GNS', kind: 'grc20', balance: 3065606172057, amount: 3065606.17, usd_value: 56020.01, priced: true, fungible: true } },
    { address: 'g1em9s40nfrwd2aqn9ypjv7d9x9z9c8uk5uxrza9', assets: 5, priced_assets: 5, usd_value: 30418.57, largest: { token: 'gno.land/r/gnoswap/gns.GNS.0000000', symbol: 'GNS', kind: 'grc20', balance: 1631928883269, amount: 1631928.88, usd_value: 29821.40, priced: true, fungible: true } },
    { address: 'g1w62226g8vrd7m5jhrpp4l5th7cf2dxrh2z6g4z', assets: 2, priced_assets: 2, usd_value: 9120.00, largest: { token: 'gno.land/r/gnoland/wugnot.wugnot.0000000', symbol: 'wugnot', kind: 'grc20', balance: 8068805812, amount: 8068.80, usd_value: 8000.00, priced: true, fungible: true } },
    { address: 'g1z2qnvwl8pmxhkm9yvsc8p6e0xr9v8h5skk9n2p', assets: 1, priced_assets: 1, usd_value: 412.50, largest: { token: 'gno.land/r/gnoswap/gns.GNS.0000000', symbol: 'GNS', kind: 'grc20', balance: 22000000000, amount: 22000, usd_value: 412.50, priced: true, fungible: true } },
  ],
};

test('/holders draws the concentration curve, over what it can actually value', async ({ page }) => {
  const seen = watch(page);
  await stub(page);
  await page.route('**/api/holders*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(HOLDERS) }));

  await page.goto('/holders');
  await settle(page);

  const drawn = await charts(page);
  expect(drawn, 'Chart.js did not load, so nothing was drawn').not.toBeNull();
  const curve = drawn.find(c => /cumulative share/.test(c.title || ''));
  expect(curve, '/holders drew no concentration curve').toBeTruthy();

  // It has to end at 100%: a cumulative share that stops short is a sum over
  // something other than what it divided by.
  const last = await page.evaluate(() => {
    const c = [...document.querySelectorAll('.view:not([style*="none"]) canvas')]
      .map(x => Chart.getChart(x)).filter(Boolean)
      .find(x => /cumulative share/.test(x.options.plugins.title.text || ''));
    const d = c.data.datasets[0].data;
    return { first: d[0], last: d[d.length - 1], n: d.length };
  });
  expect(last.n).toBe(HOLDERS.holders.length);
  expect(last.last).toBeCloseTo(100, 6);
  // The richest address holds 75% of this ranking, so the curve must start
  // steep rather than at 1/5th of the way up.
  expect(last.first).toBeGreaterThan(70);

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});
