const { skipTutorial } = require('./returning-user.cjs');
// Product UI regression: published models are not silently treated as usable.
// All service state and upstream traffic stay in a disposable loopback fixture.
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
  test(`Profile verifies selected model before saving (${engine})`, { timeout: 180000 }, async t => {
    const home = await fs.mkdtemp(path.join(os.tmpdir(), 'api-router-profile-probe-'));
    let service, exited, browser;
    const calls = [], profileCalls = [];
    const upstream = http.createServer(async (req, res) => {
      if (req.url === '/v1/models') {
        res.setHeader('Content-Type', 'application/json');
        res.end(JSON.stringify({ data: [{ id: 'published-but-rejected' }, { id: 'usable-model' }] })); return;
      }
      let body = ''; for await (const chunk of req) body += chunk;
      const draft = JSON.parse(body); calls.push(draft);
      assert.equal(req.url, '/v1/chat/completions');
      assert.equal(req.headers.authorization, 'Bearer probe-fixture-key');
      profileCalls.push(draft);
      assert.equal(draft.stream, true);
      assert.equal(draft.max_tokens, 256);
      assert.deepEqual(draft.messages, [{ role: 'user', content: 'Reply with OK.', agent: 'connection-check' }]);
      if (draft.model === 'published-but-rejected') {
        res.writeHead(400, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: { message: 'PRIVATE upstream body', code: 'invalid_model' } })); return;
      }
      res.setHeader('Content-Type', 'text/event-stream');
      res.end('data: {"id":"probe","model":"usable-model","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":null}]}\n\ndata: {"id":"probe","model":"usable-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n');
    });
    t.after(async () => {
      if (browser) await browser.close();
      if (service) { if (service.exitCode === null) service.kill('SIGTERM'); await exited; }
      await new Promise(resolve => upstream.close(resolve)); await fs.rm(home, { recursive: true, force: true });
    });
    upstream.listen(0, '127.0.0.1'); await once(upstream, 'listening');
    const env = { ...process.env, HOME: home, USERPROFILE: home };
    for (const key of ['XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'APPDATA', 'LOCALAPPDATA']) env[key] = path.join(home, key);
    // Never inherit a real host override from the developer's shell.
    const workbuddy = path.join(home, 'hosts', 'workbuddy');
    env.WORKBUDDY_CONFIG_DIR = workbuddy; env.CODEBUDDY_CONFIG_DIR = workbuddy;
    env.LOOMY_CONFIG_DIR = path.join(home, 'hosts', 'loomy'); env.LOOMY_OPENCODE_DIR = env.LOOMY_CONFIG_DIR;
    env.PUBLIC = path.join(home, 'Public');
    await fs.mkdir(workbuddy, { recursive: true });
    await fs.writeFile(path.join(workbuddy, 'models.json'), JSON.stringify({ models: [] }));
    await fs.writeFile(path.join(workbuddy, 'settings.json'), JSON.stringify({ model: 'fixture-before-router' }));
    const layout = JSON.parse(execFileSync(binary, ['dirs'], { env, encoding: 'utf8' }));
    execFileSync(binary, ['config', 'init'], { env });
    const reservation = net.createServer(); reservation.listen(0, '127.0.0.1'); await once(reservation, 'listening');
    const address = `127.0.0.1:${reservation.address().port}`; await new Promise(resolve => reservation.close(resolve));
    const configFile = path.join(layout.config, 'config.json'), config = JSON.parse(await fs.readFile(configFile, 'utf8'));
    config.listen.address = address; await fs.writeFile(configFile, JSON.stringify(config), { mode: 0o600 });
    service = spawn(binary, ['serve'], { env, stdio: ['ignore', 'pipe', 'pipe'] }); exited = once(service, 'exit');
    let output = '', token; service.stdout.on('data', chunk => { output += chunk; }); service.stderr.on('data', chunk => { output += chunk; });
    for (let i = 0; i < 200; i++) {
      if (service.exitCode !== null) assert.fail(`service exited: ${output}`);
      try { token = (await fs.readFile(path.join(layout.state, 'router.lock', 'management-token'), 'utf8')).trim(); break; } catch {}
      await new Promise(resolve => setTimeout(resolve, 25));
    }
    assert.ok(token, `no management token: ${output}`);
    const origin = `http://${address}`, headers = { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' };
    const providerResult = await fetch(origin + '/api/cloud-providers', { method: 'POST', headers, body: JSON.stringify({ name: 'Probe fixture', protocol: 'chat', baseURL: `http://127.0.0.1:${upstream.address().port}/v1`, apiKey: 'probe-fixture-key', proxyMode: 'direct' }) });
    assert.equal(providerResult.status, 200); const { provider } = await providerResult.json();
    const discoverRes = await fetch(origin + '/api/cloud-providers/models', { method: 'POST', headers, body: JSON.stringify({ id: provider.id, persist: true, fallback: true }) });
    assert.equal(discoverRes.status, 200);
    const discovered = await discoverRes.json();
    assert.deepEqual(discovered.models, ['published-but-rejected', 'usable-model']);

    // Verify /api/apps/check still distinguishes rejected vs usable upstream models without leaking private errors
    const rejectedCheck = await fetch(origin + '/api/apps/check', {
      method: 'POST', headers,
      body: JSON.stringify({ id: 'workbuddy', name: 'WorkBuddy', revision: 0, credentialMode: 'stored', cloud: { providerId: provider.id, model: 'published-but-rejected' } }),
    });
    assert.equal(rejectedCheck.status, 400);
    const rejectedText = await rejectedCheck.text();
    assert.doesNotMatch(rejectedText, /PRIVATE|probe-fixture-key/);

    const usableCheck = await fetch(origin + '/api/apps/check', {
      method: 'POST', headers,
      body: JSON.stringify({ id: 'workbuddy', name: 'WorkBuddy', revision: 0, credentialMode: 'stored', cloud: { providerId: provider.id, model: 'usable-model' } }),
    });
    assert.equal(usableCheck.status, 200);
    const usableBody = await usableCheck.json();
    assert.equal(usableBody.verified, true);
    assert.equal(profileCalls.length, 2);

    browser = await (engine === 'webkit' ? webkit : chromium).launch({ ...(engine === 'chromium' && process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}) });
    for (const language of ['zh', 'en', 'zh-TW', 'ja', 'de']) for (const width of [1600, 1100, 375]) {
      await fs.writeFile(path.join(workbuddy, 'models.json'), JSON.stringify({ models: [] }));
      await fs.writeFile(path.join(workbuddy, 'settings.json'), JSON.stringify({ model: 'fixture-before-router' }));
      const page = await browser.newPage({ locale: 'en-US', viewport: { width, height: 950 } });
      const errors = [], saves = [], hosts = [], hostResponses = [];
      const beforeCalls = calls.length;
      page.on('pageerror', error => errors.push(error.message));
      page.on('request', request => {
        if (request.method() !== 'POST') return;
        const route = new URL(request.url()).pathname;
        if (route === '/api/apps') saves.push(request.postDataJSON());
        if (route === '/api/hosts/auto') hosts.push(request.postDataJSON());
      });
      page.on('response', response => {
        const url = new URL(response.url());
        if (url.pathname === '/api/hosts/auto') hostResponses.push(response.status());
      });
      await skipTutorial(page); await page.goto(origin + `/?locale=${language}&view=agents`);
      await page.waitForFunction(() => !!managementToken && !!serviceInfo && !!document.querySelector('#agents [data-app=workbuddy] .field.ag-menu'));

      await page.locator('#agents [data-app=workbuddy] .field.ag-menu').click();
      await page.waitForSelector(`.pop.am-pop [data-section="provider-${provider.id}"]`);
      const candidateModels = await page.locator(`.pop.am-pop [data-section="provider-${provider.id}"] .am-mr`).evaluateAll(nodes => nodes.map(n => n.dataset.model));
      assert.deepEqual(candidateModels, [`${provider.id}/published-but-rejected`, `${provider.id}/usable-model`]);

      const targetModel = `${provider.id}/usable-model`;
      const isSelected = await page.locator(`.pop.am-pop .am-mr[data-model="${targetModel}"]`).getAttribute('aria-selected') === 'true';
      if (!isSelected) {
        const saved = page.waitForResponse(r => new URL(r.url()).pathname === '/api/apps' && r.request().method() === 'POST');
        await page.locator(`.pop.am-pop .am-mr[data-model="${targetModel}"]`).click();
        await saved;
        assert.equal(saves.length, 1);
        assert.deepEqual(saves[0].models, [targetModel]);
      }
      await page.keyboard.press('Escape');

      // Connect WorkBuddy via toggle switch (FastChecker non-blocking host connection)
      const sw = page.locator('#agents [data-app=workbuddy] .lib-switch.ag-conn');
      if ((await sw.getAttribute('aria-checked')) !== 'true') {
        const connected = page.waitForResponse(r => new URL(r.url()).pathname === '/api/hosts/auto' && r.request().method() === 'POST');
        await sw.click();
        await connected;
      } else {
        // Disconnect then reconnect so each iteration exercises the host write
        const disconnected = page.waitForResponse(r => new URL(r.url()).pathname === '/api/hosts/auto' && r.request().method() === 'POST');
        await sw.click();
        await disconnected;
        const connected = page.waitForResponse(r => new URL(r.url()).pathname === '/api/hosts/auto' && r.request().method() === 'POST');
        await sw.click();
        await connected;
      }
      assert.equal(hostResponses.at(-1), 200);
      assert.equal(calls.length, beforeCalls, 'FastChecker host connection must not trigger blocking upstream generation calls');

      const models = await fs.readFile(path.join(workbuddy, 'models.json'), 'utf8');
      const target = JSON.parse(models).models.find(m => m.id === targetModel);
      assert.ok(target, 'WorkBuddy models.json must contain the selected provider/model entry');
      assert.equal(target.url, origin + '/app/workbuddy/v1');
      assert.equal(JSON.parse(await fs.readFile(path.join(workbuddy, 'settings.json'), 'utf8')).model, `custom-local:${targetModel}`);
      assert.equal(models.includes(token) || models.includes('probe-fixture-key'), false, 'host configuration must not import management or upstream credentials');

      assert.equal(await page.locator('body').evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
      assert.equal(errors.length, 0, errors.join('\n'));
      await page.close();
    }
  });
}
