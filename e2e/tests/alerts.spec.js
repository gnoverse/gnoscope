import { expect, test } from '@playwright/test';

import { settle, watch } from './helpers.js';

// /alerts: each rule in words with its thresholds, and a verdict that
// distinguishes "quiet" from "could not look".
const BODY = {
  network: 'alpha',
  checked: '2026-10-08T10:00:00Z',
  alerts: [
    { id: 'failure-spike', title: 'reverts are spiking', rule: 'the last hour has at least 10 failed calls and more than 2x the rate before it',
      firing: true, evidence: [{ text: '12 of 12 calls failed in the last hour', href: '/failed?window=1h' }] },
    { id: 'large-transfer', title: 'a large native transfer', rule: 'a successful send of at least 1,000,000 GNOT in 24 hours', firing: false },
    { id: 'other', title: 'a rule that could not be read', rule: 'whatever', firing: false, unread: 'the thing could not be read' },
  ],
};

test('alerts state their rules, fire on evidence, and say when they could not look', async ({ page }) => {
  const seen = watch(page);
  await page.route('**/api/alerts*', route =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(BODY) }));
  await page.goto('/alerts?network=alpha');
  await settle(page);

  const spike = page.locator('#alert-failure-spike');
  await expect(spike).toContainText('firing');
  await expect(spike).toContainText('rule: the last hour has at least 10 failed calls');
  await expect(spike.getByText('12 of 12 calls failed in the last hour')).toBeVisible();

  await expect(page.locator('#alert-large-transfer')).toContainText('quiet');
  const unread = page.locator('#alert-other');
  await expect(unread).toContainText('not read');
  await expect(unread).not.toContainText('quiet');
  await expect(unread).toContainText('the thing could not be read');

  await expect(page.locator('#nav-alerts')).toBeVisible();
  expect(seen.jsErrors).toEqual([]);
});
