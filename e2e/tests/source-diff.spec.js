// What changed between two publications: the diff view in the code map, and
// the links into it from the version rail, the timeline and the realm page.
//
// What a Go test cannot see: that the API summary and the file hunks are
// drawn, that each entry point lands on the right pair of submissions, and
// that source the chain hands us is drawn as text and never as markup.
import { test, expect } from '@playwright/test';

import { settle, unexpected, watch } from './helpers.js';

const V2 = '/code/r/diffy/app/v2';

test('the diff view draws the API change and the file hunks', async ({ page }) => {
  const w = watch(page);
  await page.goto(V2 + '?network=alpha&compare=..');
  await settle(page);

  const head = page.locator('.cx-diff-head');
  await expect(head).toContainText('what changed in gno.land/r/diffy/app/v2');
  // Defaults: the current stamp against the successful one before it.
  await expect(head.locator('.cx-diff-side').first()).toContainText('801');
  await expect(head.locator('.cx-diff-side').last()).toContainText('802');

  const api = page.locator('.cx-diff-api');
  await expect(api.locator('.cx-diff-sig.del').filter({ hasText: 'func Count() int' })).toHaveCount(1);
  await expect(api.locator('.cx-diff-sig.add').filter({ hasText: 'func Count(of string) int' })).toHaveCount(1);
  await expect(api.locator('.cx-diff-sig.add').filter({ hasText: 'func Extra()' })).toHaveCount(1);

  const app = page.locator('.cx-diff-file[data-file="app.gno"]');
  await expect(app.locator('.cx-diff-fhead')).toContainText('modified');
  await expect(app.locator('.cx-diff-row.add')).not.toHaveCount(0);
  await expect(app.locator('.cx-diff-row.del')).not.toHaveCount(0);
  await expect(page.locator('.cx-diff-file[data-file="extra.gno"] .cx-diff-fhead')).toContainText('added');

  // The markup in the source is text: drawn, and not an element.
  await expect(app).toContainText('<img src=x onerror=alert(1)>');
  await expect(page.locator('.cx-diff img')).toHaveCount(0);

  // A file folds and unfolds from its header.
  await app.locator('.cx-diff-fhead').click();
  await expect(app).toHaveClass(/closed/);
  await app.locator('.cx-diff-fhead').click();
  await expect(app).not.toHaveClass(/closed/);

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('no exported signature changed is said in so many words', async ({ page }) => {
  const w = watch(page);
  await page.goto(V2 + '?network=alpha&compare=800..801&base=r/diffy/app/v1');
  await settle(page);
  await expect(page.locator('.cx-diff-api')).toContainText('no exported signature changed');
  expect(w.jsErrors).toEqual([]);
});

test('the version rail opens the diff, and a failed submission is marked', async ({ page }) => {
  const w = watch(page);
  await page.goto(V2 + '?network=alpha');
  await settle(page);

  const compare = page.locator('.cx-deploys a.cx-compare');
  await expect(compare).toHaveText('compare with the previous publication');
  await compare.click();
  await expect(page).toHaveURL(/compare=/);
  await expect(page.locator('.cx-diff-head')).toContainText('what changed');

  await page.goto(V2 + '?network=alpha');
  await settle(page);
  await page.locator('.cx-dot.fail').click();
  await expect(page).toHaveURL(/compare=802\.\.803/);
  await expect(page.locator('.cx-diff-head .cx-diff-side').last()).toContainText('failed');

  // The generation chip row compares with the previous generation.
  await page.goto(V2 + '?network=alpha');
  await settle(page);
  await page.locator('.cx-gens a.cx-compare').click();
  await expect(page).toHaveURL(/base=r%2Fdiffy%2Fapp%2Fv1/);
  await expect(page.locator('.cx-diff-head .cx-diff-side').first()).toContainText('v1');

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('timeline cards link to what changed', async ({ page }) => {
  const w = watch(page);
  await page.goto('/code/timeline?network=alpha&ns=diffy');
  await settle(page);

  const republished = page.locator('.tl-card').filter({ has: page.locator('.tl-kind-redeploy') });
  await expect(republished.locator('a.tl-changed')).toHaveText('what changed');
  await republished.locator('a.tl-changed').click();
  await expect(page).toHaveURL(/\/code\/r\/diffy\/app\/v2\?.*compare=\.\.802/);
  await expect(page.locator('.cx-diff-head .cx-diff-side').first()).toContainText('801');

  await page.goto('/code/timeline?network=alpha&ns=diffy');
  await settle(page);
  const version = page.locator('.tl-card').filter({ has: page.locator('.tl-kind-version') });
  await version.locator('a.tl-changed').click();
  await expect(page).toHaveURL(/base=r%2Fdiffy%2Fapp%2Fv1/);
  await expect(page.locator('.cx-diff-api')).toContainText('no exported signature changed');

  expect(w.jsErrors).toEqual([]);
  expect(unexpected(w.failedRequests)).toEqual([]);
});

test('the realm page links to its changes', async ({ page }) => {
  const w = watch(page);
  await page.goto('/realm/r/diffy/app/v2?network=alpha');
  await settle(page);
  const changes = page.locator('a.tab-changes');
  await expect(changes).toHaveText('changes');
  await changes.click();
  await expect(page).toHaveURL(/\/code\/r\/diffy\/app\/v2\?.*compare=/);
  await expect(page.locator('.cx-diff-head')).toContainText('what changed');
  expect(w.jsErrors).toEqual([]);
});
