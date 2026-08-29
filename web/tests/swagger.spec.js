import {expect, test} from '@playwright/test';

test('serves the self-contained Swagger UI and OpenAPI contract', async ({page, request}) => {
  const browserErrors = [];
  page.on('console', message => {
    if (message.type() === 'error') browserErrors.push(message.text());
  });
  page.on('pageerror', error => browserErrors.push(error.message));

  const directResponse = await page.goto('/docs');
  expect(directResponse?.status()).toBe(200);
  await expect(page).toHaveURL(/\/docs\/$/);
  await expect(page.locator('.swagger-ui .title')).toContainText('Cloudlet API');
  await expect(page.getByText('Auth', {exact: true}).first()).toBeVisible();
  await expect(page.getByText('Files', {exact: true}).first()).toBeVisible();

  await page.reload();
  await expect(page.locator('.swagger-ui .title')).toContainText('Cloudlet API');

  const contract = await request.get('/openapi.yaml');
  expect(contract.status()).toBe(200);
  expect(contract.headers()['content-type']).toContain('application/yaml');
  const contractDocument = JSON.parse(await contract.text());
  expect(contractDocument.info.title).toBe('Cloudlet API');

  expect(browserErrors, browserErrors.join('\n')).toEqual([]);
});
