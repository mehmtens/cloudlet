import {expect, test} from '@playwright/test';

test('register, create a folder, upload, trash, restore, and close the account', async ({page}) => {
  const suffix = `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  const email = `playwright-${suffix}@example.com`;
  const password = 'Playwright-test-123!';
  const folderName = `E2E-${suffix}`;
  const fileName = `cloudlet-${suffix}.txt`;

  await page.goto('/');
  await expect(page.getByRole('heading', {name: /Dosyaların seni/})).toBeVisible();
  await page.getByRole('button', {name: /Hesabın yok mu/}).click();
  await page.getByLabel('E-posta').fill(email);
  await page.getByLabel('Parola').fill(password);
  await page.getByRole('button', {name: 'Hesap oluştur'}).click();

  await expect(page.getByRole('heading', {name: /Dosyaların, senin düzenin/})).toBeVisible();
  await page.getByRole('button', {name: 'Yeni klasör'}).click();
  await page.getByLabel('Klasör adı').fill(folderName);
  await page.getByRole('button', {name: 'Klasör oluştur'}).click();
  await expect(page.getByRole('button', {name: new RegExp(folderName)})).toBeVisible();

  await page.locator('input[type="file"]').setInputFiles({
    name: fileName,
    mimeType: 'text/plain',
    buffer: Buffer.from('Cloudlet Playwright end-to-end upload'),
  });
  await expect(page.getByText(fileName, {exact: true})).toBeVisible();

  await page.getByRole('button', {name: `${fileName} dosyasını çöp kutusuna taşı`}).click();
  await expect(page.getByText(fileName, {exact: true})).not.toBeVisible();
  await page.getByRole('button', {name: 'Çöp kutusu'}).click();
  await expect(page.getByText(fileName, {exact: true})).toBeVisible();
  await page.getByRole('button', {name: 'Geri yükle'}).click();
  await expect(page.getByText(fileName, {exact: true})).not.toBeVisible();

  await page.getByRole('button', {name: 'Dosyalar'}).click();
  await expect(page.getByText(fileName, {exact: true})).toBeVisible();

  await page.getByRole('button', {name: 'Hesabı kapat'}).click();
  await page.getByLabel('Mevcut parola').fill(password);
  await page.getByLabel('Onaylamak için HESABIMI SİL yaz').fill('HESABIMI SİL');
  await page.getByRole('button', {name: 'Hesabı kalıcı olarak kapat'}).click();
  await expect(page.getByRole('button', {name: 'Giriş yap'})).toBeVisible();
});
