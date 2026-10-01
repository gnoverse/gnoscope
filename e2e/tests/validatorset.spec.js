import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

// The per-validator page.
//
// /validators already rendered the set; what it could not do is show one
// validator over time, because the table is a snapshot and a validator's story
// is a history.

const OPERATOR = 'g1manfred47kzduec920z88wfr64ylksmdcedlf5';

const DETAIL = {
  network: 'alpha',
  in_set: true,
  total_power: 215,
  validator: {
    address: 'g1val_a', name: 'val-a', moniker: 'val-a', operator: OPERATOR, voting_power: 60, spof: false,
    missed_100: 0, missed_24h: 3, avg_block_ms: 3300,
    blocks: 400, txs: 12, share: 0.6666, last_block_time: '2026-09-20T00:50:00Z',
  },
  proposals: [{ id: 18, title: 'Add validator ' + OPERATOR, status: 'ACCEPTED', op: 'add', power: 4 }],
  blocks: [{ height: 105, time: '2026-09-20T00:50:00Z', num_txs: 2 }],
  shares: [{ day: '2026-09-19', blocks: 300, shares: { g1val_a: 0.5 } }],
};

test('a validator page shows its history and says what the share series is', async ({ page }) => {
  const seen = watch(page);
  await page.route('**/api/validator/g1val_a*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DETAIL) }));

  const response = await page.goto('/validator/g1val_a');
  expect(response.status()).toBe(200);
  await settle(page);

  const detail = page.locator('#validator-detail-content');
  await expect(detail).toContainText('blocks proposed');
  await expect(detail).toContainText('recent blocks proposed');
  await expect(detail).toContainText('signs as');
  await expect(detail).toContainText('val-a');
  await expect(detail).toContainText('60 of 215');

  // Membership is a governance decision on gno, so the proposal that seated it
  // is linked, and lands on the proposal's own page.
  const prop = detail.getByText('#18 add (power 4)');
  await expect(prop).toBeVisible();
  await prop.click();
  await expect(page).toHaveURL(/\/govdao\/18/);

  // No historical validator set is stored anywhere, so the share series is the
  // observable half of one and the page has to say so rather than presenting it
  // as a voting-power timeline.
  await expect(detail).toContainText('no historical validator set is recorded');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

test('a validator that has left the set says so', async ({ page }) => {
  const seen = watch(page);
  await page.route('**/api/validator/g1val_a*', route =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ ...DETAIL, in_set: false }),
    }));

  await page.goto('/validator/g1val_a');
  await settle(page);

  // An address with history but no place in the current set would otherwise
  // read as an active validator.
  await expect(page.locator('#validator-detail-content')).toContainText('not in the current set');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

test('the set table links through to each validator', async ({ page }) => {
  const seen = watch(page);

  await page.goto('/validators');
  await settle(page);

  // The link is on the set's rows, which carry the consensus address.
  const history = page.locator('#validators-content').getByText('history', { exact: true }).first();
  await expect(history).toBeVisible();
  await history.click();
  await expect(page).toHaveURL(/\/validator\/g1/);

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

// gnockpit describes mainnet only. On any other chain its liveness figures are
// absent, and a missing figure must not read as a perfect record.
test('a validator page without liveness data does not claim zero missed blocks', async ({ page }) => {
  const seen = watch(page);
  const { missed_100, missed_24h, avg_block_ms, ...rest } = DETAIL.validator;
  await page.route('**/api/validator/g1val_a*', route =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ ...DETAIL, validator: rest, proposals: [] }),
    }));

  await page.goto('/validator/g1val_a');
  await settle(page);

  const detail = page.locator('#validator-detail-content');
  await expect(detail).toContainText('blocks proposed');
  await expect(detail).not.toContainText('missed 24h');
  await expect(detail).toContainText('most likely in the genesis set');
  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

// The bug this table had on onyx-1 (2026-10-01): it listed whoever proposed
// in the last 100 blocks, so a seated validator with little power that had not
// proposed recently was not in the "active set" at all.
test('the set table lists every member, including one that has not proposed', async ({ page }) => {
  const seen = watch(page);
  const QUIET = 'g1g0yrmdhney8pcaf9rfut4604equz604pszdsyt';
  await page.route('**/api/validators/live*', route =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify([
        { address: QUIET, voting_power: '4', operator: OPERATOR, moniker: 'quiet-one', name: 'quiet-one', spof: false,
          proposals: [{ id: 18, title: 'Add validator', status: 'ACCEPTED', op: 'add', power: 4 }] },
      ]),
    }));

  await page.goto('/validators');
  await settle(page);

  const set = page.locator('#valset');
  await expect(set).toContainText('#18 add (power 4)');
  await expect(set.locator('tbody tr')).toHaveCount(1);
  // No gnockpit figures in the payload, so no gnockpit columns.
  await expect(set.locator('thead')).not.toContainText('missed');
  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});

// The registrations table nests a history table per validator, and its filter
// key used to slug every nested header too: ~2 KB on onyx-1, growing with each
// registration, so every saved link broke the next time someone registered.
test('the registrations filter has a short key that does not grow', async ({ page }) => {
  const seen = watch(page);
  const reg = (addr, moniker, h) => ({
    tx_hash: 'tx' + h, block_height: h, block_time: '2026-10-01T11:21:32Z', caller: addr,
    func: 'Register', address: addr, moniker, success: true, network: 'alpha',
  });
  await page.route('**/api/validators?*', route =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify([reg(OPERATOR, 'moul', 2), reg('g1aeddlftlfk27ret5rf750d7w5dume3kcsm8r8m', 'other', 1)]),
    }));

  await page.goto('/validators');
  await settle(page);

  await expect(page.locator('#validators-content .table-filter[data-param="f.valopers"]')).toHaveCount(1);
  const params = await page.locator('#validators-content .table-filter').evaluateAll(
    els => els.map(e => e.getAttribute('data-param') || ''));
  for (const p of params) expect(p.length, 'filter key ' + p.slice(0, 80)).toBeLessThan(64);
  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
});
