// Build the matching API-router candidate first, then run:
// API_ROUTER_BINARY=/absolute/path/api-router BROWSER=chromium node --test this file
// BROWSER may be chromium or webkit; without it both engines run.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const net = require('node:net');
const { spawn, execFileSync } = require('node:child_process');
const { once } = require('node:events');
const { chromium, webkit } = require('playwright');

const binary = process.env.API_ROUTER_BINARY || path.resolve(__dirname, '../../..', 'dist/api-router');
const storageKey = 'api-router.tutorial.v1';
const modal = '#tutorialModal';

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ['chromium', 'webkit'])) {
  test(`first-run tutorial (${engine})`, { timeout: 180000 }, async (t) => {
    assert.ok(['chromium', 'webkit'].includes(engine), `unsupported browser: ${engine}`);
    const home = await fs.mkdtemp(path.join(os.tmpdir(), 'api-router-tutorial-'));
    let service, exited, browser;
    t.after(async () => {
      if (browser) await browser.close();
      if (service) {
        if (service.exitCode === null) service.kill('SIGTERM');
        await exited;
      }
      await fs.rm(home, { recursive: true, force: true });
    });
    const env = { ...process.env, HOME: home, USERPROFILE: home };
    for (const key of ['XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'APPDATA', 'LOCALAPPDATA']) env[key] = path.join(home, key);
    const layout = JSON.parse(execFileSync(binary, ['dirs'], { env, encoding: 'utf8', windowsHide: true }));
    execFileSync(binary, ['config', 'init'], { env, windowsHide: true });
    const reservation = net.createServer();
    reservation.listen(0, '127.0.0.1');
    await once(reservation, 'listening');
    const address = `127.0.0.1:${reservation.address().port}`;
    await new Promise(resolve => reservation.close(resolve));
    const origin = `http://${address}`;
    const configFile = path.join(layout.config, 'config.json');
    const config = JSON.parse(await fs.readFile(configFile, 'utf8'));
    config.listen.address = address;
    await fs.writeFile(configFile, JSON.stringify(config), { mode: 0o600 });
    const initialConfig = await fs.readFile(configFile, 'utf8');
    service = spawn(binary, ['serve'], { env, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true });
    exited = once(service, 'exit');
    let output = '';
    service.stdout.on('data', chunk => { output += chunk; });
    service.stderr.on('data', chunk => { output += chunk; });
    const tokenFile = path.join(layout.state, 'router.lock', 'management-token');
    let token;
    for (let i = 0; i < 200; i++) {
      if (service.exitCode !== null) assert.fail(`service exited: ${output}`);
      try { token = (await fs.readFile(tokenFile, 'utf8')).trim(); break; } catch {}
      await new Promise(resolve => setTimeout(resolve, 25));
    }
    assert.ok(token, `no management token: ${output}`);
    browser = await (engine === 'webkit' ? webkit : chromium).launch({
      ...(engine === 'chromium' && process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {})
    });

    // Every case has empty browser storage and the service uses an isolated home.
    // No external provider is configured. Observe the browser's real network
    // path and assert that reading sends no writes or external traffic.
    async function openCase(testCase, options = {}) {
      const context = await browser.newContext({ locale: 'en-US', viewport: { width: options.width || 1600, height: options.height || 950 } });
      const page = await context.newPage();
      const errors = [], mutations = [], external = [];
      const responses = [], failedRequests = [], sessionHeaders = [];
      page.on('pageerror', error => errors.push(error.message));
      page.on('response', response => responses.push({ path: new URL(response.url()).pathname, status: response.status() }));
      page.on('requestfailed', request => failedRequests.push({ path: new URL(request.url()).pathname, failure: request.failure() }));
      page.on('request', request => {
        const url = new URL(request.url());
        if (url.pathname === '/api/management/session') {
          const headers = request.headers();
          sessionHeaders.push(Object.fromEntries(['origin', 'sec-fetch-site', 'x-api-router-ui', 'accept-language'].map(key => [key, headers[key] || ''])));
        }
        if (!['http:', 'https:'].includes(url.protocol)) return;
        if (url.origin !== origin) {
          external.push(request.url());
        }
        if (!['GET', 'HEAD'].includes(request.method()) && !(request.method() === 'POST' && url.pathname === '/api/management/session')) {
          mutations.push(`${request.method()} ${url.pathname}`);
        }
      });
      if (options.stored !== undefined) await context.addInitScript(({ key, value }) => localStorage.setItem(key, value), { key: storageKey, value: options.stored });
      if (options.disabledStorage) await context.addInitScript(() => {
        Object.defineProperty(window, 'localStorage', { configurable: true, get() { throw new DOMException('Storage disabled for test', 'SecurityError'); } });
      });
      testCase.after(async () => {
        assert.deepEqual(mutations, [], 'reading the tutorial or following its navigation must not write configuration, download, probe or run inference');
        assert.deepEqual(external, [], 'tutorial browser traffic must remain on the isolated loopback fixture');
        assert.deepEqual(errors, [], 'tutorial must not produce uncaught page errors');
        assert.equal(await fs.readFile(configFile, 'utf8'), initialConfig, 'tutorial completion must not modify service configuration');
        await context.close();
      });
      await page.goto(`${origin}/?locale=${options.language || 'en'}`);
      try { await page.waitForFunction(() => !!managementToken && !!serviceInfo); }
      catch (error) {
        if (process.env.ARTIFACT_DIR) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await fs.writeFile(path.join(process.env.ARTIFACT_DIR, `tutorial-${engine}-startup-error.json`), JSON.stringify({
            responses, failedRequests, sessionHeaders, errors, mutations, external,
            service: await page.locator('#service').textContent(),
          }, null, 2));
        }
        throw error;
      }
      return page;
    }
    async function visible(page) {
      await page.locator(modal).waitFor({ state: 'visible' });
    }
    async function step(page, number) {
      await page.locator(`#tutorialSteps button[data-step="${number}"]`).click();
      await page.waitForFunction(number => document.querySelector(`#tutorialSteps button[data-step="${number}"]`).getAttribute('aria-current') === 'step', number);
    }
    async function state(page) {
      return page.evaluate(key => JSON.parse(localStorage.getItem(key)), storageKey);
    }
    async function assertStep(page, number) {
      assert.equal(await page.locator(`#tutorialSteps button[data-step="${number}"]`).getAttribute('aria-current'), 'step');
      assert.equal((await state(page)).step, number);
    }

    await t.test('first open, skip and resume preserve independent reading progress', async t => {
      const page = await openCase(t);
      await visible(page);
      const uiBefore = await page.evaluate(() => localStorage.getItem('api-router.ui'));
      assert.equal(await page.locator(modal).getAttribute('role'), 'dialog');
      assert.equal(await page.locator(modal).getAttribute('aria-modal'), 'true');
      assert.equal(await page.locator(modal).getAttribute('aria-labelledby'), 'tutorialTitle');
      assert.equal(await page.locator('#tutorialTitle').evaluate(node => node.tagName), 'H2');
      assert.deepEqual(await page.locator('#tutorialSteps button[data-step]').evaluateAll(nodes => nodes.map(node => Number(node.dataset.step))), [0, 1, 2, 3, 4, 5]);
      assert.equal(await page.locator('#tutorialBack').isDisabled(), true);
      const fresh = await state(page);
      assert.equal(fresh.version, 1);
      assert.equal(fresh.completed, false);
      await page.locator('#tutorialNext').click();
      await assertStep(page, 1);
      await step(page, 2);
      await page.locator('#tutorialContent button[data-app="loomy"]').click();
      assert.equal((await state(page)).app, 'loomy');
      await page.locator('#tutorialDismiss').click();
      await page.locator(modal).waitFor({ state: 'hidden' });
      assert.equal((await state(page)).dismissed, true);
      await page.reload();
      await page.waitForFunction(() => !!managementToken && !!serviceInfo);
      assert.equal(await page.locator(modal).isVisible(), false, 'skipping must not force the guide back on reload');
      assert.equal(await page.locator('#tutorialResume').isVisible(), true);
      await page.locator('#tutorialContinue').click();
      await visible(page);
      await assertStep(page, 2);
      assert.equal(await page.locator('#tutorialContent button[data-app="loomy"]').getAttribute('aria-pressed'), 'true');
      const stored = await page.evaluate(() => ({ ...localStorage }));
      assert.equal(stored['api-router.ui'] ?? null, uiBefore, 'reading progress must not overwrite ordinary UI preferences');
      assert.equal(JSON.stringify(stored).includes(token), false, 'reading state must never contain the management token');
      assert.deepEqual(Object.keys(await state(page)).sort(), ['app', 'completed', 'dismissed', 'route', 'step', 'version']);
    });

    await t.test('keyboard focus remains in the dialog, Escape closes and manual entry restores focus', async t => {
      const page = await openCase(t);
      await visible(page);
      for (const key of ['Tab', 'Shift+Tab']) {
        for (let i = 0; i < 16; i++) {
          await page.keyboard.press(key);
          assert.equal(await page.evaluate(() => document.querySelector('#tutorialModal').contains(document.activeElement)), true, `${key} must remain in the guide`);
        }
      }
      await step(page, 1);
      await page.keyboard.press('Escape');
      await page.locator(modal).waitFor({ state: 'hidden' });
      await page.reload();
      await page.waitForFunction(() => !!managementToken && !!serviceInfo);
      assert.equal(await page.locator(modal).isVisible(), false);
      await page.locator('#tutorialOpen').click();
      await visible(page);
      await assertStep(page, 1);
      await page.locator('#tutorialClose').click();
      await page.locator(modal).waitFor({ state: 'hidden' });
      assert.equal(await page.evaluate(() => document.activeElement.id), 'tutorialOpen');
      await page.locator('#prefs').click();
      await page.locator('#tutorialSettings').click();
      await visible(page);
      await assertStep(page, 1);
      await page.keyboard.press('Escape');
      assert.equal(await page.evaluate(() => document.activeElement.id), 'tutorialSettings');
    });

    await t.test('cloud and local routes navigate reversibly and Go only opens existing pages', async t => {
      const page = await openCase(t);
      await visible(page);
      await step(page, 3);
      await page.locator('#tutorialContent button[data-route="cloud"]').click();
      await page.locator('#tutorialNext').click();
      await assertStep(page, 5);
      await page.locator('#tutorialBack').click();
      await assertStep(page, 3);
      await page.locator('#tutorialContent button[data-route="local"]').click();
      await page.locator('#tutorialNext').click();
      await assertStep(page, 4);
      await page.locator('#tutorialNext').click();
      await assertStep(page, 5);
      await page.locator('#tutorialBack').click();
      await assertStep(page, 4);
      for (const [number, target] of [[0, 'agents'], [1, 'providers'], [2, 'agents'], [3, 'local-model'], [4, 'local-model'], [5, 'usage']]) {
        await step(page, number);
        await page.locator('#tutorialGo').click();
        await page.locator(modal).waitFor({ state: 'hidden' });
        assert.equal(await page.locator(`#view-${target}`).isVisible(), true, `chapter ${number} must open ${target}`);
        assert.equal(await page.locator('#providerModal, #profileModal').evaluateAll(nodes => nodes.every(node => node.hidden)), true, 'Go must only navigate, not open an editor that writes or verifies a model');
        if (number === 5) assert.equal(await page.locator('#ledgerPane').isVisible(), true, 'last chapter opens the per-request statistics tab');
        await page.locator('#tutorialOpen').click();
        await visible(page);
        await assertStep(page, number);
      }
    });

    await t.test('finishing marks reading complete while actual configuration remains untouched', async t => {
      const page = await openCase(t, { language: 'zh' });
      await visible(page);
      assert.equal(await page.locator('.tutorial-rail-title').textContent(), '从这里开始');
      assert.equal(await page.locator('.tutorial-eyebrow').textContent(), '新手入门');
      await step(page, 5);
      await page.locator('#tutorialNext').click();
      await page.locator(modal).waitFor({ state: 'hidden' });
      assert.equal((await state(page)).completed, true);
      assert.equal(await fs.readFile(configFile, 'utf8'), initialConfig);
      await page.locator('#nav [data-view=usage]').click();
      assert.match(await page.locator('#tutorialSummary').textContent(), /阅读|閱讀|读完|讀完|已读|已讀/);
      assert.doesNotMatch(await page.locator('#tutorialSummary').textContent(), /配置已完成|已完成配置|模型已安装|模型已安裝/);
      await page.reload();
      await page.waitForFunction(() => !!managementToken && !!serviceInfo);
      assert.equal(await page.locator(modal).isVisible(), false);
      await page.locator('#tutorialOpen').click();
      await visible(page);
      await assertStep(page, 5);
    });

    await t.test('five languages fit desktop and mobile without untranslated chapter headings', async t => {
      const english = [];
      for (const language of ['en', 'zh', 'zh-TW', 'ja', 'de']) {
        for (const [width, height] of [[1600, 950], [1100, 600], [375, 667]]) {
          const page = await openCase(t, { language, width, height });
          await visible(page);
          for (let number = 0; number < 6; number++) {
            await step(page, number);
            if (number === 3) {
              await page.locator('#tutorialContent button[data-route="local"]').click();
              assert.equal(await page.locator('.tutorial-model-label strong').textContent(), 'Spark-X2.5 · 1.7B / 4B');
              assert.equal(await page.locator('.tutorial-model-label .tutorial-inlinehint').textContent(), await page.evaluate(() => t('Both tiers use the same installation and trial flow')));
            }
            if (number === 4) {
              assert.equal(await page.locator('.tutorial-sequence li').nth(1).locator('strong').textContent(), await page.evaluate(() => t('Select 1.7B or 4B')));
              assert.equal(await page.locator('.tutorial-sequence li').nth(1).locator('.tutorial-inlinehint').textContent(), await page.evaluate(() => t('Choose a fixed 1.7B or 4B model. Both use the same download, configuration and capability-check flow.')));
            }
            const heading = (await page.locator('#tutorialChapter').textContent()).trim();
            assert.ok(heading.length > 0);
            if (language === 'en' && width === 1600) english[number] = heading;
            if (language !== 'en') assert.notEqual(heading, english[number], `${language} chapter ${number} must use localized copy`);
            assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `${language}/${width} must not overflow the page`);
            assert.equal(await page.locator('#tutorialContent').evaluate(node => node.scrollWidth <= node.clientWidth + 1), true, `${language}/${width} chapter ${number} must wrap its content`);
            assert.equal(await page.locator(modal).evaluate(node => {
              const dialog = node.querySelector('.tutorial-dialog');
              const rect = dialog.getBoundingClientRect();
              const footer = node.querySelector('.tutorial-footer').getBoundingClientRect();
              return rect.left >= -1 && rect.right <= innerWidth + 1 && rect.top >= -1 && rect.bottom <= innerHeight + 1 && dialog.scrollWidth <= dialog.clientWidth + 1 && footer.top >= rect.top && footer.bottom <= rect.bottom + 1;
            }), true, `${language}/${width} chapter ${number} must fit its dialog`);
          }
          if (process.env.ARTIFACT_DIR && ['en', 'de', 'zh'].includes(language)) {
            await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
            await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `tutorial-${engine}-${language}-${width}.png`) });
          }
        }
      }
    });

    await t.test('invalid saved state safely resets without disrupting the app', async t => {
      for (const stored of ['{broken', 'null', '[]', JSON.stringify({ version: 99, step: 99, route: 'invalid', app: 'codex', completed: true, dismissed: true })]) {
        const page = await openCase(t, { stored });
        await visible(page);
        await assertStep(page, 0);
        const saved = await state(page);
        assert.equal(saved.version, 1);
        assert.equal(saved.completed, false);
        assert.ok(['cloud', 'local'].includes(saved.route));
        assert.ok(['workbuddy', 'loomy'].includes(saved.app));
        await page.locator('#tutorialDismiss').click();
        await page.locator('#nav [data-view=providers]').click();
        assert.equal(await page.locator('#view-providers').isVisible(), true);
      }
    });

    await t.test('disabled storage keeps the guide and ordinary navigation usable', async t => {
      const page = await openCase(t, { disabledStorage: true });
      await visible(page);
      await page.locator('#tutorialNext').click();
      assert.equal(await page.locator('#tutorialSteps button[data-step="1"]').getAttribute('aria-current'), 'step');
      await page.locator('#tutorialGo').click();
      await page.locator(modal).waitFor({ state: 'hidden' });
      assert.equal(await page.locator('#view-providers').isVisible(), true);
      await page.locator('#tutorialOpen').click();
      await visible(page);
      assert.equal(await page.locator('#tutorialSteps button[data-step="1"]').getAttribute('aria-current'), 'step');
      await page.keyboard.press('Escape');
      await page.locator('#nav [data-view=usage]').click();
      assert.equal(await page.locator('#view-usage').isVisible(), true);
    });
  });
}
