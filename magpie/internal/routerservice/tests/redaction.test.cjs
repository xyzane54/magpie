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

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ['chromium', 'webkit'])) {
  test(`redaction intent and integrated pending state (${engine})`, { timeout: 180000 }, async t => {
    const home = await fs.mkdtemp(path.join(os.tmpdir(), 'api-router-redaction-ui-'));
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
    const configFile = path.join(layout.config, 'config.json'), config = JSON.parse(await fs.readFile(configFile, 'utf8'));
    config.listen.address = new URL(base).host;
    // Existing intent is a persisted fixture, never a claim of model quality.
    config.appRevision = 2;
    for (const [index, id] of ['codex', 'dsh'].entries()) config.apps[id] = { schemaVersion: 1, id, revision: index + 1, name: id, cloud: { providerId: '', model: '' }, credentialMode: 'stored', localRuntime: 'shared', redactionEnabled: true, referencePrice: null };
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
    const state = async () => (await (await fetch(base + '/api/redaction', { headers: { Authorization: 'Bearer ' + token } })).json());
    assert.equal((await fetch(base + '/api/redaction')).status, 401);
    let current = await state();
    assert.equal(current.capability, 'unknown'); assert.equal(current.productionIntegrated, true); assert.equal(current.modelReady, false); assert.equal(current.recognitionReady, false); assert.equal(current.statsAvailable, true); assert.equal(current.statistics.processed, 0);
    assert.equal(current.intents.filter(p => p.enabled).length, 2);
    assert.ok(current.intents.every(p => p.active === false));
    assert.equal((await fetch(base + '/api/redaction', { method: 'POST', headers: { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' }, body: JSON.stringify({ edits: [{ id: 'loomy', revision: 0, enabled: true }] }) })).status, 409);
    browser = await (engine === 'webkit' ? webkit : chromium).launch();
    const errors = [];
    async function authenticate(page, language = 'en', width = 1600, expectedRows = 15) {
      const authenticationErrors = [];
      page.on('pageerror', error => authenticationErrors.push(error.message));
      await page.setViewportSize({ width, height: 950 });
      await skipTutorial(page); await page.goto(`${base}/?locale=${language}&view=local-model`);
      await page.waitForFunction(() => document.querySelector('#service').classList.contains('ok'));
      assert.equal(await page.locator('#view-agents #redactionPanel').count(), 0);
      assert.equal(await page.locator('#view-local-model #redactionPanel').count(), 1);
      await page.waitForFunction(() => !!managementToken && !!serviceInfo);
      try { await page.waitForFunction(expectedRows => document.querySelectorAll('#redactionContent > .row').length === expectedRows, expectedRows); }
      catch (failure) {
        t.diagnostic(JSON.stringify({ errors: authenticationErrors, state: await page.evaluate(() => ({ tokenPresent: !!managementToken, view, language: document.documentElement.lang, browserLanguage: navigator.language, panelStatus: document.querySelector('#redactionStatus')?.textContent, panelRows: document.querySelectorAll('#redactionContent > .row').length, appStatus: document.querySelector('#appStatus')?.textContent })) }));
        throw failure;
      }
      await page.waitForFunction(() => !document.querySelector('#redactionPanel').hidden);
    }
    for (const language of ['zh', 'en', 'zh-TW', 'ja', 'de']) {
      for (const width of [1600, 375]) {
        const page = await browser.newPage({ locale: 'en-US' });
        page.on('pageerror', error => errors.push(error.message));
        const external = []; page.on('request', req => { if (new URL(req.url()).origin !== base) external.push(req.url()); });
        await authenticate(page, language, width);
        assert.equal(await page.locator('#redactionTitle').textContent(), await page.evaluate(() => t('Redaction intent')));
        assert.equal(await page.locator('#redactionContent .router-state').filter({ hasText: await page.evaluate(() => t('Inactive')) }).count(), 6);
        assert.ok((await page.locator('#redactionContent').textContent()).includes(await page.evaluate(() => t('Production redaction is connected but recognition acceptance is pending.'))));
        assert.ok((await page.locator('#redactionContent .redaction-metrics').textContent()).includes(await page.evaluate(() => t('Session cache hit rate'))));
        assert.ok((await page.locator('#redactionContent .redaction-metrics').textContent()).includes(await page.evaluate(() => t('Average redaction duration (ms)'))));
        assert.ok((await page.locator('#redactionContent .redaction-metrics').textContent()).includes(await page.evaluate(() => t('Blocked reasons'))));
        assert.equal(await page.locator('#redactionDisableAll').isEnabled(), true);
        await page.locator('#nav [data-view=agents]').click();
        assert.equal(await page.locator('#redactionPanel').isVisible(), false);
        await page.locator('#nav [data-view=local-model]').click();
        await page.waitForFunction(() => !document.querySelector('#redactionPanel').hidden);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false, `${language} ${width}: overflow`);
        assert.equal((await page.content()).includes(token), false);
        assert.equal(await page.evaluate(token => JSON.stringify({ ...localStorage, ...sessionStorage }).includes(token), token), false);
        assert.deepEqual(external, []);
        if (process.env.ARTIFACT_DIR && ['zh', 'en'].includes(language)) {
          await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
          await page.locator('#redactionPanel').scrollIntoViewIfNeeded();
          await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `redaction-${engine}-${language}-${width}.png`), fullPage: true });
        }
        await page.close();
      }
    }
    // UI-only server projections exercise trial presentation without loading a
    // model, enabling production quality, or changing the real default service.
    const trialNotice = 'Experimental trial: formal quality is not accepted. Misses and false positives are possible. Invalid results still block cloud requests. The total redaction budget is 30 seconds.';
    for (const language of ['zh', 'en', 'zh-TW', 'ja', 'de']) {
      for (const width of [1600, 375]) {
        const page = await browser.newPage({ locale: 'en-US' });
        page.on('pageerror', error => errors.push(error.message));
        const external = []; page.on('request', req => { if (new URL(req.url()).origin !== base) external.push(req.url()); });
        let projection = { ...current, capability: 'experimental', experimental: true, recognitionReady: false, formalQualityAccepted: false, trialReady: true, modelReady: true, budgetMs: 30000 };
        await page.route('**/api/redaction', route => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(projection) }));
        await authenticate(page, language, width, 18);
        const notice = await page.evaluate(key => t(key), trialNotice);
        assert.ok((await page.locator('#redactionContent').textContent()).includes(notice));
        const recognition = page.locator('#redactionContent > .row').filter({ has: page.locator('.who').filter({ hasText: await page.evaluate(() => t('Recognition acceptance')) }) });
        assert.equal(await recognition.locator('.router-state').textContent(), await page.evaluate(() => t('unknown')));
        assert.ok((await page.locator('#redactionContent').textContent()).includes(await page.evaluate(() => t('Select an application Profile to enable the experimental trial.'))));
        const savedIntents = projection.intents;
        projection = { ...projection, intents: savedIntents.map(intent => ({ ...intent, enabled: false, active: false })) };
        await page.locator('#sync').click();
        await page.waitForFunction(() => document.querySelector('#redactionPanel').hidden);
        assert.equal(await page.locator('#redactionSetup').isVisible(), true);
        projection = { ...projection, intents: savedIntents, trialReady: false };
        await page.locator('#localModelContent [data-action=refresh]').click();
        await page.waitForFunction(() => !document.querySelector('#redactionPanel').hidden);
        assert.ok((await page.locator('#redactionContent').textContent()).includes(await page.evaluate(() => t('The experimental trial is not ready. Existing intent is retained and new enabling is blocked.'))));
        assert.equal(projection.intents.filter(p => p.enabled).length, 2);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1), false, `trial ${language} ${width}: overflow`);
        assert.equal((await page.content()).includes(token), false);
        assert.equal(await page.evaluate(token => JSON.stringify({ ...localStorage, ...sessionStorage }).includes(token), token), false);
        assert.deepEqual(external, []);
        if (language === 'en' && width === 1600) {
          // A malformed projection cannot convert unknown quality or a client
          // readiness claim into an enabled control.
          const valid = { ...projection, trialReady: true };
          for (const inconsistent of [{ formalQualityAccepted: true }, { recognitionReady: true }, { experimental: false }, { budgetMs: 15000 }, { capability: 'client-claimed-experimental' }]) {
            projection = valid;
            await page.locator('#sync').click();
            await page.waitForFunction(() => (document.querySelector('#redactionContent')?.textContent || '').includes(t('Select an application Profile to enable the experimental trial.')));
            projection = { ...valid, ...inconsistent };
            await page.locator('#sync').click();
            await page.waitForFunction(() => document.querySelector('#redactionStatus').textContent === t('Redaction management unavailable. Refresh and check configuration.'));
            assert.equal(await page.locator('#redactionDisableAll').isDisabled(), true);
          }
        }
        await page.close();
      }
    }
    // Pin the same browser locale as the matrix. WSL's C.UTF-8 default can
    // expose en-US@posix, which the inherited client cannot pass to Intl.
    const page = await browser.newPage({ locale: 'en-US' }); await authenticate(page);
    // A conflict never silently overwrites another editor and disables stale UI.
    let releaseConflict;
    await page.route('**/api/redaction', async route => {
      if (route.request().method() === 'POST') {
        await new Promise(resolve => { releaseConflict = resolve; });
        return route.fulfill({ status: 409, contentType: 'application/json', body: '{"error":{"code":"profile_conflict"}}' });
      }
      return route.continue();
    });
    const conflictRequest = page.waitForRequest(request => request.method() === 'POST' && new URL(request.url()).pathname === '/api/redaction');
    await page.locator('#redactionDisableAll').click();
    await conflictRequest;
    await page.locator('#sync').click();
    await page.waitForFunction(() => !busy);
    releaseConflict();
    await page.waitForFunction(() => document.querySelector('#redactionStatus').textContent === t('Profiles changed. Refresh before changing redaction intent.'));
    assert.equal((await state()).intents.filter(p => p.enabled).length, 2);
    assert.equal(await page.locator('#redactionDisableAll').isDisabled(), true);
    await page.unroute('**/api/redaction');
    await page.locator('#sync').click(); await page.waitForFunction(() => !document.querySelector('#redactionDisableAll').disabled);
    await page.locator('#redactionDisableAll').click();
    await page.waitForFunction(() => document.querySelector('#redactionStatus').textContent === t('Redaction intents disabled.'));
    current = await state(); assert.ok(current.intents.every(p => p.enabled === false && p.active === false));
    assert.equal(await page.locator('#redactionDisableAll').isDisabled(), true);
    assert.equal(await page.locator('#redactionPanel').isVisible(), false);
    assert.equal(await page.locator('#redactionSetup').isVisible(), true);
    assert.equal(current.capability, 'unknown');
    await page.reload(); await page.waitForFunction(() => document.querySelector('#service').classList.contains('ok'));
    await page.waitForFunction(() => !!managementToken && !!serviceInfo && document.querySelectorAll('#redactionContent > .row').length > 0);
    assert.equal(await page.locator('#redactionDisableAll').isDisabled(), true);
    assert.equal(await page.locator('#redactionPanel').isVisible(), false);
    assert.equal(await page.locator('#redactionSetup').isVisible(), true);
    assert.equal(await page.evaluate(token => JSON.stringify({ ...localStorage, ...sessionStorage }).includes(token), token), false);
    assert.deepEqual(errors, []);
    const persisted = JSON.parse(await fs.readFile(configFile, 'utf8'));
    assert.equal(persisted.apps.codex.redactionEnabled, false); assert.equal(persisted.apps.dsh.redactionEnabled, false);
    assert.equal(output.includes(token), false);
  });
}
