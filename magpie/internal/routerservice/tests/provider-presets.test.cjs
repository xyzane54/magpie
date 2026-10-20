const { skipTutorial } = require('./returning-user.cjs');
// Build with make api-router, then run with node --test this file.
// Requires Playwright and installed engines. Override BROWSER or the Chromium
// executable for a machine that already has a compatible browser installation.
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
  test(`provider platform identity (${engine})`, { timeout: 180000 }, async (t) => {
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
    let listRequests = 0;
    for (const language of ['zh', 'en', 'zh-TW', 'ja', 'de']) {
      const page = await browser.newPage({ locale: 'en-US' });
      const errors = [], outgoing = [];
      page.on('pageerror', error => errors.push(error.message));
      page.on('request', request => { if (new URL(request.url()).host !== address) outgoing.push(request.url()); });
      await page.route('**/api/cloud-providers/models', route => {
        listRequests++;
        return route.fulfill({ contentType: 'application/json', body: JSON.stringify({ models: ['cmaas-qwen3.8-flash', 'qwen3.8-flash'] }) });
      });
      for (const width of [1600, 1100, 375]) {
        await page.setViewportSize({ width, height: 950 });
        await skipTutorial(page); await page.goto(`http://${address}/?locale=${language}&view=providers`);
        await page.waitForFunction(() => document.querySelector('#service').classList.contains('ok'));
        await page.waitForFunction(() => !!managementToken && !!serviceInfo);
        await page.waitForFunction(() => document.querySelectorAll('#providerPresets .tile').length > 20);
        assert.equal(await page.locator('[data-preset=qianwen-ai]').count(), 1, 'Qianwen AI Platform must have its own selectable preset');
        for (const [id, host, platform] of [
          ['qianwen-ai', 'maas.qianwenaiapi.com', 'Qianwen AI Platform'],
          ['qwen-cn', 'dashscope.aliyuncs.com', 'Alibaba Cloud Bailian (Beijing)'],
          ['qwen', 'dashscope-intl.aliyuncs.com', 'Alibaba Cloud Model Studio (Singapore)'],
        ]) {
          const before = listRequests;
          const tile = page.locator(`[data-preset=${id}]`);
          assert.equal(await tile.locator('.n').textContent(), await page.evaluate(key => t(key), platform));
          await tile.click();
          assert.equal(new URL(await page.locator('#cloudBase').inputValue()).hostname, host);
          assert.ok((await page.locator('#cloudPlatformHint').textContent()).includes(await page.evaluate(key => t(key), platform)));
          assert.equal(await page.locator('#cloudDocs').isVisible(), true);
          assert.equal(await page.locator('#providerModal .dialog').evaluate(node => node.scrollWidth <= node.clientWidth + 1), true);
          assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
          assert.equal(listRequests, before, 'opening a preset must not fetch models or probe generation');
          assert.equal(await page.locator('#cloudModel, #cloudFetchModels, #cloudModels').count(), 0, 'models are chosen only in application Profiles');
          if (id === 'qianwen-ai') {
            assert.equal(await page.locator('#cloudGetKey').getAttribute('href'), 'https://platform.qianwenai.com/home/api-keys');
            assert.equal(await page.locator('#cloudDocs').getAttribute('href'), 'https://platform.qianwenai.com/docs/api-reference/preparation/api-key');
          }
          // Changing a draft address updates the help, never the address or key.
          await page.locator('#cloudBase').fill('https://maas.qianwenaiapi.com/compatible-mode/v1');
          assert.equal(await page.locator('#cloudGetKey').getAttribute('href'), 'https://platform.qianwenai.com/home/api-keys');
          await page.locator('#cloudBase').fill('http://127.0.0.1:1/custom/v1');
          assert.equal(await page.locator('#cloudPlatformHint').textContent(), '');
          assert.equal(await page.locator('#cloudBase').inputValue(), 'http://127.0.0.1:1/custom/v1');
          await page.locator('#providerCancel').click();
        }
        assert.deepEqual(errors, []);
        assert.deepEqual(outgoing, [], 'editor must never send a key or a request to an alternative platform');
      }
      // Old preset IDs keep existing custom addresses and names. Models belong to Profiles.
      const saved = await page.request.post(`http://${address}/api/cloud-providers`, {
        headers: { Authorization: 'Bearer ' + token },
        data: { name: 'Existing custom Qwen', preset: 'qwen-cn', protocol: 'chat', baseURL: 'http://127.0.0.1:1/owned/v1', apiKey: 'fake-owned-key', proxyMode: 'direct' }
      });
      assert.equal(saved.status(), 200);
      await page.reload();
      await page.waitForFunction(() => !!managementToken && !!serviceInfo);
      await page.locator('#providers .row').last().locator('button').click();
      assert.equal(await page.locator('#cloudBase').inputValue(), 'http://127.0.0.1:1/owned/v1');
      assert.equal(await page.locator('#cloudName').inputValue(), 'Existing custom Qwen');
      assert.equal(await page.locator('#cloudModel, #cloudFetchModels, #cloudModels').count(), 0);
      await page.locator('#providerCancel').click();
      await page.close();
    }
  });
}
