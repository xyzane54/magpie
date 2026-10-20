const { skipTutorial } = require('./returning-user.cjs');
// Build with make api-router, then run with node --test this file.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const net = require('node:net');
const http = require('node:http');
const { spawn, execFileSync } = require('node:child_process');
const { once } = require('node:events');
const { chromium, webkit } = require('playwright');

const binary = process.env.API_ROUTER_BINARY || path.resolve(__dirname, '../../..', 'dist/api-router');
for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ['chromium', 'webkit'])) {
  test(`embedded management page (${engine})`, { timeout: 180000 }, async (t) => {
    assert.ok(['chromium', 'webkit'].includes(engine));
    const home = await fs.mkdtemp(path.join(os.tmpdir(), 'api-router-ui-'));
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
    const workBuddyRoot = path.join(home, 'workbuddy');
    env.WORKBUDDY_CONFIG_DIR = workBuddyRoot;
    const layout = JSON.parse(execFileSync(binary, ['dirs'], { env, encoding: 'utf8' }));
    execFileSync(binary, ['config', 'init'], { env });
    const reservation = net.createServer();
    reservation.listen(0, '127.0.0.1');
    await once(reservation, 'listening');
    const address = `127.0.0.1:${reservation.address().port}`;
    await new Promise(resolve => reservation.close(resolve));
    const configFile = path.join(layout.config, 'config.json');
    const config = JSON.parse(await fs.readFile(configFile, 'utf8'));
    config.listen.address = address;
    await fs.writeFile(configFile, JSON.stringify(config), { mode: 0o600 });
    service = spawn(binary, ['serve'], { env, stdio: ['ignore', 'pipe', 'pipe'] });
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
    let modelRequests = 0, generationRequests = 0;
    const upstream = http.createServer(async (req, res) => {
      if (req.url === '/v1/chat/completions') {
        generationRequests++;
        res.setHeader('Content-Type', 'application/json');
        res.end(JSON.stringify({ id: 'probe', object: 'chat.completion', choices: [{ index: 0, message: { role: 'assistant', content: 'OK' }, finish_reason: 'stop' }] }));
        return;
      }
      modelRequests++;
      assert.equal(req.url, '/v1/models');
      assert.equal(req.headers.authorization, 'Bearer ui-test-cloud-key');
      assert.equal(req.headers['x-tenant'], 'ui-private-tenant');
      res.setHeader('Content-Type', 'application/json');
      res.end(JSON.stringify({ data: [{ id: 'cloud-model-a' }, { id: 'cloud-model-b' }] }));
    });
    upstream.listen(0, '127.0.0.1');
    await once(upstream, 'listening');
    t.after(() => new Promise(resolve => upstream.close(resolve)));
    const upstreamBase = `http://127.0.0.1:${upstream.address().port}/v1`;
    for (const language of ['zh', 'en', 'zh-TW', 'ja', 'de']) {
      const page = await browser.newPage({ locale: 'en-US' });
      const errors = [], requests = [];
      let sessionRequest;
      page.on('pageerror', error => errors.push(error.message));
      page.on('request', request => {
        const pathname = new URL(request.url()).pathname;
        requests.push(pathname);
        if (pathname === '/api/management/session' && request.method() === 'POST') sessionRequest = request;
      });
      for (const width of [1600, 1100, 375]) {
        await page.setViewportSize({ width, height: 950 });
        sessionRequest = null;
        await skipTutorial(page);
        await page.goto(`http://${address}/?locale=${language}&view=usage&tab=context`);
        await page.waitForFunction(() => document.querySelector('#service').classList.contains('ok'));
        assert.ok(sessionRequest, 'the page acquires its management session through the guarded handshake');
        const sessionHeaders = await sessionRequest.allHeaders();
        assert.equal(sessionHeaders.origin, `http://${address}`);
        assert.equal(sessionHeaders['sec-fetch-site'], 'same-origin');
        assert.equal(sessionHeaders['x-api-router-ui'], '1');
        assert.match(await page.title(), /API-router/);
        assert.deepEqual(await page.locator('#nav [data-view]').evaluateAll(nodes => nodes.map(node => node.dataset.view)), ['agents', 'providers', 'routing', 'gateway', 'local-model', 'usage']);
        assert.equal(await page.locator('#usagePane').isVisible(), true);
        assert.equal(new URL(page.url()).searchParams.get('tab'), 'usage');
        assert.equal(await page.locator('#contextPane, #usageTab-context, [data-tab=context]').count(), 0);
        assert.equal(await page.locator('#manage, #manageSettings, #modal, #token, #connect').count(), 0);
        assert.equal(await page.locator('body').evaluate(node => node.classList.contains('side-nav')), width >= 1024);
        if (width >= 1024) {
          assert.equal(await page.locator('.nav-sub .nav-s').count(), 2);
          assert.equal(await page.locator('#crumb b').textContent(), await page.locator('#usageTab-usage').textContent());
          assert.equal(Math.round(await page.locator('#side').evaluate(node => node.getBoundingClientRect().width)), 212);
        }
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
        const appCSS = await page.request.get(`http://${address}/app.css`);
        assert.deepEqual(await appCSS.body(), await fs.readFile(path.resolve(__dirname, '../../gui/assets/app.css')));
        await page.waitForFunction(() => !!managementToken && !!serviceInfo);
        assert.equal(await page.evaluate(() => serviceInfo.service), 'api-router');
        assert.equal(page.url().includes(token), false);
        await page.locator('#nav [data-view=gateway]').click();
        assert.equal(await page.locator('#service').evaluate(node => node.classList.contains('ok')), true);
        assert.equal(await page.locator('#model').textContent(), await page.evaluate(() => t('Ready')));
        assert.equal(await page.locator('#runtime').textContent(), await page.evaluate(() => t('Not connected yet')));
        assert.equal(await page.locator('#view-gateway .router-note').textContent(), await page.evaluate(() => t('Cloud requests are connected. Explicit 1.7B or 4B local routing and execution become available after enable, install and capability checks; redaction is separate.')));
        const stored = await page.evaluate(() => JSON.stringify({ ...localStorage, ...sessionStorage }));
        assert.equal(stored.includes(token), false);
        assert.equal((await page.content()).includes(token), false);
        if (language === 'zh' && width === 1600) {
          const beforeReconnect = requests.filter(route => route === '/api/management/session').length;
          await page.evaluate(() => { managementToken = 'f'.repeat(64); });
          await page.locator('#sync').click();
          await page.waitForFunction(token => managementToken === token && !!serviceInfo, token);
          assert.equal(requests.filter(route => route === '/api/management/session').length, beforeReconnect + 1);
          assert.equal(await page.locator('#manage, #manageSettings, #modal, #token, #connect').count(), 0);
        }
        await page.locator('#nav [data-view=agents]').click();
        await page.waitForFunction(() => document.querySelectorAll('#agents .row.agent.conn').length === 2);
        assert.deepEqual(await page.locator('#agents .row.agent.conn').evaluateAll(nodes => nodes.map(n => n.dataset.app)), ['loomy', 'workbuddy']);
        assert.equal(await page.locator('#agents [data-app="codex"], #agents [data-app="claude-code"], #agents [data-app="astudio"], #agents [data-app="dsh"], #addApp, #profileModal').count(), 0);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
        await page.locator('#nav [data-view=providers]').click();
        assert.equal(await page.locator('#addProvider').isDisabled(), false);
        await page.waitForFunction(() => document.querySelectorAll('#providerPresets .tile').length > 20);
        await page.locator('[data-preset=zhipu]').click();
        assert.equal(await page.locator('#cloudAdvanced').evaluate(node => node.open), false);
        await page.locator('#cloudAdvanced summary').click();
        assert.equal(await page.locator('#providerModal .dialog').evaluate(node => node.scrollWidth <= node.clientWidth + 1), true);
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
        await page.locator('#providerCancel').click();
        if (language === 'zh' && width === 1600) {
          await page.locator('[data-preset=zhipu]').click();
          assert.equal(await page.locator('#providerModal').isVisible(), true);
          assert.match(await page.locator('#cloudBase').inputValue(), /bigmodel/);
          await page.locator('#providerCancel').click();
          assert.equal(modelRequests, 0);
          await page.locator('[data-preset=custom]').click();
          await page.locator('#cloudName').fill('UI provider');
          await page.locator('#cloudBase').fill(upstreamBase);
          await page.locator('#cloudKey').fill('ui-test-cloud-key');
          await page.locator('#cloudAdvanced summary').click();
          await page.locator('#cloudHeaders').fill('{"X-Tenant":"ui-private-tenant"}');
          await page.locator('#cloudExtraBody').fill('{"prompt_cache_key":"${sessionId}"}');
          await page.locator('#cloudProxyMode button').nth(1).click();
          assert.equal(await page.locator('#cloudModel, #cloudFetchModels, #cloudModels').count(), 0);
          const providerSave = page.waitForRequest(request => request.method() === 'POST' && new URL(request.url()).pathname === '/api/cloud-providers');
          await page.locator('#providerSave').click();
          const providerSaveRequest = await providerSave;
          const providerSaveHeaders = await providerSaveRequest.allHeaders();
          assert.equal(providerSaveHeaders.origin, `http://${address}`);
          assert.equal(providerSaveHeaders['sec-fetch-site'], 'same-origin');
          assert.equal(Object.hasOwn(providerSaveRequest.postDataJSON(), 'model'), false);
          await page.waitForFunction(() => document.querySelector('#providerModal').hidden && document.querySelectorAll('#providers [data-provider]').length === 1);
          assert.equal(modelRequests, 1, 'provider save automatically discovers and caches models');
          assert.equal(generationRequests, 0);
          assert.equal(await page.locator('#providers .sub').textContent(), upstreamBase);
          assert.equal(await page.locator('#providers .provider-models-summary').getAttribute('data-model-source'), 'discovered');
          assert.match(await page.locator('#providers .provider-models-summary').textContent(), /2/);
          assert.equal(await page.locator('#localProvidersSection').isHidden(), true);
          const ordinaryConfig = await fs.readFile(configFile, 'utf8');
          assert.equal(ordinaryConfig.includes('ui-test-cloud-key'), false);
          assert.equal(ordinaryConfig.includes('ui-private-tenant'), false);
          await page.locator('#nav [data-view=agents]').click();
          const beforeGuide = await fs.readFile(configFile, 'utf8');
          const workBuddyRow = page.locator('#agents [data-app=workbuddy]');
          await workBuddyRow.locator('.ag-link').click();
          assert.equal(await workBuddyRow.locator('.ag-exp').isVisible(), true);
          assert.equal(await workBuddyRow.locator('.ag-pick').isVisible(), true);
          assert.equal((await workBuddyRow.locator('.ag-exp').textContent()).includes(token), false);
          assert.equal(await fs.readFile(configFile, 'utf8'), beforeGuide);
          await workBuddyRow.locator('.ag-link').click();
          assert.equal(await workBuddyRow.locator('.ag-exp').count(), 0);
          // Connect WorkBuddy via inline multi-select picker and one-click switch without upstream generation probe
          await page.locator('#agents [data-app=workbuddy] .field.ag-menu').click();
          await page.locator('.pop.am-pop .am-mr[data-model$="/cloud-model-b"]').click();
          await page.locator('#view-agents .row-head .label').click();
          const hostConnect = page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/hosts/auto');
          await page.locator('#agents [data-app=workbuddy] .lib-switch.ag-conn').click();
          const hostRes = await hostConnect;
          assert.equal(hostRes.status(), 200);
          assert.equal(generationRequests, 0, 'FastChecker connects host without upstream generation probe');
          const hostModels = JSON.parse(await fs.readFile(path.join(workBuddyRoot, 'models.json'), 'utf8'));
          assert.ok(hostModels.models.some(m => m.id.endsWith('/cloud-model-b')));
          await page.locator('#agents [data-app=workbuddy] .lib-switch.ag-conn').click();
          await page.waitForFunction(() => document.querySelector('#agents [data-app=workbuddy] .lib-switch.ag-conn').getAttribute('aria-checked') === 'false');
          await page.locator('#agents [data-app=workbuddy] .field.ag-menu').click();
          const clearWait = page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname === '/api/apps');
          await page.locator('.pop.am-pop .am-reset').click();
          await clearWait;
          await page.locator('#view-agents .row-head .label').click();
          await page.locator('#nav [data-view=providers]').click();
          await page.locator('#providers [data-provider] button').click();
          await page.locator('#providerDelete').click();
          await page.locator('#providerDelete').click();
          await page.waitForFunction(() => document.querySelector('#providerModal').hidden && document.querySelectorAll('#providers [data-provider]').length === 0);
          const cleanCfgRaw = await fs.readFile(configFile, 'utf8');
          const cfgWithLocal = JSON.parse(cleanCfgRaw);
          cfgWithLocal.localModels = [{ id: 'spark-x2.5-1.7b', installed: true, enabled: true, status: 'ready' }];
          await fs.writeFile(configFile, JSON.stringify(cfgWithLocal), { mode: 0o600 });
          await page.evaluate(() => window.routerProvidersOpen());
          await page.waitForFunction(() => !document.querySelector('#localProvidersSection').hidden && document.querySelectorAll('#localProviders [data-local-model]').length === 1);
          assert.equal(await page.locator('#localProviders [data-local-model="spark-x2.5-1.7b"] .name').textContent(), 'Spark-X2.5-1.7B');
          assert.equal(await page.locator('#localProviders [data-local-model="spark-x2.5-1.7b"] .sub').textContent(), 'local/spark-x2.5-1.7b');
          await page.locator('#localProviders [data-local-model="spark-x2.5-1.7b"] [data-action="open-local-model"]').click();
          assert.equal(await page.locator('#view-local-model').isVisible(), true);
          await fs.writeFile(configFile, cleanCfgRaw, { mode: 0o600 });
          await page.locator('#nav [data-view=providers]').click();
          await page.waitForFunction(() => document.querySelector('#localProvidersSection').hidden);
        }
        await page.locator('#nav [data-view=routing]').click();
        assert.equal(await page.locator('#view-routing').isVisible(), true);
        assert.equal(await page.locator('#routingGroups').count(), 1);
        const routingCSS = await page.request.get(`http://${address}/plugins/routing/routing.css`);
        assert.deepEqual(await routingCSS.body(), await fs.readFile(path.resolve(__dirname, '../../gui/assets/plugins/routing/routing.css')));
        await page.locator('#nav [data-view=usage]').click();
        const tab = width >= 1024 ? '.nav-s[data-of=usageTab-usage]' : '#usageTab-usage';
        await page.locator(tab).click();
        assert.equal(await page.locator('#usagePane').isVisible(), true);
        await page.locator(width >= 1024 ? '.nav-s[data-of=usageTab-requests]' : '#usageTab-requests').click();
        assert.equal(await page.locator('#ledgerPane').isVisible(), true);
        await page.locator('#prefs').click();
        assert.equal(await page.locator('#setPage-general').isVisible(), true);
        await page.locator('#themeSegs button').nth(2).click();
        assert.equal(await page.locator('html').getAttribute('data-theme'), 'dark');
        await page.locator('#themeSegs button').nth(1).click();
      }
      assert.equal(requests.some(route => /^\/(boot|start|kernel|context)\.js/.test(route) || /^\/api\/(providers|tune|context|plugins|agents|sessions|update)/.test(route)), false, requests.join(', '));
      assert.deepEqual(errors, []);
      await page.close();
    }
    const page = await browser.newPage();
    await skipTutorial(page); await page.goto(`http://${address}/`);
    assert.equal(await page.locator('#usagePane').isVisible(), true);
    await page.evaluate(() => localStorage.setItem('api-router.ui', JSON.stringify({ view: 'context', usageTab: 'context' })));
    await page.reload();
    await page.waitForFunction(() => !!managementToken && !!serviceInfo);
    assert.equal(await page.locator('#usagePane').isVisible(), true);
    assert.deepEqual(await page.evaluate(() => { const saved = JSON.parse(localStorage.getItem('api-router.ui')); return [saved.view, saved.usageTab]; }), ['usage', 'usage']);
    await skipTutorial(page); await page.goto(`http://${address}/?view=routing`);
    assert.equal(await page.locator('#view-routing').isVisible(), true);
    await skipTutorial(page); await page.goto(`http://${address}/?view=plugins`);
    assert.equal(await page.locator('#view-plugins').count(), 0);
    for (const route of ['/api/plugins', '/boot.js', '/kernel.js', '/plugins/usage/page.js']) {
      assert.equal((await page.request.get(`http://${address}${route}`)).status(), 404);
    }
    service.kill('SIGTERM');
    await exited;
    await page.locator('#sync').click();
    await page.waitForFunction(() => document.querySelector('#status').textContent !== '');
    assert.equal(await page.locator('#sideGw').evaluate(node => node.classList.contains('off')), true);
    await assert.rejects(fs.stat(tokenFile), { code: 'ENOENT' });
  });
}
