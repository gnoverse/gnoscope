import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

// One GRC721 item, #405. The item is read live from the realm and the harness
// has no RPC, so /api/nft is stubbed with what mainnet answered for GNFT 1 on
// 2026-11-05: a staked, in-range wugnot/GNS position.

const GNFT_1 = {
  network: 'alpha', collection: 'gno.land/r/gnoswap/gnft', id: '1',
  name: 'GNOSWAP NFT', symbol: 'GNFT',
  owner: 'g1gdg8jjm56xnk4jckj3x9yuc3fp5zh2ekaq48ty',
  custodian: 'g1q6d4ns7zkr492rgl0pcgf5ajaf2dlz0nnptky3',
  token_uri: 'data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciLz4=',
  kind: 'gnoswap-position',
  position: {
    pool: 'gno.land/r/gnoland/wugnot.wugnot:gno.land/r/gnoswap/gns.GNS:3000',
    token0: 'gno.land/r/gnoland/wugnot.wugnot', token1: 'gno.land/r/gnoswap/gns.GNS',
    token0_symbol: 'WUGNOT', token1_symbol: 'GNS', token0_decimals: 6, token1_decimals: 6,
    fee: 3000, tick_lower: -9180, tick_upper: 36900, tick_current: 14092,
    price_lower: 0.3993352045798068, price_upper: 40.03745985697331, price_current: 4.092391609255843,
    price_scale: 1, liquidity: '584708087353', in_range: true, burned: false,
    amount0: 196619569577, amount1: 813384024312, unclaimed_fee0: '57269', unclaimed_fee1: '1091920',
    staked: true, claimable_reward: 859862148,
  },
  read_at: '2026-11-05T08:34:59Z',
};

test('a staked gnoswap position reads as a position, owned by its depositor', async ({ page }) => {
  const seen = watch(page);
  let asked = '';
  await page.route('**/api/nft/**', route => {
    asked = route.request().url();
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(GNFT_1) });
  });

  const response = await page.goto('/nft/gno.land/r/gnoswap/gnft/1');
  expect(response.status()).toBe(200);
  await settle(page);

  expect(asked).toContain('/api/nft/gno.land/r/gnoswap/gnft?id=1');
  const view = page.locator('#nft-item-content');
  await expect(view).toContainText('GNFT #1');
  await expect(view).toContainText('in range');
  await expect(view).toContainText('staked');
  await expect(view).toContainText('WUGNOT / GNS');
  await expect(view).toContainText('0.3%');
  await expect(view).toContainText('now 4.0924');
  // The depositor is the owner; the staker realm is named as the custodian
  // and not as the owner, which is the trap #405 describes.
  await expect(view.locator('tr', { hasText: 'depositor' })).toContainText('g1gdg8jj');
  await expect(view.locator('tr', { hasText: 'held by' })).toContainText('custody');
  await expect(view.locator('img')).toHaveCount(1);
  await expect(page.locator('#nav-nfts')).toHaveClass(/active/);

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('an out-of-range position says it earns nothing', async ({ page }) => {
  const seen = watch(page);
  const out = structuredClone(GNFT_1);
  Object.assign(out.position, { in_range: false, staked: false, tick_current: -14627, price_current: 0.2316 });
  delete out.custodian;
  await page.route('**/api/nft/**', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(out) }));

  await page.goto('/nft/gno.land/r/gnoswap/gnft/1');
  await settle(page);

  const view = page.locator('#nft-item-content');
  await expect(view).toContainText('out of range: earns nothing');
  await expect(view.locator('tr', { hasText: 'owner' }).first()).toContainText('g1gdg8jj');
  await expect(view).not.toContainText('held by');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

test('a collection page opens one of its items by id', async ({ page }) => {
  const seen = watch(page);
  await page.route('**/api/asset/**', route => route.fulfill({
    status: 200, contentType: 'application/json', body: JSON.stringify({
      network: 'alpha',
      asset: {
        token: 'gno.land/r/gnoswap/gnft.GNFT.0000000', pkg_path: 'gno.land/r/gnoswap/gnft', symbol: 'GNFT',
        kind: 'grc721', fungible: false, transfers: 201, holders: 3,
      },
      holders: [], transfers: [],
    }),
  }));
  await page.route('**/api/nft/**', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(GNFT_1) }));

  await page.goto('/grc20/' + encodeURIComponent('gno.land/r/gnoswap/gnft.GNFT.0000000'));
  await settle(page);
  const box = page.locator('input[data-track="nft-lookup"]');
  await box.fill('1');
  await box.press('Enter');
  await settle(page);

  expect(new URL(page.url()).pathname).toBe('/nft/gno.land/r/gnoswap/gnft/1');
  await expect(page.locator('#nft-item-content')).toContainText('GNFT #1');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});
