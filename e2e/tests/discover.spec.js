import { expect, test } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

// /discover renders a judgement, so the tests are about what a reader can see
// and act on rather than about the numbers, which pkg/httpapi already covers.
//
// The payload is intercepted rather than seeded. The rollup runs on a tick and
// the harness starts with -sync=false, so seeding discover_events would test
// the builder's schedule and not the page; intercepting pins the exact shapes
// that are hard to produce on demand and easy to get wrong: a shareable event
// that does not rank onto page 1, a hold, and a package path written by
// somebody hostile.

const SPIKE = {
  id: 'alpha/chain.spike/2026-09-18/0',
  kind: 'chain.spike',
  at: '2026-09-18T00:00:00Z',
  height: 4200,
  network: 'alpha',
  facts: { new_addresses: 603, baseline_median: 46, excess: 557, ratio: 13.1, day: '2026-09-18' },
  layers: {
    what: { text: '603 addresses appeared on alpha for the first time on 2026-09-18, against a 7-day median of 46.' },
    means: { text: '603 new wallets showed up on the chain in one day.' },
    matters: { text: 'A normal day is about 46, so that is 13 times normal.' },
  },
  headline: '603 new wallets showed up on the chain in one day.',
  verdict: 'share',
  verdict_reason: '603 new wallets in one day, 13 times normal, and it is our chain\'s own number.',
  judgement: {
    interest: { level: 'high', why: '13 times a normal day of new wallets' },
    clearance: { level: 'ours', why: 'a chain-wide number belongs to nobody in particular' },
    blocking: null,
  },
  demoted: false,
};

// The package name is the injection surface: it is whatever a deployer typed,
// and it reaches a headline, a link and a tooltip.
const HOSTILE = '<img src=x onerror=alert(1)>';

const DEPLOY = {
  id: 'alpha/package.deployed/gno.land/r/probe/evil/0',
  kind: 'package.deployed',
  at: '2026-09-19T00:00:00Z',
  height: 4300,
  network: 'alpha',
  facts: { package_name: HOSTILE, namespace: 'probe', num_files: 3 },
  layers: {
    what: { text: 'MsgAddPackage published a package at block 4300.' },
    means: { text: '@probe put a new app on the chain, called ' + HOSTILE + '.' },
    matters: { text: 'Anyone can look at it or use it now.' },
  },
  headline: '@probe put a new app on the chain.',
  verdict: 'maybe',
  verdict_reason: 'No clearance is configured for this namespace.',
  judgement: {
    interest: { level: 'medium', why: 'from 4 different people' },
    clearance: { level: 'unclear', why: 'no clearance is configured for this namespace' },
    blocking: 'confirm with whoever owns this namespace before posting',
  },
  actor: 'g1arpgqtq9q3emx6mfhuz7xuwd6qptcwynedzlk2',
  target: 'gno.land/r/probe/' + HOSTILE,
  namespace: 'probe',
  evidence_tx: 'aGVsbG8vd29ybGQr',
  demoted: false,
};

const HELD = {
  id: 'alpha/package.deployed/gno.land/r/other/thing/0',
  kind: 'package.deployed',
  at: '2026-09-17T00:00:00Z',
  height: 4100,
  network: 'alpha',
  facts: { package_name: 'thing', namespace: 'other' },
  layers: {
    what: { text: 'MsgAddPackage published gno.land/r/other/thing at block 4100.' },
    means: { text: 'Somebody put a new app on the chain, called thing.' },
    matters: { text: 'Anyone can look at it or use it now.' },
  },
  headline: 'Somebody put a new app on the chain, called thing.',
  verdict: 'hold',
  verdict_reason: 'Nothing about this stands out against a normal day.',
  judgement: {
    interest: { level: 'low', why: 'from 1 person' },
    clearance: { level: 'unclear', why: 'no clearance is configured for this namespace' },
    blocking: null,
  },
  demoted: false,
};

function envelope(events, over) {
  return {
    network: 'alpha',
    vocabulary: 1,
    built_at: '2026-09-19T01:00:00Z',
    window: { label: '30d', from: '2026-08-20T00:00:00Z', to: '2026-09-19T00:00:00Z' },
    counts: { 'chain.spike': 1, 'package.deployed': 2 },
    verdict_counts: { share: 1, maybe: 1, hold: 1 },
    clearance_configured: false,
    total: 3,
    unranked: 0,
    events,
    ...over,
  };
}

// The page asks twice: the ranked page, and the shares on their own. Answering
// them differently is the whole point of the second request.
async function stub(page, { ranked, shares }) {
  await page.route('**/api/discover*', route => {
    const url = new URL(route.request().url());
    const body = url.searchParams.get('verdict') === 'share'
      ? envelope(shares)
      : envelope(ranked);
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(body) });
  });
}

// The reason this page exists. A reader arrives asking "is there anything I
// should tell people about", and the ranked order does not answer it: measured
// on mainnet the one shareable event sorted below every deployer.first row. So
// the share is lifted out, and it has to appear even when it is nowhere in the
// ranked page the reader is also being shown.
test('a shareable event leads the page even when it is not in the ranked page', async ({ page }) => {
  const seen = watch(page);
  await stub(page, { ranked: [DEPLOY, HELD], shares: [SPIKE] });

  await page.goto('/discover?network=alpha');
  await settle(page);

  const lead = page.locator('#discover-content .dsc-card').first();
  await expect(lead).toContainText('603 new wallets showed up on the chain in one day.');
  await expect(lead.locator('.dsc-pill')).toHaveText('share');
  // The reason, not just the verdict: a recommendation a reader cannot argue
  // with is one they cannot learn from either.
  await expect(lead).toContainText('13 times normal');
  await expect(lead).toContainText('clearance');
  await expect(lead).toContainText('ours');

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

// Keeping the rejects on the page is a decision, not an oversight: they are how
// a reader calibrates against the machine and the audit trail on the day they
// disagree with it. Collapsed, so they do not crowd out the answer.
test('a held event stays on the page, folded away, with its reason', async ({ page }) => {
  await stub(page, { ranked: [DEPLOY, HELD], shares: [SPIKE] });
  await page.goto('/discover?network=alpha');
  await settle(page);

  const ruled = page.locator('#discover-content .dsc-ruled');
  await expect(ruled).toContainText('ruled out (1)');
  // Folded: present in the DOM, not visible until asked for.
  const held = ruled.locator('.dsc-card');
  await expect(held).toHaveCount(1);
  await expect(held).not.toBeVisible();

  // `> summary` and not `summary`: each card carries its own disclosure for the
  // raw fact set, so the descendant form matches three elements here.
  await ruled.locator('> summary').click();
  await expect(held).toBeVisible();
  await expect(held).toContainText('Nothing about this stands out');
});

// Four separate bugs shipped on this endpoint by publishing a number that
// looked like it answered the question. The page prints the three terms so a
// reader can add them up rather than trust them.
test('the footer prints an envelope that adds up', async ({ page }) => {
  await stub(page, { ranked: [DEPLOY, HELD], shares: [SPIKE] });
  await page.goto('/discover?network=alpha');
  await settle(page);

  const env = page.locator('#discover-content .dsc-envelope');
  await expect(env).toContainText('3 events in the last 30d');
  await expect(env).toContainText('3 judged (1 share, 1 maybe, 1 hold)');
  await expect(env).toContainText('0 outside the ranked pool');
});

// This page renders more attacker-controlled text than any other here: a
// package name reaches a headline, a link label and a tooltip, and all three
// are built through el()/textContent. A name that is markup must come out as
// characters.
test('a package name written as markup renders as text', async ({ page }) => {
  const seen = watch(page);
  await stub(page, { ranked: [DEPLOY], shares: [] });
  await page.goto('/discover?network=alpha');
  await settle(page);

  const card = page.locator('#discover-content .dsc-card').first();
  await expect(card.locator('.dsc-headline')).toContainText(HOSTILE);
  // The injected tag must not have become an element anywhere on the page.
  expect(await page.locator('#discover-content img').count()).toBe(0);

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});

// Ranking is against one chain's own baselines: how busy a normal day is
// there, how many people have ever deployed there. Interleaving two chains
// produces an order true of neither, so the server refuses it. The page has to
// offer the choice rather than surface the 400.
test('all-networks offers a chain to pick instead of an error', async ({ page }) => {
  const seen = watch(page);
  await page.goto('/discover');
  await page.evaluate(() => localStorage.setItem('mygnoscan-network', 'all'));
  await page.goto('/discover');
  await settle(page);

  const content = page.locator('#discover-content');
  await expect(content).toContainText('pick one');
  // The buttons, not just the sentence: they are built from a list that arrives
  // over the network after the route fires, and sampling it too early rendered
  // the invitation with nothing to accept.
  await expect(content.locator('button')).not.toHaveCount(0);

  expect(seen.jsErrors, 'uncaught exceptions').toEqual([]);
  expect(unexpected(seen.consoleErrors), 'console errors').toEqual([]);
});
