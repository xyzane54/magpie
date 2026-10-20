const { skipTutorial } = require('./returning-user.cjs');
// UI-only service fixtures never register autostart, stop a process or load a model.
// Native lifecycle tests exercise those effects separately through the product CLI.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const http = require('node:http');
const path = require('node:path');
const { once } = require('node:events');
const { chromium, webkit } = require('playwright');

const assets = path.resolve(__dirname, '../assets');
const gui = path.resolve(__dirname, '../../gui/assets');
const token = 'a'.repeat(64);
const restartedToken = 'c'.repeat(64);
const executable = 'C:\\Users\\测试 用户\\AppData\\Local\\API-router\\api-router.exe';
const startCommand = `& '${executable}' service start`;

for (const engine of process.env.BROWSER ? [process.env.BROWSER] : ['chromium', 'webkit']) {
  test(`service settings consent and recovery (${engine})`, { timeout: 180000 }, async t => {
    const controls = [], unexpected = [];
    let state, currentToken, denyAutostart = false, newProcess = false, polls = 0;
    function reset() {
      state = { service: 'api-router', status: 'ok', running: true, pid: 10001, instanceId: 'before-restart', processStamp: 'synthetic-process-stamp', startedAt: '2026-10-18T00:00:00Z', version: '0.1.0-ui-fixture', address: '127.0.0.1', modelAPIReady: true, runtimeReady: false, executable, experimentalRedaction: true, autostart: { supported: true, enabled: false }, startCommand };
      currentToken = token;
      controls.length = 0; denyAutostart = false; newProcess = false; polls = 0;
    }
    reset();
    const server = http.createServer(async (req, res) => {
      const pathname = new URL(req.url, 'http://127.0.0.1').pathname;
      res.setHeader('Cache-Control', 'no-store');
      const json = (body, code = 200) => { res.writeHead(code, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(body)); };
      try {
        if (pathname === '/healthz') {
          if (newProcess) polls++;
          if (newProcess && polls > 1) {
            state.pid = 10002; state.instanceId = 'after-restart'; state.processStamp = 'synthetic-restarted-process-stamp';
            currentToken = restartedToken;
          }
          return json({ service: 'api-router', status: 'ok', pid: newProcess && polls > 1 ? 10002 : 10001 });
        }
        if (pathname === '/api/management/session') {
          assert.equal(req.method, 'POST');
          assert.equal(req.headers['x-api-router-ui'], '1');
          assert.equal(req.headers.origin, 'http://' + req.headers.host);
          assert.equal(req.headers['sec-fetch-site'], 'same-origin');
          return json({ schemaVersion: 1, token: currentToken });
        }
        // Automatic management connection may read the passive application list.
        if (pathname === '/api/apps' && req.method === 'GET' && req.headers.authorization === 'Bearer ' + currentToken) return json({ apps: [] });
        if (pathname.startsWith('/api/service')) {
          if (req.headers.authorization !== 'Bearer ' + currentToken) {
            res.setHeader('WWW-Authenticate', 'Bearer');
            return json({ error: { code: 'unauthorized' } }, 401);
          }
          if (req.method === 'GET' && pathname === '/api/service') return json(state);
          assert.equal(req.headers['x-api-router-instance'], state.processStamp);
          let body = ''; for await (const chunk of req) body += chunk;
          const data = JSON.parse(body || '{}');
          controls.push({ path: pathname, method: req.method, body: data, authorization: req.headers.authorization });
          if (pathname === '/api/service/autostart') {
            if (denyAutostart) return json({ error: { code: 'autostart_write_failed' } }, 409);
            state.autostart.enabled = data.enabled; return json(state);
          }
          if (pathname === '/api/service/stop') return json({ stopping: true, startCommand });
          if (pathname === '/api/service/restart') { newProcess = true; return json({ restarting: true, startCommand }); }
          unexpected.push(pathname); return json({ error: { code: 'not_found' } }, 404);
        }
        if (pathname.startsWith('/api/')) { unexpected.push(pathname); return json({ error: { code: 'unexpected_ui_request' } }, 404); }
        const original = ['/app.css', '/context.css', '/i18n.js', '/plugins/routing/routing.css'].includes(pathname) || pathname.startsWith('/icons/');
        const name = pathname === '/' ? 'index.html' : pathname.slice(1);
        if (name.includes('..')) return json({}, 404);
        const bytes = await fs.readFile(path.join(original ? gui : assets, name));
        const extension = path.extname(name);
        res.writeHead(200, { 'Content-Type': ({ '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json', '.svg': 'image/svg+xml' })[extension] || 'application/octet-stream' }); res.end(bytes);
      } catch (error) { unexpected.push(error.message); if (!res.headersSent) res.writeHead(500); res.end(); }
    });
    server.listen(0, '127.0.0.1'); await once(server, 'listening');
    let browser;
    t.after(async () => { if (browser) await browser.close(); await new Promise(resolve => server.close(resolve)); });
    browser = await (engine === 'webkit' ? webkit : chromium).launch({ ...(engine === 'chromium' && process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}) });
    const base = `http://127.0.0.1:${server.address().port}`;
    for (const language of ['zh', 'en', 'zh-TW', 'ja', 'de']) for (const width of [1600, 375]) {
      reset();
      const page = await browser.newPage({ locale: 'en-US', viewport: { width, height: 950 } });
      const errors = []; page.on('pageerror', error => errors.push(error.message));
      const translated = key => page.evaluate(key => t(key), key);
      const connect = async () => {
        await page.locator('#serviceControlRefresh').click();
        await page.waitForFunction(() => !!managementToken && !!serviceInfo && !!document.querySelector('#serviceControlPID'));
      };
      await skipTutorial(page); await page.goto(`${base}/?locale=${language}&view=settings`);
      await page.waitForFunction(() => !!managementToken && !!serviceInfo && !!document.querySelector('#serviceControlPID'));
      assert.equal(await page.locator('#manage, #manageSettings, #modal, #token, #connect').count(), 0);
      assert.equal(await page.locator('#serviceControl-stop').isDisabled(), false);
      assert.equal(controls.length, 0);
      assert.equal(await page.locator('#serviceControlPID').textContent(), '10001');
      assert.equal(await page.locator('#serviceControlExecutable').textContent(), executable);
      assert.equal(await page.locator('#serviceStartCommand').textContent(), startCommand);
      assert.equal(await page.locator('#serviceControlTrial').textContent(), await translated('Enabled'));
      assert.equal(await page.locator('#serviceControl > .row-head').textContent(), await translated('Service management'));
      if (language !== 'en') assert.notEqual(await page.locator('#serviceControl > .row-head').textContent(), 'Service management');
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `${language}/${width} must fit the viewport`);
      assert.equal((await page.content()).includes(token), false);
      assert.equal(await page.evaluate(token => JSON.stringify({ ...localStorage, ...sessionStorage }).includes(token), token), false);

      await page.locator('#serviceControlAutostart').click();
      await page.waitForFunction(() => document.querySelector('#serviceControlMessage').textContent === t('Autostart setting saved.'));
      assert.equal(await page.locator('#serviceControlAutostartState').textContent(), await translated('Enabled'));
      assert.deepEqual(controls.at(-1), { path: '/api/service/autostart', method: 'POST', body: { enabled: true }, authorization: 'Bearer ' + token });
      await page.locator('#serviceControlAutostart').click();
      await page.waitForFunction(() => document.querySelector('#serviceControlAutostartState').textContent === t('Disabled'));
      assert.deepEqual(controls.at(-1).body, { enabled: false });
      state.autostart = { supported: false, enabled: false };
      await page.locator('#serviceControlRefresh').click();
      await page.waitForFunction(() => document.querySelector('#serviceControlAutostart').disabled);
      assert.equal(await page.locator('#serviceControlAutostartState').textContent(), await translated('Autostart is unavailable on this platform.'));
      state.autostart = { supported: true, enabled: null };
      await page.locator('#serviceControlRefresh').click();
      await page.waitForFunction(() => document.querySelector('#serviceControlAutostartState').textContent === t('Autostart status is unknown. Refresh before changing it.'));
      assert.equal(await page.locator('#serviceControlAutostart').isDisabled(), true);
      state.autostart = { supported: true, enabled: false };
      await page.locator('#serviceControlRefresh').click();
      await page.waitForFunction(() => !document.querySelector('#serviceControlAutostart').disabled);
      denyAutostart = true;
      await page.locator('#serviceControlAutostart').click();
      await page.waitForFunction(() => document.querySelector('#serviceControlMessage').textContent === t('Service operation failed. Refresh to inspect the current state.'));
      assert.equal(await page.locator('#serviceControlAutostartState').textContent(), await translated('Disabled'));
      denyAutostart = false;

      await page.locator('#serviceControl-stop').click();
      assert.equal(await page.locator('#serviceControlExecute').isDisabled(), true);
      const beforeCancel = controls.length; await page.locator('#serviceControlCancel').click(); assert.equal(controls.length, beforeCancel);
      await page.locator('#serviceControl-stop').click();
      await page.locator('#serviceControlAck').check(); await page.locator('#serviceControlExecute').click();
      await page.waitForFunction(() => document.querySelector('#serviceControlMessage').textContent === t('Stop requested. Use the saved PowerShell command to start Router again.'));
      assert.equal(await page.locator('#serviceControl-stop').isDisabled(), true);
      assert.equal(await page.locator('#serviceStartCommand').textContent(), startCommand);
      assert.equal(await page.evaluate(() => managementToken), '');
      assert.equal(await page.locator('#serviceControlExecutable').count(), 0);

      await connect();
      await page.locator('#serviceControl-restart').click();
      await page.locator('#serviceControlAck').check(); await page.locator('#serviceControlExecute').click();
      await page.waitForFunction(() => document.querySelector('#serviceControlMessage').textContent === t('Restart requested. Waiting for a new Router process…'));
      assert.equal(await page.locator('#serviceControl-restart').isDisabled(), true);
      await page.waitForFunction(() => serviceInfo?.pid === 10002 && !!managementToken && !document.querySelector('#serviceControl-restart').disabled);
      assert.ok(polls >= 2, 'an unchanged old PID cannot confirm restart');
      assert.equal(await page.evaluate(() => managementToken), restartedToken);
      assert.equal(await page.locator('#serviceControlPID').textContent(), '10002');
      assert.equal(await page.locator('#serviceControl-restart').isDisabled(), false);
      assert.equal(await page.evaluate(token => JSON.stringify({ ...localStorage, ...sessionStorage }).includes(token), token), false);
      assert.equal((await page.content()).includes(token), false);
      assert.equal((await page.content()).includes(restartedToken), false);
      assert.equal(await page.locator('#serviceStartCommand').textContent(), startCommand);
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
      assert.deepEqual(errors, [], `${language}/${width} page errors`);
      assert.deepEqual(unexpected, [], 'settings must not perform model or host operations');
      await page.close();
    }
  });
}
