const { skipTutorial } = require('./returning-user.cjs');
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

test(`local model consent and management (${process.env.BROWSER || 'chromium'})`, { timeout: 180000 }, async t => {
  const home = await fs.mkdtemp(path.join(os.tmpdir(), 'api-router-local-model-ui-'));
  let service, exited, browser;
  t.after(async () => {
    if (browser) await browser.close();
    if (service) { if (service.exitCode === null) service.kill('SIGTERM'); await exited; }
    await fs.rm(home, { recursive: true, force: true });
  });
  const env = { ...process.env, HOME: home, USERPROFILE: home };
  for (const key of ['XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'APPDATA', 'LOCALAPPDATA']) env[key] = path.join(home, key);
  const layout = JSON.parse(execFileSync(binary, ['dirs'], { env, encoding: 'utf8' }));
  execFileSync(binary, ['config', 'init'], { env });
  const reservation = net.createServer(); reservation.listen(0, '127.0.0.1'); await once(reservation, 'listening');
  const base = `http://127.0.0.1:${reservation.address().port}`; await new Promise(resolve => reservation.close(resolve));
  const configFile = path.join(layout.config, 'config.json');
  const config = JSON.parse(await fs.readFile(configFile, 'utf8')); config.listen.address = new URL(base).host;
  await fs.writeFile(configFile, JSON.stringify(config), { mode: 0o600 });
  service = spawn(binary, ['serve'], { env, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true }); exited = once(service, 'exit');
  let output = ''; service.stdout.on('data', chunk => { output += chunk; }); service.stderr.on('data', chunk => { output += chunk; });
  let token;
  for (let i = 0; i < 200; i++) {
    if (service.exitCode !== null) assert.fail(output);
    try { token = (await fs.readFile(path.join(layout.state, 'router.lock', 'management-token'), 'utf8')).trim(); break; } catch {}
    await new Promise(resolve => setTimeout(resolve, 25));
  }
  assert.ok(token);
  const status = async () => (await (await fetch(base + '/api/local-model', { headers: { Authorization: 'Bearer ' + token } })).json()).status;
  assert.equal((await fetch(base + '/api/local-model')).status, 401);
  assert.equal((await status()).intent.enabled, false);
  browser = await (process.env.BROWSER === 'webkit' ? webkit : chromium).launch();
  const errors = [];
  const expectedInstallFailures = [];
  const recommendationKey = 'Based on the check, your computer is suitable for {model}.';
  const deviceReport = (tier, state, maxTier = tier) => ({
    device: {
      system: { value: { os: 'windows', version: { value: '10.0.26100' }, architecture: { value: 'amd64' }, cpu: { value: 'Fixture CPU' }, logicalCPUs: { value: 8 } } },
      memoryBytes: { value: tier === 'unknown' ? null : (tier === '4B' ? 32 : tier === 'none' ? 4 : maxTier === '4B' ? 16 : 12) * 2 ** 30 },
      availableMemoryBytes: { value: tier === 'unknown' ? null : (tier === '4B' ? 12 : tier === 'none' ? 2 : 6) * 2 ** 30 },
      adapters: { value: [] },
      diskFreeBytes: { value: state === 'failed' ? 0 : 100 * 2 ** 30 },
      backends: { value: [{ kind: 'cpu', state: 'available' }] },
    },
    recommendation: { tier, maxTier, reason: tier === 'unknown' ? 'memory_unknown' : tier === 'none' ? 'memory_below_8_gib' : tier === '4B' ? 'memory_at_least_24_gib' : maxTier === '4B' ? 'memory_16_gib_prefer_1_7b_allow_4b' : 'memory_at_least_8_gib' },
    detection: { state, reasons: state === 'failed' ? [tier === 'none' ? 'memory_below_8_gib' : 'disk_insufficient'] : state === 'unknown' ? ['memory_unknown'] : [] },
  });
  let pageIndex = 0;
  for (const language of ['zh', 'en', 'zh-TW', 'ja', 'de']) {
    for (const width of [1600, 375]) {
      const page = await browser.newPage({ locale: 'en-US' }); await page.setViewportSize({ width, height: 950 });
      let expectedInstallFailure = null;
      const startupResponses = [], startupFailures = [];
      page.on('response', response => {
        const url = new URL(response.url());
        if (url.origin === base && (url.pathname.endsWith('.js') || ['/api/management/session', '/api/service'].includes(url.pathname))) startupResponses.push({ path: url.pathname, status: response.status() });
      });
      page.on('requestfailed', request => {
        const url = new URL(request.url());
        if (url.origin === base) startupFailures.push({ path: url.pathname, error: request.failure()?.errorText });
      });
      page.on('pageerror', error => errors.push(error.message));
      page.on('console', message => {
        if (message.type() !== 'error') return;
        if (expectedInstallFailure && message.location().url === base + '/api/local-model/install' && /^Failed to load resource:.*\b409\b/.test(message.text())) {
          expectedInstallFailure.consoleErrors.push(message.text());
        } else errors.push(message.text());
      });
      const external = []; page.on('request', req => { if (new URL(req.url()).origin !== base) external.push(req.url()); });
      const initialActions = [];
      page.on('request', request => { if (request.method() === 'POST' && new URL(request.url()).pathname.startsWith('/api/local-model/')) initialActions.push(new URL(request.url()).pathname); });
      const installation = ['not_installed', 'canceled', 'failed'][pageIndex++ % 3];
      const emptyModelStatus = async route => {
        const response = await route.fetch();
        const result = await response.json();
        result.status.installation = installation;
        result.status.progress = { artifact: 'dep', downloaded: 3313422, total: 3313422 };
        await route.fulfill({ response, json: result });
      };
      await page.route('**/api/local-model', emptyModelStatus);
      let report = deviceReport('1.7B', 'passed'), detections = 0;
      await page.route('**/api/local-model/detect', async route => {
        assert.deepEqual(route.request().postDataJSON(), {});
        detections++;
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ schemaVersion: 1, device: report }) });
      });
      await skipTutorial(page);
      await page.goto(`${base}/?locale=${language}&view=local-model`);
      try {
        await page.waitForFunction(() => document.querySelector('#service').classList.contains('ok'));
        await page.waitForFunction(() => !!managementToken && !!serviceInfo);
        await page.locator('#localModelChoices button').first().waitFor({ timeout: 5000 });
      } catch (error) {
        const startup = await page.evaluate(() => {
          const state = { tokenPresent: false, serviceInfoPresent: false, healthy: false, localHook: typeof window.routerLocalOpen, statusText: document.querySelector('#status')?.textContent, serviceText: document.querySelector('#service')?.textContent };
          try { state.tokenPresent = !!managementToken; } catch {}
          try { state.serviceInfoPresent = !!serviceInfo; } catch {}
          try { state.healthy = healthy; } catch {}
          return state;
        }).catch(() => ({ pageStateUnavailable: true }));
        t.diagnostic(JSON.stringify({ language, width, errors, startup, startupResponses, startupFailures }));
        throw error;
      }
      const recommendation = async model => page.evaluate(({ key, model }) => t(key).replace('{model}', model), { key: recommendationKey, model });
      await page.waitForFunction(key => document.querySelector('#localDeviceMessage')?.textContent === t(key).replace('{model}', 'Spark-X2.5-1.7B'), recommendationKey);
      await page.waitForFunction(() => !document.querySelector('#localDeviceGuide [data-action=detect]').disabled);
      assert.equal(await page.evaluate(() => [...document.querySelectorAll('#localModelContent .row b')].some(el => el.textContent === t('Downloading'))), false, `${installation} never renders a stale Downloading progress row`);
      assert.equal(detections, 1, `${installation} triggers one automatic read-only device check`);
      assert.deepEqual(initialActions, ['/api/local-model/detect'], 'opening an empty model page only detects; it does not select, enable, download or load');
      assert.deepEqual(await page.locator('#localModelChoices button').allTextContents(), ['Spark-X2.5-1.7B', 'Spark-X2.5-4B']);
      assert.equal(await page.locator('#localModelContent [data-action=detect]').count(), 1, 'one detection control is shown at the top');
      assert.equal(await page.evaluate(() => !!(document.querySelector('#localDeviceGuide').compareDocumentPosition(document.querySelector('#localModelChoices')) & Node.DOCUMENT_POSITION_FOLLOWING)), true, 'device guidance precedes model selection');
      if (language === 'zh') assert.equal(await page.locator('#localDeviceMessage').textContent(), '根据检测，您的电脑配置适合 Spark-X2.5-1.7B。');
      await page.locator('#localModelContent [data-action=refresh]').click();
      await page.evaluate(() => window.routerLocalOpen());
      assert.equal(detections, 1, 'manual and repeated refresh do not repeat automatic detection');
      await page.locator('#localModelChoices button').first().click();
      await page.waitForFunction(() => document.querySelector('#localModelChoices button').classList.contains('on'));
      await page.evaluate(() => window.routerLocalOpen());
      assert.equal(detections, 1, 'selecting a model does not repeat automatic detection');
      const [fourBSelection] = await Promise.all([
        page.waitForResponse(response => response.url() === base + '/api/local-model/select' && response.request().method() === 'POST'),
        page.locator('#localModelChoices button').last().click(),
      ]);
      assert.equal(fourBSelection.status(), 200, 'the real fixed catalog accepts 4B selection');
      assert.deepEqual(fourBSelection.request().postDataJSON(), { model: 'spark-x2.5-4B' });
      await page.waitForFunction(() => document.querySelector('#localModelChoices button:last-child').classList.contains('on'));
      const selectedFourB = await status();
      assert.equal(selectedFourB.intent.selected, 'spark-x2.5-4B', '4B selection is saved by the real manager');
      assert.equal(selectedFourB.intent.enabled, false, 'selecting 4B does not enable it');
      assert.equal(selectedFourB.installation, 'not_installed', 'selecting 4B does not download it');
      assert.equal(selectedFourB.runtime, 'unloaded', 'selecting 4B does not load it');
      await page.evaluate(() => window.routerLocalOpen());
      assert.equal(await page.locator('#localModelChoices button').last().getAttribute('aria-pressed'), 'true', 'refresh preserves the selected 4B model');
      await page.locator('#localModelChoices button').first().click();
      await page.waitForFunction(() => document.querySelector('#localModelChoices button').classList.contains('on'));
      const successCopies = [await recommendation('Spark-X2.5-1.7B'), await recommendation('Spark-X2.5-4B')];
      for (const [tier, state, maxTier] of [['4B', 'passed', '4B'], ['1.7B', 'passed', '4B'], ['1.7B', 'unknown', '1.7B'], ['1.7B', 'failed', '1.7B'], ['unknown', 'unknown', 'unknown'], ['none', 'failed', 'none'], ['1.7B', 'passed', '1.7B']]) {
        report = deviceReport(tier, state, maxTier);
        const before = detections;
        await page.locator('#localDeviceGuide [data-action=detect]').click();
        await page.waitForFunction(() => !document.querySelector('#localDeviceGuide [data-action=detect]').disabled);
        assert.equal(detections, before + 1, 'an explicit retry performs exactly one fresh detection');
        const message = await page.locator('#localDeviceMessage').textContent();
        if (state === 'passed') assert.equal(message, await recommendation(`Spark-X2.5-${tier}`), `${language}/${width}/${tier} recommendation names the precise model`);
        else {
          assert.ok(successCopies.every(copy => !message.includes(copy)), `${language}/${width}/${tier}/${state} never claims suitability`);
          const key = tier === 'none' ? 'Your computer does not meet the requirements for these local models.' : tier === 'unknown' ? 'Your computer configuration could not be fully detected. Please try again.' : 'Hardware recommendation: {model}. Complete the device checks before downloading.';
          assert.equal(message, await page.evaluate(({ key, tier }) => t(key).replace('{model}', `Spark-X2.5-${tier}`), { key, tier }), 'incomplete or failed checks show actionable guidance');
        }
        if (tier === '4B') assert.ok((await page.locator('#localDeviceGuide').textContent()).includes(await page.evaluate(() => t('You can also choose {model} to use fewer resources.').replace('{model}', 'Spark-X2.5-1.7B'))), '4B recommendation also offers the smaller model');
        if (tier === '1.7B' && maxTier === '4B') assert.ok((await page.locator('#localDeviceGuide').textContent()).includes(await page.evaluate(() => t('You can also try {model}, which uses more memory and runs more slowly on 16 GB devices.').replace('{model}', 'Spark-X2.5-4B'))), '16 GB 1.7B recommendation also notes optional 4B with warning');
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false, `${language}/${width}/${tier}/${state} remains within the viewport`);
      }
      assert.ok(initialActions.every(endpoint => ['/api/local-model/detect', '/api/local-model/select'].includes(endpoint)), 'recommendations and retries never mutate runtime or download state');
      await page.unroute('**/api/local-model', emptyModelStatus);
      const current = await status();
      assert.equal(current.intent.enabled, false, 'opening/detecting/selecting never enables');
      assert.equal(current.runtime, 'unloaded'); assert.equal(current.installation, 'not_installed');
      assert.ok(Object.values(current.capabilities).every(cap => cap.state === 'unknown'));
      assert.equal(await page.locator('#localModelContent [data-action=install]').isEnabled(), true);
      assert.equal(await page.locator('#localModelContent [data-action=cancel]').isDisabled(), true);
      assert.equal(await page.locator('#localModelContent [data-action=enable]').count(), 0);
      assert.equal(await page.locator('#localModelContent [data-action=load]').count(), 0);
      assert.equal(await page.locator('#localModelContent [data-action=disable]').count(), 0);
      assert.equal(await page.locator('#localModelContent [data-action=check]').count(), 0);
      assert.equal(await page.locator('#localModelContent [data-action="trial/enable"]').count(), 0);
      assert.equal(await page.locator('#localModelContent [data-action="trial/disable"]').count(), 0);
      assert.equal(await page.evaluate(() => [...document.querySelectorAll('#localModelContent .list.prefs .name')].some(el => [t('Judgment'), t('Execution'), t('Redaction')].includes(el.textContent))), false, 'capability rows are removed from status list');
      assert.deepEqual(external, []);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false);
      assert.equal(await page.locator('#localRuntimeSettings select').count(), 0, 'context choices use the app controls');
      assert.deepEqual(await page.locator('#localContextChoices button').allTextContents(), ['8K', '16K', '32K', '64K']);
      const preset = page.locator('#localSettingsForm button[type=button]').last();
      await preset.click();
      assert.equal(await page.locator('#localSettingsForm input[name=judgmentInferenceSeconds]').inputValue(), '30');
      await page.locator('#localSettingsForm input[name=requestSeconds]').fill('300');
      const beforeInvalidSettings = initialActions.length;
      await page.locator('#localSettingsForm [data-action=settings]').click();
      await page.waitForFunction(() => document.querySelector('#localModelError').textContent === t('Enter whole seconds within the shown limits. The request budget minus the cloud reserve must fit loading, judgment queue plus inference, and execution queue plus execution.'));
      assert.equal(initialActions.length, beforeInvalidSettings, 'inconsistent stage budgets never reach the service');
      await preset.click();
      await page.locator('#localContextChoices button').nth(2).click();
      await page.locator('#localSettingsForm input[name=judgmentInferenceSeconds]').fill('34');
      await page.locator('#localSettingsForm [data-action=settings]').click();
      await page.waitForFunction(() => !document.querySelector('#localSettingsForm [data-action=settings]').disabled);
      assert.equal((await status()).settings.contextTokens, 32768, 'the displayed context choice is persisted');
      assert.equal((await status()).settings.judgmentInferenceSeconds, 34, 'the edited inference budget is persisted');
      assert.equal((await status()).intent.enabled, false, 'saving settings does not enable a disabled model');
      assert.ok((await page.locator('#localSettingsSaved').textContent()).includes(await page.evaluate(() => (32768).toLocaleString(intlLang()))));
      assert.equal(await page.locator('#localSettingsForm input[name=requestSeconds]').inputValue(), '600');
      assert.ok((await page.locator('#localRuntimeSettings').textContent()).includes(await page.evaluate(() => t('Settings changes require new capability checks before experimental routing and execution.'))));
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false, 'runtime settings fit the viewport');
      if (process.env.ARTIFACT_DIR && ['zh', 'en'].includes(language)) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `local-model-${language}-${width}.png`), fullPage: true });
      }
      // Controlled management replies exercise dependency copy in every language
      // without installing packages or starting a model in browser tests.
      const actions = [];
      page.on('request', request => { if (request.method() === 'POST' && new URL(request.url()).pathname.startsWith('/api/local-model/')) actions.push(new URL(request.url()).pathname); });
      let dependency = { state: 'missing', version: '14.50.35717.0', error: 'local_model_dependency_missing', recovery: 'install' };
      let trialFixture = null;
      await page.route('**/api/local-model', async route => {
        const response = await route.fetch();
        const result = await response.json();
        result.status = { ...result.status, intent: { ...result.status.intent, enabled: true }, installation: 'installed', dependencies: dependency };
        if (trialFixture) {
          const capability = enabled => ({ state: trialFixture.unavailable ? 'unavailable' : 'unknown', ready: false, trial: !trialFixture.unavailable, trialEvidence: !trialFixture.unavailable, trialEnabled: enabled, trialReady: enabled, fingerprint: 'fixture', suite: 'fixture' });
          result.status = {
            ...result.status,
            installation: trialFixture.downloading ? 'downloading' : 'installed',
            progress: trialFixture.downloading ? { artifact: 'model', downloaded: 52428800, total: 104857600 } : result.status.progress,
            runtime: trialFixture.loaded ? 'ready' : 'unloaded',
            pid: trialFixture.loaded ? 4242 : 0,
            dependencies: { state: 'ready', version: '14.50.35717.0' },
            intent: { ...result.status.intent, enabled: trialFixture.loaded, trial: { judgment: trialFixture.enabled, execution: trialFixture.enabled } },
            capabilities: { ...result.status.capabilities, judgment: capability(trialFixture.enabled), execution: capability(trialFixture.enabled) },
          };
          result.models = [
            { id: 'spark-x2.5-1.7B', selector: 'local/spark-x2.5-1.7b', name: 'Spark-X2.5-1.7B', tier: '1.7B', installed: true },
            { id: 'spark-x2.5-4B', selector: 'local/spark-x2.5-4b', name: 'Spark-X2.5-4B', tier: '4B', installed: true },
          ];
        }
        await route.fulfill({ response, json: result });
      });
      if (language === 'zh' && width === 1600) {
        trialFixture = { loaded: false, enabled: false, unavailable: false, downloading: false, enables: 0, disables: 0, loads: 0 };
        await page.route('**/api/local-model/enable', async route => {
          await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ schemaVersion: 1, status: { intent: { selected: 'spark-x2.5-1.7B', enabled: true } } }) });
        });
        await page.route('**/api/local-model/load', async route => {
          trialFixture.loaded = true; trialFixture.loads++;
          await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ schemaVersion: 1, status: { runtime: 'ready', intent: { selected: 'spark-x2.5-1.7B', enabled: true } } }) });
        });
        await page.route('**/api/local-model/check', async route => {
          const cap = { state: trialFixture.unavailable ? 'unavailable' : 'unknown', trial: !trialFixture.unavailable, trialEvidence: !trialFixture.unavailable };
          await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ schemaVersion: 1, status: { capabilities: { judgment: cap, execution: cap } } }) });
        });
        await page.route('**/api/local-model/trial/enable', async route => {
          assert.deepEqual(route.request().postDataJSON(), {});
          trialFixture.enabled = true; trialFixture.enables++;
          await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ schemaVersion: 1, status: {} }) });
        });
        await page.route('**/api/local-model/trial/disable', async route => {
          assert.deepEqual(route.request().postDataJSON(), {});
          trialFixture.enabled = false; trialFixture.disables++;
          await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ schemaVersion: 1, status: {} }) });
        });
        await page.route('**/api/local-model/disable', async route => {
          trialFixture.loaded = false; trialFixture.enabled = false;
          await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ schemaVersion: 1, status: {} }) });
        });
        await page.evaluate(() => window.routerLocalOpen());
        assert.equal(await page.evaluate(() => !!(document.querySelector('#localInstalledWrap').compareDocumentPosition(document.querySelector('#localDeviceGuide')) & Node.DOCUMENT_POSITION_FOLLOWING)), true, 'configured on-device models list precedes device guide');
        assert.deepEqual(await page.locator('#localInstalledModels .row.local-provider .name').allTextContents(), ['Spark-X2.5-1.7B', 'Spark-X2.5-4B']);
        const firstSwitch = page.locator('#localInstalledModels [data-action="toggle-model"]').first();
        assert.equal(await firstSwitch.getAttribute('aria-checked'), 'false');
        await firstSwitch.click();
        await page.waitForFunction(() => document.querySelector('#localInstalledModels [data-action="toggle-model"]')?.getAttribute('aria-checked') === 'true');
        assert.equal(trialFixture.loads, 1);
        assert.equal(trialFixture.enables, 1);
        await firstSwitch.click();
        await page.waitForFunction(() => document.querySelector('#localInstalledModels [data-action="toggle-model"]')?.getAttribute('aria-checked') === 'false');
        assert.equal(trialFixture.disables, 1);
        trialFixture.unavailable = true;
        await firstSwitch.click();
        await page.waitForFunction(() => document.querySelector('#localModelError')?.textContent === t('Capability acceptance suites have not been provided.'));
        assert.equal(await firstSwitch.getAttribute('aria-checked'), 'true', 'runtime remains loaded when capability check reports missing capability');
        trialFixture.downloading = true;
        await page.evaluate(() => window.routerLocalOpen());
        assert.equal(await page.locator('#localDownloadProgress progress.local-progress-bar').getAttribute('value'), '50');
        assert.ok((await page.locator('#localDownloadProgress').textContent()).includes('50.0 / 100.0 MiB (50%)'));
        trialFixture = null;
        await page.unroute('**/api/local-model/enable');
        await page.unroute('**/api/local-model/load');
        await page.unroute('**/api/local-model/check');
        await page.unroute('**/api/local-model/trial/enable');
        await page.unroute('**/api/local-model/trial/disable');
        await page.unroute('**/api/local-model/disable');
        actions.length = 0;
      }
      for (const [state, message] of [
        ['missing', 'Runtime dependencies are missing.'], ['preparing', 'Preparing runtime dependencies'],
        ['ready', 'Installed and verified'], ['canceled', 'Canceled'], ['failed', 'Runtime dependency download failed.'],
        ['corrupt', 'Runtime dependencies are damaged.'], ['architecture', 'Runtime dependency architecture does not match.'],
        ['version', 'Runtime dependency version does not match.'], ['layout', 'Runtime dependency layout is invalid.'],
        ['not_required', 'Not required on this platform'],
      ]) {
        dependency = { ...dependency, state };
        await page.evaluate(() => window.routerLocalOpen());
        const expected = await page.evaluate(key => t(key), message);
        assert.ok((await page.locator('#localModelDependencies').textContent()).includes(expected), `${language}/${state}`);
        assert.equal(await page.locator('#localInstalledModels [data-action="toggle-model"]').first().isEnabled(), ['ready', 'not_required'].includes(state));
      }
      assert.deepEqual(actions, [], 'reading dependency states never installs or loads');
      dependency = { ...dependency, state: 'corrupt', error: 'local_model_dependency_corrupt' };
      let repairErrorCode = 'local_model_dependency_corrupt';
      await page.route('**/api/local-model/install', async route => {
        assert.deepEqual(route.request().postDataJSON(), {});
        await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: { code: repairErrorCode } }) });
      });
      const clickFailedRepair = async (code, message) => {
        const observed = { language, width, code, consoleErrors: [] };
        expectedInstallFailure = observed;
        try {
          const [response] = await Promise.all([
            page.waitForResponse(response => response.url() === base + '/api/local-model/install' && response.request().method() === 'POST'),
            page.locator('#localModelContent [data-action=install]').click(),
          ]);
          assert.equal(response.status(), 409, 'explicit repair receives the injected HTTP failure');
          assert.deepEqual(await response.json(), { error: { code } });
          assert.equal(await response.finished(), null, 'injected HTTP response is fully received');
          await page.waitForFunction(key => document.querySelector('#localModelError').textContent === t(key), message);
          assert.ok(observed.consoleErrors.length <= 1, 'one injected response emits at most one resource-error console message');
          expectedInstallFailures.push(observed);
        } finally { expectedInstallFailure = null; }
      };
      await page.evaluate(() => window.routerLocalOpen());
      await clickFailedRepair(repairErrorCode, 'Runtime dependencies are damaged.');
      assert.deepEqual(actions, ['/api/local-model/install'], 'only explicit repair invokes the existing install action');
      dependency = { ...dependency, state: 'failed', error: 'local_model_dependency_download_failed' };
      repairErrorCode = 'local_model_dependency_download_failed';
      await page.evaluate(() => window.routerLocalOpen());
      await clickFailedRepair(repairErrorCode, 'Runtime dependency download failed.');
      assert.deepEqual(actions, ['/api/local-model/install', '/api/local-model/install'], 'download failure is shown only after a second explicit repair');
      assert.equal((await status()).intent.enabled, false, 'render fixtures do not enable the real runtime');
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false);
      await page.close();
      assert.equal((await fetch(base + '/healthz')).status, 200, 'closing page keeps Router alive');
    }
  }
  const installedPage = await browser.newPage({ locale: 'en-US' });
  installedPage.on('pageerror', error => errors.push(error.message));
  let installedDetections = 0;
  await installedPage.route('**/api/local-model', async route => {
    const response = await route.fetch();
    const result = await response.json(); result.status.installation = 'installed';
    await route.fulfill({ response, json: result });
  });
  await installedPage.route('**/api/local-model/detect', async route => {
    installedDetections++;
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ schemaVersion: 1, device: deviceReport('1.7B', 'passed') }) });
  });
  await skipTutorial(installedPage); await installedPage.goto(`${base}/?locale=en&view=local-model`);
  await installedPage.waitForFunction(() => document.querySelector('#service').classList.contains('ok'));
  await installedPage.waitForFunction(() => !!managementToken && !!serviceInfo);
  await installedPage.locator('#localModelChoices button').first().waitFor();
  await installedPage.evaluate(() => window.routerLocalOpen());
  assert.equal(installedDetections, 0, 'opening and refreshing an installed model does not automatically probe the device');
  await installedPage.locator('#localDeviceGuide [data-action=detect]').click();
  await installedPage.waitForFunction(() => !document.querySelector('#localDeviceGuide [data-action=detect]').disabled);
  assert.equal(installedDetections, 1, 'installed models still allow explicit device detection');
  await installedPage.close();
  assert.equal(expectedInstallFailures.length, 20, 'both controlled HTTP failures are verified for every language and viewport');
  t.diagnostic(`Verified ${expectedInstallFailures.length} injected install HTTP 409 responses; observed ${expectedInstallFailures.reduce((count, failure) => count + failure.consoleErrors.length, 0)} matching resource-error console messages.`);
  assert.deepEqual(errors, []);
});
