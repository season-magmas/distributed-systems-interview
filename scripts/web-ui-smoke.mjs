// Browser checks run against an explicitly supplied disposable stack.
import assert from 'node:assert/strict';
import { mkdir, writeFile } from 'node:fs/promises';
const { chromium } = await import(process.env.LAB_PLAYWRIGHT_MODULE || 'playwright-core');
const baseURL = process.env.LAB_UI_URL;
if (!baseURL) throw Error('Set LAB_UI_URL to a disposable executor endpoint.');
const artifacts = process.env.LAB_UI_ARTIFACTS || '/tmp/lab-ui-evidence';
await mkdir(artifacts, { recursive: true });
const browser = await chromium.launch({
  executablePath: process.env.CHROMIUM_PATH || '/usr/bin/chromium',
  args: ['--no-sandbox'],
});
const errors = [];
const runs = [];
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 1000 } });
  page.on('pageerror', (error) => errors.push(error.message));
  await page.goto(baseURL);
  await page.locator('#workflow:enabled').waitFor();
  await page.selectOption('#workflow', 'PCR Amplification');
  await page.getByRole('button', { name: 'Create run' }).click();
  await page.locator('#start:enabled').waitFor();
  const runId = await page.locator('.record-head .run-id').innerText();
  runs.push(runId);
  assert.equal(new URL(page.url()).hash, `#${runId}`);
  await page.getByRole('button', { name: 'Start run', exact: true }).click();
  await page.locator('.record-head .badge.running').waitFor();
  await page.locator('details').first().locator('summary').click();
  await page.waitForTimeout(2500);
  assert.equal(await page.locator('details').first().getAttribute('open'), '');
  await page.locator('.record-head .badge.completed').waitFor({ timeout: 120000 });
  await page.locator('details').last().locator('summary').click();
  await page.getByText('Stored', { exact: true }).waitFor();
  assert.match(await page.locator('#record').innerText(), /s3:\/\/lab-data\//);
  await page.screenshot({ path: `${artifacts}/desktop.png`, fullPage: true });

  await page.route('**/graphql', (route) => route.abort());
  await page.locator('#read-error:visible').waitFor();
  assert.equal(await page.locator('.record-head .badge.completed').count(), 1);
  assert.equal(await page.locator('#create').isDisabled(), true);
  await page.unroute('**/graphql');
  await page.locator('#read-error').waitFor({ state: 'hidden' });

  await page.setViewportSize({ width: 390, height: 844 });
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await page.screenshot({ path: `${artifacts}/mobile.png`, fullPage: true });
  await page.reload();
  await page.locator('.record-head .badge.completed').waitFor();
  assert.equal(await page.locator('.record-head .run-id').innerText(), runId);

  await page.goto('about:blank');
  await page.goBack();
  await page.locator('.record-head .badge.completed').waitFor();
  const priorUpdate = await page.locator('#connection').innerText();
  await page.waitForFunction(
    (previous) => document.getElementById('connection').textContent !== previous,
    priorUpdate,
    { timeout: 8000 },
  );

  await page.getByRole('button', { name: 'Create run' }).click();
  await page.locator('#start:enabled').waitFor();
  runs.push(await page.locator('.record-head .run-id').innerText());
  await page.getByRole('button', { name: 'Request cancellation' }).click();
  await page.locator('.record-head .badge.cancelled').waitFor();

  let uncertainCreates = 0;
  await page.route('**/graphql', (route) => {
    if (route.request().postDataJSON().query.includes('mutation Create')) {
      uncertainCreates++;
      return route.abort();
    }
    return route.continue();
  });
  await page.getByRole('button', { name: 'Create run' }).click();
  await page.locator('#action-error:visible').waitFor();
  assert.match(await page.locator('#action-error').innerText(), /may have been applied/);
  await page.waitForTimeout(4500);
  assert.equal(uncertainCreates, 1);
  await page.unroute('**/graphql');

  await page.goto(`${baseURL}/#absent-run`);
  await page.getByRole('heading', { name: 'No record available.' }).waitFor();
  assert.equal(await page.locator('#actions').isHidden(), true);
  assert.deepEqual(errors, []);
  await writeFile(
    `${artifacts}/browser-result.json`,
    JSON.stringify(
      {
        status: 'passed',
        runs,
        checks: [
          'create',
          'start',
          'poll to completion',
          'measurement reference',
          'expanded details preserved',
          'read outage and recovery',
          '390px layout',
          'bookmark reload',
          'polling after browser back',
          'cancel pending',
          'uncertain mutation never replayed',
          'missing record',
          'no JavaScript errors',
        ],
      },
      null,
      2,
    ),
  );
  console.log(JSON.stringify({ status: 'passed', runs, artifacts }));
} finally {
  await browser.close();
}
