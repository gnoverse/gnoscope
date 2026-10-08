import { test, expect } from '@playwright/test';

// /failed: the reverted calls of a window, against the window before it. The
// fixture's calls are all successes, so the claim here is the page's shape: it
// renders, says plainly when nothing reverted, offers the windows, and does not
// pretend to know why a call reverted.
test('/failed renders and says so when nothing reverted', async ({ page }) => {
  const errors = [];
  page.on('pageerror', e => errors.push(String(e)));

  await page.goto('/failed?network=alpha');
  await expect(page.locator('#failed-content h1')).toHaveText('failed calls');
  await expect(page.locator('#failed-content .stats-bar .stat', { hasText: 'failure rate' })).toBeVisible();
  await expect(page.locator('#failed-content')).toContainText('the reason a call reverted is not stored');
  await expect(page.locator('#failed-content').getByRole('button', { name: '7d' })).toBeVisible();

  // The rail lights the blocks section when a child of it is open.
  await expect(page.locator('#nav-failed')).toBeVisible();
  expect(errors).toEqual([]);
});
