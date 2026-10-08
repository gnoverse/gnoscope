import { test, expect } from '@playwright/test';

// The home page's "new functions" panel: it renders from /api/pulse, and when
// nothing qualifies it says so in a row rather than drawing an empty table.
test('home has a new functions panel that is never blank', async ({ page }) => {
  const errors = [];
  page.on('pageerror', e => errors.push(String(e)));

  await page.goto('/?network=alpha');
  await expect(page.locator('#new-funcs')).toBeAttached();
  await expect(page.locator('.section-title', { hasText: 'new functions' })).toBeVisible();
  await expect(page.locator('#new-funcs tr').first()).toBeVisible();
  expect(errors).toEqual([]);
});
