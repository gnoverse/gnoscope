import { expect, test } from '@playwright/test';

import { settle } from './helpers.js';

// The balance reconciliation, in the ledgers on the account page's defi tab.
//
// A reconstructed balance and the chain's own figure disagree for a spending
// account, and the page used to explain that in a sentence: "gas and storage
// deposits leave no transfer event". True, and unfalsifiable by the reader.
//
// Both spends are indexed, so three of the four terms are arithmetic:
//
//   live = derived + genesis - gas - storage_deposit
//
// and only genesis is a residual. Measured against mainnet on 2026-09-28, the
// three computable terms close to the ugnot on post-genesis accounts
// (g1ptqfn8… and g1yq399y5…, residual exactly 0).
//
// Each case below is one of the three things the residual can be, because the
// whole value of showing the arithmetic is lost if the page overclaims on any
// of them.

const ADDR = 'g1recon0000000000000000000000000000000';
const CARD = '[data-pane="defi"]';

function stub(page, holdings, identity) {
  page.route('**/api/address/*/identity*', r => r.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify(identity || { address: ADDR, kind: 'signer', transactions: 1 }),
  }));
  return page.route('**/api/address/*/holdings*', r => r.fulfill({
    status: 200, contentType: 'application/json',
    body: JSON.stringify({
      network: 'alpha', path: '', address: ADDR,
      balance_known: true, storage_balance_known: false,
      flows: [], counterparties: [], tokens: [], token_flows: [],
      flows_shown: 0, flows_offset: 0, token_flows_shown: 0, token_flows_total: 0,
      truncated: false,
      ...holdings,
    }),
  }));
}

async function openHoldings(page) {
  await page.goto(`/address/${ADDR}?network=alpha&tab=defi`);
  await settle(page);
  return page.locator(CARD);
}

// The case the whole change exists for: the gap is spent, not missing.
test('the gap is decomposed into gas and storage deposits', async ({ page }) => {
  stub(page, {
    derived_ugnot: 151276588, live_ugnot: 75833169,
    gas_ugnot: 28796119, storage_deposit_ugnot: 46647300,
    transactions: 146, unexplained_ugnot: 0, flows_total: 89,
  });
  const card = await openHoldings(page);
  await expect(card).toContainText('in gas');
  await expect(card).toContainText('storage deposits');
  await expect(card).toContainText('146 transactions');
  // Closing exactly is the claim worth making, and only when it is true.
  await expect(card).toContainText('this history is complete');
  // The old copy asserted the cause without the numbers. It must not come back.
  await expect(card).not.toContainText('always reads short');
});

// A positive residual is a credit with no transfer behind it, and only genesis
// does that. Saying so still needs the evidence, which is the vesting schedule.
test('a vesting account has its residual named as the genesis allocation', async ({ page }) => {
  stub(page, {
    derived_ugnot: -5353788610, live_ugnot: 104751705757,
    gas_ugnot: 310054432, storage_deposit_ugnot: 173793100,
    transactions: 492, unexplained_ugnot: 110589341899, flows_total: 281,
  }, {
    address: ADDR, kind: 'signer', transactions: 492,
    chain: { exists: true, has_signed: true, sequence: 333, vesting: { original: '106560000000ugnot', start_time: 1789225200, end_time: 1852383600 } },
  });
  const card = await openHoldings(page);
  await expect(card).toContainText('unaccounted for');
  await expect(card).toContainText('genesis allocation');
  await expect(card).toContainText('vesting schedule');
});

// Without that evidence the same number must stay unexplained. An explorer that
// upgrades a guess to a fact when it happens to be plausible is worse than one
// that says it does not know.
test('without a vesting schedule the residual is named, not explained', async ({ page }) => {
  stub(page, {
    derived_ugnot: 500000000, live_ugnot: 900000000,
    gas_ugnot: 1000000, storage_deposit_ugnot: 0,
    transactions: 3, unexplained_ugnot: 401000000, flows_total: 4,
  }, { address: ADDR, kind: 'signer', transactions: 3, chain: { exists: true, has_signed: true, sequence: 3 } });
  const card = await openHoldings(page);
  await expect(card).toContainText('unaccounted for');
  await expect(card).toContainText('not claimed here');
  await expect(card).not.toContainText('carries a vesting schedule');
});

// The fourth shape, and the reason the page is worth having: on mainnet
// g1leu8d2… is short by a stable 119.141 GNOT that none of the three terms
// explain. The page has to surface that rather than absorb it into a
// reassuring sentence, which is exactly what the old copy did.
test('an outflow the three terms do not explain is surfaced, not absorbed', async ({ page }) => {
  stub(page, {
    derived_ugnot: 786090163, live_ugnot: 33955263,
    gas_ugnot: 167501300, storage_deposit_ugnot: 465492600,
    transactions: 2080, unexplained_ugnot: -119141000, flows_total: 3927,
  });
  const card = await openHoldings(page);
  await expect(card).toContainText('more has left this account');
  await expect(card).toContainText('worth reporting');
  await expect(card).not.toContainText('this history is complete');
});

// A realm's banker signs nothing and pays no deposit, so the two middle terms
// are absent and the note must not grow a line of zeroes.
test('a realm account shows no gas line', async ({ page }) => {
  stub(page, {
    derived_ugnot: 84984428254, live_ugnot: 84984428254,
    gas_ugnot: 0, storage_deposit_ugnot: 0,
    transactions: 0, unexplained_ugnot: 0, flows_total: 12,
  }, { address: ADDR, kind: 'package', package: { address: ADDR, path: 'gno.land/r/hub/core', deposit: false } });
  const card = await openHoldings(page);
  await expect(card).not.toContainText('in gas');
});
