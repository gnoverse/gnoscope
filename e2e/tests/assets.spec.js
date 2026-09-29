import { expect, test } from '@playwright/test';

import { HUB_ROUTE } from '../harness/fixture.mjs';
import { settle, unexpected, watch } from './helpers.js';

// The assets page, over the GRC20 transfer ledger.
//
// The three rows below are the three shapes mainnet actually contains, and each
// one would render a plausible lie if the page assumed the common case.

const ASSETS = {
  network: 'alpha',
  assets: [
    {
      token: 'gno.land/r/gnoswap/gns.GNS.0000000', pkg_path: 'gno.land/r/gnoswap/gns', symbol: 'GNS',
      network: 'alpha', supply: 100393107865894, holders: 119, fungible: true,
      transfers: 1818, transfers_24h: 12, first_seen_time: '2026-09-01T00:00:00Z',
      verified: true, display_symbol: 'GNS', display_name: 'GnoSwap', decimals: 6,
    },
    {
      // GRC721 goes through the same events without an amount.
      token: 'gno.land/r/gnoswap/gnft.GNFT.0000000', pkg_path: 'gno.land/r/gnoswap/gnft', symbol: 'GNFT',
      network: 'alpha', supply: 0, holders: 0, fungible: false, transfers: 201, transfers_24h: 0,
      verified: false,
    },
    {
      // A token that emits a bare symbol instead of its realm path.
      token: 'COVID', pkg_path: '', symbol: 'COVID',
      network: 'alpha', supply: 615028450, holders: 15, fungible: true, transfers: 66, transfers_24h: 0,
      verified: false,
    },
  ],
};

async function stubAssets(page) {
  await page.route('**/api/assets*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(ASSETS) }));
}

test('an asset shows supply, holders and whether anyone vouched for it', async ({ page }) => {
  const seen = watch(page);
  await stubAssets(page);

  const response = await page.goto('/grc20');
  expect(response.status()).toBe(200);
  await settle(page);

  const list = page.locator('#grc20-list');
  await expect(list).toContainText('GNS');
  await expect(list).toContainText('100,393,107,865,894');
  await expect(list).toContainText('119');
  // The badge is the registry's claim, and the tooltip says so.
  await expect(list.locator('.badge-ok').first()).toContainText('verified');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('an asset whose transfers carry no amount says n/a, not zero', async ({ page }) => {
  const seen = watch(page);
  await stubAssets(page);

  await page.goto('/grc20');
  await settle(page);

  // A supply of 0 and 0 holders would read as "this token is empty". It means
  // the arithmetic does not apply, and the row has to distinguish the two.
  const nftRow = page.locator('#grc20-list tr', { hasText: 'GNFT' });
  await expect(nftRow).toContainText('n/a');
  // Its transfers are real and still counted.
  await expect(nftRow).toContainText('201');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

test('an asset that emits a bare symbol admits its realm is unknown', async ({ page }) => {
  const seen = watch(page);
  await stubAssets(page);

  await page.goto('/grc20');
  await settle(page);

  // Printing "COVID" under a column headed "realm" would be inventing a path.
  const row = page.locator('#grc20-list tr', { hasText: 'COVID' });
  await expect(row).toContainText('unknown');
  // Its supply is still real: only the realm is missing.
  await expect(row).toContainText('615,028,450');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

// --- prices, and the caveats that outweigh them ------------------------------
//
// This file used to assert that the page refused to show a price at all, which
// was right while GNOT had no market. It has one now, so the assertion moved:
// the page shows a price and must never show one unqualified.

// The four tiers, as the server computes them from measured slippage. GNOMIC is
// the case the whole feature exists for: a six-figure paper value over a pool
// holding under fifty dollars.
const PRICES = {
  network: 'alpha',
  pool_count: 5,
  priced_count: 2,
  asset_count: 3,
  twap_available: false,
  computed_at: '2026-09-29T17:00:00Z',
  anchor: {
    usd_per_gnot: 0.070707,
    spread_pct: 0.56,
    sources: [
      { venue: 'Kraken', pair: 'GNOT/USD', usd: 0.070707, kind: 'vwap-24h', volume_24h: 1091173 },
      { venue: 'KuCoin', pair: 'GNOT/USDT', usd: 0.07072, kind: 'last' },
    ],
  },
  quotes: [
    {
      token: 'gno.land/r/gnoswap/gns.GNS.0000000', symbol: 'GNS', tier: 'market',
      tier_label: 'market', tier_explain: 'A $10,000 trade against this pool settles within 2% of the price shown.',
      usd_per_base_unit: '0.000000017608929525', usd_per_token: 0.01760893,
      decimals: 6, decimals_known: true, fdv_usd: 1903377,
      route: [{ pool_path: 'w:g:3000', from: 'gno.land/r/gnoland/wugnot.wugnot', to: 'gno.land/r/gnoswap/gns.GNS', fee: 3000, tvl_usd: 759104 }],
      route_count: 1, route_spread_pct: 0,
      depth: [
        { notional_usd: 10, slippage_pct: 0.45, quoted: true },
        { notional_usd: 10000, slippage_pct: 0.52, quoted: true },
      ],
      warnings: [
        { code: 'twap-is-spot', short: 'no time-weighted price exists', severity: 'alarm', explain: 'Every pool reports observationCardinality = 1, so the oracle answers the spot tick.' },
        { code: 'no-onchain-oracle', short: 'USD leg is off-chain', severity: 'caution', explain: 'gno.land has no price oracle.' },
      ],
    },
    {
      token: 'COVID', symbol: 'COVID', tier: 'decorative',
      tier_label: 'decorative', tier_explain: 'There is effectively no market here.',
      usd_per_base_unit: '0.000000027734209154', usd_per_token: 0.02773421,
      decimals: 6, decimals_known: false, fdv_usd: 582003,
      route: [{ pool_path: 'w:c:3000', from: 'gno.land/r/gnoland/wugnot.wugnot', to: 'COVID', fee: 3000, tvl_usd: 47 }],
      route_count: 1, route_spread_pct: 0,
      depth: [{ notional_usd: 10, slippage_pct: 21.44, quoted: true }],
      warnings: [
        { code: 'thin-pool', short: 'thin market', severity: 'alarm', explain: 'The shallowest pool on this route holds $47.' },
        { code: 'decimals-unknown', short: 'decimals assumed', severity: 'caution', explain: 'This token exposes no Decimals().' },
      ],
    },
    {
      token: 'gno.land/r/gnoswap/gnft.GNFT.0000000', symbol: 'GNFT', tier: 'none',
      tier_label: 'no market', tier_explain: 'No GnoSwap pool routes this token to wugnot.',
      usd_per_base_unit: '', usd_per_token: 0, decimals: 6, decimals_known: false,
      route: [], route_count: 0, route_spread_pct: 0, depth: [], warnings: [],
    },
  ],
};

async function stubPrices(page, body) {
  await page.route('**/api/prices*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) }));
}

test('a price never appears without the tier that says what it is worth', async ({ page }) => {
  const seen = watch(page);
  await stubAssets(page);
  await stubPrices(page, PRICES);

  await page.goto('/grc20');
  await settle(page);

  const gns = page.locator('#grc20-list tr', { hasText: 'GNS' }).first();
  await expect(gns).toContainText('$0.0176');
  await expect(gns.locator('.px-tier-market')).toContainText('market');

  // The one that matters: a six-figure paper value backed by a $47 pool is not
  // allowed to render the same way a real price does.
  const covid = page.locator('#grc20-list tr', { hasText: 'COVID' });
  await expect(covid.locator('.px-tier-decorative')).toContainText('decorative');

  // Every tier badge carries its argument on hover, not just its label.
  const explain = await gns.locator('.px-tier-market').getAttribute('title');
  expect(explain).toContain('settles within 2%');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('a token with no pool says no market, which is not zero', async ({ page }) => {
  const seen = watch(page);
  await stubAssets(page);
  await stubPrices(page, PRICES);

  await page.goto('/grc20');
  await settle(page);

  const row = page.locator('#grc20-list tr', { hasText: 'GNFT' });
  await expect(row.locator('.px-tier-none')).toContainText('no market');
  // $0.00 would be a claim about its value. An absence of a market is not one.
  await expect(row).not.toContainText('$0.00');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

test('the page says out loud that no time-weighted price exists on this chain', async ({ page }) => {
  const seen = watch(page);
  await stubAssets(page);
  await stubPrices(page, PRICES);

  await page.goto('/grc20');
  await settle(page);

  const banner = page.locator('#grc20-price-banner');
  await expect(banner).toContainText('No manipulation-resistant price exists');
  await expect(banner).toContainText('5 liquidity pools on this entire chain');
  await expect(banner).toContainText('GNOT at $0.07');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('a network with no RPC says why the prices are missing, and stays quiet', async ({ page }) => {
  const seen = watch(page);
  await stubAssets(page);
  await stubPrices(page, { network: 'alpha', unavailable: 'no verified RPC endpoint for network alpha' });

  await page.goto('/grc20');
  await settle(page);

  // "This server could not find out" and "this chain has no market" look
  // identical as an empty column and mean opposite things.
  await expect(page.locator('#grc20-price-banner')).toContainText('no verified RPC endpoint');
  await expect(page.locator('#grc20-list tr', { hasText: 'GNS' }).first()).not.toContainText('$');

  // The column is supplementary. It must not put a red line in the console of
  // every page load on a network that has no RPC, which is most of them.
  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

// --- the per-asset page ------------------------------------------------------
//
// A token used to be a panel painted under the table: no URL, so nothing could
// be linked or bookmarked, and the back button left the list instead of
// returning to it. These four assets are the shape of the case that forces the
// page to be keyed on the event key rather than the realm: `demo/factory`
// issues two, exactly as `.../gnomi/padv3` issues six on mainnet.

const FACTORY_ASSETS = {
  network: 'alpha',
  assets: [
    {
      token: 'gno.land/r/demo/factory.AAA.0000001', pkg_path: 'gno.land/r/demo/factory', symbol: 'AAA',
      network: 'alpha', supply: 1000, holders: 3, fungible: true, transfers: 12, transfers_24h: 2,
      minted: 1200, burned: 200, mint_count: 2, burn_count: 1,
      first_block: 4200, last_block: 4900, first_seen_time: '2026-09-21T13:00:00Z', verified: false,
    },
    {
      token: 'gno.land/r/demo/factory.BBB.0000002', pkg_path: 'gno.land/r/demo/factory', symbol: 'BBB',
      network: 'alpha', supply: 55, holders: 1, fungible: true, transfers: 3, transfers_24h: 0,
      minted: 55, burned: 0, mint_count: 1, burn_count: 0,
      first_block: 4300, last_block: 4800, first_seen_time: '2026-09-21T14:00:00Z', verified: false,
    },
  ],
};

const AAA_DETAIL = {
  network: 'alpha',
  asset: FACTORY_ASSETS.assets[0],
  holders: [{ address: 'g1holder00000000000000000000000000000000', balance: 900 }],
  transfers: [{
    token: 'gno.land/r/demo/factory.AAA.0000001', from: '', to: 'g1holder00000000000000000000000000000000',
    value: 1200, tx_hash: 'TXAAA', block_height: 4200, block_time: '2026-09-21T13:00:00Z', network: 'alpha',
  }],
  supply_series: [],
  siblings: [FACTORY_ASSETS.assets[1]],
  ledger: { first_block: 4200, last_block: 4900, first_time: '2026-09-21T13:00:00Z', transfers: 15 },
  realm: {
    path: 'gno.land/r/demo/factory', creator: 'g1creator000000000000000000000000000000',
    block_height: 120, block_time: '2026-03-01T00:00:00Z', num_files: 2, call_count: 40,
  },
};

async function stubFactory(page) {
  await page.route('**/api/asset/**', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(AAA_DETAIL) }));
  await page.route('**/api/assets*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(FACTORY_ASSETS) }));
}

test('an asset has its own URL, reached by clicking it in the list', async ({ page }) => {
  const seen = watch(page);
  await stubFactory(page);

  await page.goto('/grc20');
  await settle(page);
  await page.locator('#grc20-list a', { hasText: 'AAA' }).first().click();
  await settle(page);

  // The URL is the key, not the realm: the realm issues two of these.
  expect(decodeURIComponent(new URL(page.url()).pathname))
    .toBe('/grc20/gno.land/r/demo/factory.AAA.0000001');
  await expect(page.locator('#asset-detail-content')).toContainText('gno.land/r/demo/factory.AAA.0000001');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('a deep link to an asset renders it without going through the list', async ({ page }) => {
  const seen = watch(page);
  await stubFactory(page);

  const response = await page.goto('/grc20/' + encodeURIComponent('gno.land/r/demo/factory.AAA.0000001'));
  expect(response.status()).toBe(200);
  await settle(page);

  const view = page.locator('#asset-detail-content');
  await expect(view).toContainText('AAA');
  await expect(view).toContainText('1,000');
  // Mints and burns are shown apart, because 1000 alone cannot tell "1000
  // minted" from "1200 minted and 200 burned".
  await expect(view).toContainText('1,200');
  await expect(view).toContainText('200');
  // Addresses render shortened, so this is the prefix addrLink keeps.
  await expect(view).toContainText('g1creato');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

test('an asset page names the other assets its realm issues', async ({ page }) => {
  const seen = watch(page);
  await stubFactory(page);

  await page.goto('/grc20/' + encodeURIComponent('gno.land/r/demo/factory.AAA.0000001'));
  await settle(page);

  // The whole reason the realm is not the unit: a reader who lands here from
  // the realm has to be told this is one of several.
  const view = page.locator('#asset-detail-content');
  await expect(view).toContainText('this realm issues 2 assets');
  await expect(view).toContainText('BBB');

  // And the sibling is one click away, at its own address.
  await view.locator('a', { hasText: 'BBB' }).first().click();
  await settle(page);
  expect(decodeURIComponent(new URL(page.url()).pathname))
    .toBe('/grc20/gno.land/r/demo/factory.BBB.0000002');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

test('an asset page says its figures are a window when the ledger starts after the realm', async ({ page }) => {
  const seen = watch(page);
  await stubFactory(page);

  await page.goto('/grc20/' + encodeURIComponent('gno.land/r/demo/factory.AAA.0000001'));
  await settle(page);

  // The realm was deployed at block 120 and the transfer ledger starts at
  // 4200, so everything in between was never recorded. Printing the supply as
  // a total would be presenting a window as a fact.
  await expect(page.locator('#asset-detail-content')).toContainText('window');
  await expect(page.locator('#asset-detail-content')).toContainText('4,200');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

test('the realm page lists every asset the realm issues, not one', async ({ page }) => {
  const seen = watch(page);
  await stubFactory(page);

  await page.goto(`/realm/${HUB_ROUTE}?network=alpha`);
  await settle(page);

  // "the token of this realm" names nothing when the realm is a factory, so
  // the realm page lists them and each one routes to its own page.
  const info = page.locator('#tab-info');
  await expect(info).toContainText('grc20 assets issued by this realm (2)');
  await expect(info).toContainText('AAA');
  await expect(info).toContainText('BBB');

  await info.locator('a', { hasText: 'BBB' }).first().click();
  await settle(page);
  expect(decodeURIComponent(new URL(page.url()).pathname))
    .toBe('/grc20/gno.land/r/demo/factory.BBB.0000002');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});
