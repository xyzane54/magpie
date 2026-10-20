const { skipTutorial } = require('./returning-user.cjs');
// Installed-host acceptance is separate. This regression uses the real Router,
// disposable local OpenCode files and a loopback upstream with fixture keys.
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
const model = 'loomy-fixture-model';
const fixtureKey = 'loomy-browser-upstream-fixture-key';
const originalSecret = 'loomy-browser-original-fixture-secret';
const original = {
  model: 'original/model', small_model: 'original/small',
  provider: { original: { npm: '@ai-sdk/openai-compatible', options: { baseURL: 'https://original.invalid/v1', apiKey: originalSecret }, models: { model: { name: 'model' } } } },
  keep: '<img src=x onerror="window.fixtureExecuted=true">',
};

async function jsonFiles(directory) {
  try { return (await fs.readdir(directory)).filter(name => name.endsWith('.json')).sort(); }
  catch (error) { if (error.code === 'ENOENT') return []; throw error; }
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ['chromium', 'webkit'])) {
  test(`Loomy explicit connection and independent recovery (${engine})`, { timeout: 180000 }, async t => {
    assert.ok(['chromium', 'webkit'].includes(engine));
    const home = await fs.mkdtemp(path.join(os.tmpdir(), 'api-router-loomy-ui-'));
    let service, exited, browser;
    const calls = [], upstreamErrors = [];
    const upstream = http.createServer(async (req, res) => {
      try {
        if (req.url === '/v1/models') { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify({ data: [{ id: model }] })); return; }
        let raw = ''; for await (const chunk of req) raw += chunk;
        const body = JSON.parse(raw); calls.push(body);
        if (req.url !== '/v1/chat/completions') upstreamErrors.push('unexpected upstream path');
        if (req.headers.authorization !== 'Bearer ' + fixtureKey || req.headers['x-api-key']) upstreamErrors.push('wrong upstream credential');
        if (body.model !== model) upstreamErrors.push('wrong upstream model');
        if (upstreamErrors.length) { res.writeHead(500); res.end(); return; }
        const reply = { id: 'loomy-ui', object: 'chat.completion', model, choices: [{ index: 0, message: { role: 'assistant', content: 'OK' }, finish_reason: 'stop' }] };
        if (body.stream) {
          res.setHeader('Content-Type', 'text/event-stream');
          res.end('data: ' + JSON.stringify({ id: 'loomy-ui', model, choices: [{ index: 0, delta: { role: 'assistant', content: 'OK' }, finish_reason: null }] }) + '\n\ndata: ' + JSON.stringify({ id: 'loomy-ui', model, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }) + '\n\ndata: [DONE]\n\n');
        } else { res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(reply)); }
      } catch { upstreamErrors.push('invalid upstream request'); res.writeHead(500); res.end(); }
    });
    t.after(async () => {
      if (browser) await browser.close();
      if (service) { if (service.exitCode === null) service.kill('SIGTERM'); await exited; }
      upstream.closeAllConnections(); await new Promise(resolve => upstream.close(resolve));
      await fs.rm(home, { recursive: true, force: true });
    });
    upstream.listen(0, '127.0.0.1'); await once(upstream, 'listening');
    const env = { ...process.env, HOME: home, USERPROFILE: home };
    for (const key of ['XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'APPDATA', 'LOCALAPPDATA']) env[key] = path.join(home, key);
    const layout = JSON.parse(execFileSync(binary, ['dirs'], { env, encoding: 'utf8' }));
    execFileSync(binary, ['config', 'init'], { env });
    const reservation = net.createServer(); reservation.listen(0, '127.0.0.1'); await once(reservation, 'listening');
    const address = `127.0.0.1:${reservation.address().port}`; await new Promise(resolve => reservation.close(resolve));
    const configFile = path.join(layout.config, 'config.json');
    const config = JSON.parse(await fs.readFile(configFile, 'utf8')); config.listen.address = address;
    await fs.writeFile(configFile, JSON.stringify(config), { mode: 0o600 });
    service = spawn(binary, ['serve'], { env, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true }); exited = once(service, 'exit');
    let output = '', token;
    service.stdout.on('data', chunk => { output += chunk; }); service.stderr.on('data', chunk => { output += chunk; });
    for (let i = 0; i < 200; i++) {
      if (service.exitCode !== null) assert.fail(`fixture service exited: ${output}`);
      try { token = (await fs.readFile(path.join(layout.state, 'router.lock', 'management-token'), 'utf8')).trim(); break; } catch {}
      await new Promise(resolve => setTimeout(resolve, 25));
    }
    assert.ok(token, 'fixture management token was not created');
    const origin = `http://${address}`, headers = { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' };
    const providerResult = await fetch(origin + '/api/cloud-providers', { method: 'POST', headers, body: JSON.stringify({ name: 'Loomy browser fixture', protocol: 'chat', baseURL: `http://127.0.0.1:${upstream.address().port}/v1`, apiKey: fixtureKey, proxyMode: 'direct' }) });
    assert.equal(providerResult.status, 200); const { provider } = await providerResult.json();
    const discoverResult = await fetch(origin + '/api/cloud-providers/models', { method: 'POST', headers, body: JSON.stringify({ id: provider.id, persist: true, fallback: true }) });
    assert.equal(discoverResult.status, 200);
    const stateDir = path.join(layout.state, 'host-config');
    const safe = value => {
      const text = typeof value === 'string' ? value : JSON.stringify(value);
      for (const secret of [token, fixtureKey, originalSecret, original.keep]) assert.equal(text.includes(secret), false, 'private fixture data exposed in host UI or API');
    };
    const hostAPI = async body => {
      const response = await fetch(origin + '/api/hosts/loomy', { method: 'POST', headers, body: JSON.stringify(body) });
      const result = await response.json(); safe(result); return { status: response.status, result };
    };
    browser = await (engine === 'webkit' ? webkit : chromium).launch({ ...(engine === 'chromium' && process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}) });
    for (const language of ['zh', 'en', 'zh-TW', 'ja', 'de']) for (const width of [1600, 375]) {
      const root = path.join(home, `host-${language}-${width}`), hostFile = path.join(root, 'opencode.json');
      await fs.mkdir(root); const before = JSON.stringify(original, null, 2); await fs.writeFile(hostFile, before, { mode: 0o600 });
      const beforeRecords = await jsonFiles(stateDir), page = await browser.newPage({ locale: 'en-US', viewport: { width, height: 950 } });
      const errors = [], hostRequests = [];
      page.on('pageerror', error => errors.push(error.message));
      page.on('request', request => { if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/hosts/loomy') hostRequests.push(request.postDataJSON()); });
      const noHostWrites = async () => { assert.equal(await fs.readFile(hostFile, 'utf8'), before); assert.deepEqual(await jsonFiles(stateDir), beforeRecords); };
      const fit = async () => {
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `${language}/${width} page overflow`);
        const bounds = await page.locator('#agents [data-app=loomy] .ag-exp').boundingBox();
        assert.ok(bounds && bounds.x >= -1 && bounds.x + bounds.width <= width + 1, `${language}/${width} details overflow`);
      };
      let applyDigest, restoreDigest;
      // Custom-directory preview/apply/recovery remains an HTTP/CLI contract;
      // the current Agents page exposes the model picker and inline details.
      const action = async (name, expected = 200) => {
        const body = { action: name, configRoot: root, model };
        if (name === 'apply') body.digest = applyDigest;
        if (name === 'restore') body.digest = restoreDigest;
        const { status, result } = await hostAPI(body);
        assert.equal(status, expected, `host ${name}`);
        if (status === 200 && name === 'preview') applyDigest = result.digest;
        if (status === 200 && name === 'restore-preview') restoreDigest = result.digest;
        return result;
      };
      await skipTutorial(page); await page.goto(origin + `/?locale=${language}&view=agents`);
      await page.waitForFunction(() => !!managementToken && !!serviceInfo && !!document.querySelector('#agents [data-app=loomy] .field.ag-menu'));
      await noHostWrites(); assert.equal(hostRequests.length, 0);
      if (!(await page.locator('#agents [data-app=loomy] .field.ag-menu').textContent()).includes(model)) {
        const beforeSaveCalls = calls.length;
        await page.locator('#agents [data-app=loomy] .field.ag-menu').click();
        await page.waitForSelector(`.pop.am-pop .am-mr[data-model="${provider.id}/${model}"]`);
        const saved = page.waitForResponse(r => new URL(r.url()).pathname === '/api/apps' && r.request().method() === 'POST');
        await page.locator(`.pop.am-pop .am-mr[data-model="${provider.id}/${model}"]`).click();
        await saved;
        await page.keyboard.press('Escape');
        assert.equal(calls.length, beforeSaveCalls, 'Agent model selection must not probe upstream');
      }
      await noHostWrites(); assert.equal(hostRequests.length, 0);
      const details = page.locator('#agents [data-app=loomy] .ag-link');
      await details.click();
      assert.equal((await details.textContent()).trim(), await page.evaluate(() => t('Collapse')));
      assert.equal((await page.locator('#agents [data-app=loomy] .ag-exp .ag-k').first().textContent()).trim(), await page.evaluate(() => t('In {agent}').replace('{agent}', 'Loomy')));
      assert.ok((await page.locator('#agents [data-app=loomy] .ag-pick').textContent()).includes(model));
      await noHostWrites(); await fit();
      await action('check'); await noHostWrites();
      const beforePreviewCalls = calls.length; const preview = await action('preview');
      assert.equal(calls.length, beforePreviewCalls, 'preview must not call a model'); await noHostWrites();
      assert.equal(preview.baseURL, origin + '/app/loomy/v1'); assert.equal(preview.requestURL, origin + '/app/loomy/v1/chat/completions');
      await noHostWrites(); await fit();
      if (language === 'zh' && width === 1600) {
        if (process.env.ARTIFACT_DIR) { await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true }); await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `loomy-preview-${engine}.png`) }); }
        await fs.writeFile(hostFile, JSON.stringify({ ...original, laterEdit: true }));
        const stale = await hostAPI({ action: 'apply', configRoot: root, model, digest: preview.digest });
        assert.equal(stale.status, 409, 'stale preview must not overwrite host edits'); assert.deepEqual(await jsonFiles(stateDir), beforeRecords);
        await fs.writeFile(hostFile, before); await action('preview');
        const beforeApplyCalls = calls.length; const applied = await action('apply');
        assert.equal(calls.length, beforeApplyCalls + 1, 'apply must check the saved target with a real model probe');
        const configured = JSON.parse(await fs.readFile(hostFile, 'utf8'));
        assert.equal(configured.model, 'api_router/' + model); assert.equal(configured.small_model, 'api_router/' + model);
        assert.equal(configured.provider.api_router.options.baseURL, origin + '/app/loomy/v1');
        assert.equal(configured.provider.api_router.options.apiKey, 'api-router-local-placeholder'); assert.equal(configured.provider.api_router.options.useSessionAuth, false);
        assert.deepEqual(configured.provider.original, original.provider.original);
        const records = await jsonFiles(applied.stateDir); assert.equal(records.length, beforeRecords.length + 1);
        const recordFile = path.join(applied.stateDir, records.find(name => !beforeRecords.includes(name))), firstBackup = await fs.readFile(recordFile);
        const firstApplied = await fs.readFile(hostFile); await action('preview'); await action('apply');
        assert.deepEqual(await fs.readFile(recordFile), firstBackup, 'repeat apply replaced the first backup'); assert.deepEqual(await fs.readFile(hostFile), firstApplied);
        configured.theme = 'later-user-theme'; configured.model = 'user/changed'; const conflict = JSON.stringify(configured);
        await fs.writeFile(hostFile, conflict); await action('restore-preview', 409);
        assert.equal(await fs.readFile(hostFile, 'utf8'), conflict); assert.deepEqual(await fs.readFile(recordFile), firstBackup);
        configured.model = 'api_router/' + model; await fs.writeFile(hostFile, JSON.stringify(configured));
        await action('restore-preview');
        await action('restore'); const restored = JSON.parse(await fs.readFile(hostFile, 'utf8'));
        assert.equal(restored.model, original.model); assert.equal(restored.small_model, original.small_model); assert.equal(restored.theme, 'later-user-theme');
        assert.deepEqual(restored.provider.original, original.provider.original); assert.equal(restored.provider.api_router, undefined);
      }
      assert.equal(await page.evaluate(() => window.fixtureExecuted), undefined); assert.deepEqual(errors, []);
      await fit(); await details.click(); assert.equal(await page.locator('#agents [data-app=loomy] .ag-exp').count(), 0); await page.close();
    }
    const offlineRoot = path.join(home, 'offline-host'), offlineFile = path.join(offlineRoot, 'opencode.json');
    await fs.mkdir(offlineRoot); await fs.writeFile(offlineFile, JSON.stringify(original));
    const preview = await hostAPI({ action: 'preview', configRoot: offlineRoot, model }); assert.equal(preview.status, 200);
    const applied = await hostAPI({ action: 'apply', configRoot: offlineRoot, model, digest: preview.result.digest }); assert.equal(applied.status, 200);
    const changed = JSON.parse(await fs.readFile(offlineFile, 'utf8')); changed.offlineNote = 'preserved'; await fs.writeFile(offlineFile, JSON.stringify(changed));
    await browser.close(); browser = null; service.kill('SIGTERM'); await exited;
    const args = ['--host', 'loomy', '--config-root', offlineRoot, '--state-dir', applied.result.stateDir];
    const recovery = JSON.parse(execFileSync(binary, ['host', 'restore-preview', ...args], { env, encoding: 'utf8' })); safe(recovery);
    execFileSync(binary, ['host', 'restore', ...args, '--plan-digest', recovery.digest], { env, encoding: 'utf8' });
    const restored = JSON.parse(await fs.readFile(offlineFile, 'utf8'));
    assert.equal(restored.model, original.model); assert.equal(restored.offlineNote, 'preserved'); assert.equal(restored.provider.api_router, undefined);
    assert.deepEqual(upstreamErrors, []);
  });
}
