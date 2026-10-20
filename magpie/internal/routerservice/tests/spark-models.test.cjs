const { skipTutorial } = require('./returning-user.cjs');
// A model list offers choices; neither a published ID nor an official catalog
// proves that this account can generate with it. Only discovery is mocked.
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
const officialModels = ['4.0Ultra', 'generalv3.5', 'max-32k', 'generalv3', 'pro-128k', 'lite'];
const docsURL = 'https://www.xfyun.cn/doc/spark/HTTP调用文档.html';
const maasDocsURL = 'https://www.xfyun.cn/doc/spark/产品使用说明.html';
const maasModelDocsURL = 'https://www.xfyun.cn/doc/spark/Spark-X2.5.html';
const maasModels = ['spark-x2.5', 'spark-x2.5-4b', 'spark-x2.5-1.7b'];

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ['chromium', 'webkit'])) {
  test(`Spark model candidates remain explicit and unverified (${engine})`, { timeout: 120000 }, async t => {
    assert.ok(['chromium', 'webkit'].includes(engine));
    const home = await fs.mkdtemp(path.join(os.tmpdir(), 'api-router-spark-models-'));
    let service, exited, browser;
    const upstreamCalls = [], generationCalls = [];
    // If selection ever starts generation accidentally, it reaches this local
    // failure fixture rather than sending even a fixture key to the real vendor.
    const upstream = http.createServer((req, res) => {
      upstreamCalls.push(req.url);
      if (req.url !== '/v1/models' && req.url !== '/v2/models') generationCalls.push(req.url);
      req.resume();
      res.writeHead(500, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ error: { message: 'Unexpected test upstream call' } }));
    });
    t.after(async () => {
      if (browser) await browser.close();
      if (service) {
        if (service.exitCode === null) service.kill('SIGTERM');
        await exited;
      }
      await new Promise(resolve => upstream.close(resolve));
      await fs.rm(home, { recursive: true, force: true });
    });
    upstream.listen(0, '127.0.0.1');
    await once(upstream, 'listening');
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
    let output = '', token;
    service.stdout.on('data', chunk => { output += chunk; });
    service.stderr.on('data', chunk => { output += chunk; });
    for (let i = 0; i < 200; i++) {
      if (service.exitCode !== null) assert.fail(`service exited: ${output}`);
      try {
        token = (await fs.readFile(path.join(layout.state, 'router.lock', 'management-token'), 'utf8')).trim();
        break;
      } catch {}
      await new Promise(resolve => setTimeout(resolve, 25));
    }
    assert.ok(token, `no management token: ${output}`);
    const origin = `http://${address}`;
    const headers = { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' };
    const providerResponse = await fetch(origin + '/api/cloud-providers', {
      method: 'POST',
      headers,
      body: JSON.stringify({ name: 'Spark fixture', preset: 'spark', protocol: 'chat', baseURL: `http://127.0.0.1:${upstream.address().port}/v1`, apiKey: 'fake-spark-fixture-key', proxyMode: 'direct' }),
    });
    assert.equal(providerResponse.status, 200);
    const { provider } = await providerResponse.json();
    const maasProviderResponse = await fetch(origin + '/api/cloud-providers', {
      method: 'POST',
      headers,
      body: JSON.stringify({ name: 'Spark MaaS fixture', preset: 'spark-maas', protocol: 'chat', baseURL: `http://127.0.0.1:${upstream.address().port}/v2`, apiKey: 'fake-spark-maas-fixture-key', proxyMode: 'direct' }),
    });
    assert.equal(maasProviderResponse.status, 200);
    const { provider: maasProvider } = await maasProviderResponse.json();

    const listRes = await fetch(origin + '/api/cloud-providers', { headers });
    assert.equal(listRes.status, 200);
    const listed = await listRes.json();
    const sparkListed = listed.providers.find(p => p.id === provider.id);
    const maasListed = listed.providers.find(p => p.id === maasProvider.id);
    assert.deepEqual(sparkListed.effectiveModels, officialModels);
    assert.equal(sparkListed.modelSource, 'preset');
    assert.deepEqual(maasListed.effectiveModels, maasModels);
    assert.equal(maasListed.modelSource, 'preset');

    const sparkCatalogRes = await fetch(origin + '/api/cloud-providers/models', { method: 'POST', headers, body: JSON.stringify({ id: provider.id, fallback: true }) });
    assert.equal(sparkCatalogRes.status, 200);
    const sparkCatalog = await sparkCatalogRes.json();
    assert.equal(sparkCatalog.source, 'preset');
    assert.equal(sparkCatalog.fallback, true);
    assert.deepEqual(sparkCatalog.models, officialModels);

    const maasCatalogRes = await fetch(origin + '/api/cloud-providers/models', { method: 'POST', headers, body: JSON.stringify({ id: maasProvider.id, fallback: true }) });
    assert.equal(maasCatalogRes.status, 200);
    const maasCatalog = await maasCatalogRes.json();
    assert.equal(maasCatalog.source, 'preset');
    assert.equal(maasCatalog.fallback, true);
    assert.deepEqual(maasCatalog.models, maasModels);

    const savedConfig = await fs.readFile(configFile, 'utf8');
    browser = await (engine === 'webkit' ? webkit : chromium).launch({
      ...(engine === 'chromium' && process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}),
    });
    for (const language of ['zh', 'en', 'zh-TW', 'ja', 'de']) for (const width of [1280, 375]) {
      const page = await browser.newPage({ locale: 'en-US', viewport: { width, height: 950 } });
      const errors = [], checks = [], saves = [], outgoing = [], modelDrafts = [];
      page.on('pageerror', error => errors.push(error.message));
      page.on('request', request => {
        const url = new URL(request.url());
        if (url.host !== address) outgoing.push(request.url());
        if (request.method() !== 'POST') return;
        if (url.pathname === '/api/apps/check') checks.push(request.postDataJSON());
        if (url.pathname === '/api/apps') saves.push(request.postDataJSON());
        if (url.pathname === '/api/cloud-providers/models') modelDrafts.push(request.postDataJSON());
      });
      await skipTutorial(page); await page.goto(origin + `/?locale=${language}&view=providers`);
      await page.waitForFunction(() => document.querySelector('#service').classList.contains('ok'));
      await page.waitForFunction(() => !!managementToken && !!serviceInfo);
      await page.locator('[data-preset=spark]').click();
      assert.equal(await page.locator('#cloudBase').inputValue(), 'https://spark-api-open.xf-yun.com/v1');
      await page.locator('#providerCancel').click();
      await page.locator('[data-preset=spark-maas]').click();
      assert.equal(await page.locator('#providerTitle').textContent(), await page.evaluate(() => t('Spark MaaS')));
      assert.equal(await page.locator('#cloudBase').inputValue(), 'https://maas-api.cn-huabei-1.xf-yun.com/v2', 'new MaaS providers must start with the Chat model-discovery endpoint');
      assert.equal(await page.locator('#cloudProtocol button[aria-pressed=true]').textContent(), 'OpenAI Chat Completions');
      const endpointNotice = 'Spark MaaS uses /v2 for Chat Completions, /anthropic for Anthropic Messages, and /v1 for Responses. Match the protocol to the address.';
      for (const [protocol, suffix] of [
        ['OpenAI Chat Completions', '/v2'],
        ['OpenAI Responses', '/v1'],
        ['Anthropic Messages', '/anthropic'],
        ['OpenAI Chat Completions', '/v2'],
      ]) {
        await page.locator('#cloudProtocol button').filter({ hasText: protocol }).click();
        assert.equal(await page.locator('#cloudBase').inputValue(), 'https://maas-api.cn-huabei-1.xf-yun.com' + suffix);
        assert.equal(await page.locator('#cloudProtocol button[aria-pressed=true]').textContent(), protocol);
        assert.equal(await page.locator('#cloudPlatformHint').textContent(), await page.evaluate(key => t(key), endpointNotice));
        assert.equal(await page.locator('#cloudDocs').getAttribute('href'), maasDocsURL);
      }
      await page.locator('#providerCancel').click();
      assert.equal(modelDrafts.length, 0, 'opening a provider preset must not request model discovery');

      await page.locator('#nav [data-view=agents]').click();
      await page.waitForSelector('#agents [data-app=workbuddy] .field.ag-menu');
      await page.locator('#agents [data-app=workbuddy] .field.ag-menu').click();
      await page.waitForSelector(`.pop.am-pop [data-section="provider-${provider.id}"]`);
      const sparkModelsInMenu = await page.locator(`.pop.am-pop [data-section="provider-${provider.id}"] .am-mr`).evaluateAll(nodes => nodes.map(n => n.dataset.model));
      assert.deepEqual(sparkModelsInMenu, officialModels.map(id => `${provider.id}/${id}`));
      const maasModelsInMenu = await page.locator(`.pop.am-pop [data-section="provider-${maasProvider.id}"] .am-mr`).evaluateAll(nodes => nodes.map(n => n.dataset.model));
      assert.deepEqual(maasModelsInMenu, maasModels.map(id => `${maasProvider.id}/${id}`));

      await page.locator('.pop.am-pop .am-custom-input').fill(`${provider.id}/manual-spark-model-id`);
      assert.equal(await page.locator('.pop.am-pop .am-custom-input').inputValue(), `${provider.id}/manual-spark-model-id`, 'a catalog must not block manual model IDs');
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'model choices must fit the viewport');
      if (process.env.ARTIFACT_DIR) {
        await fs.mkdir(process.env.ARTIFACT_DIR, { recursive: true });
        await page.screenshot({ path: path.join(process.env.ARTIFACT_DIR, `spark-models-${engine}-${language}-${width}.png`) });
      }
      await page.keyboard.press('Escape');
      assert.equal(await page.locator('.pop.am-pop').count(), 0);
      assert.deepEqual(errors, []);
      assert.deepEqual(outgoing, [], 'the browser must not contact official docs or cloud endpoints automatically');
      assert.deepEqual(generationCalls, [], 'candidate browsing must not send a generation request');
      assert.deepEqual(checks, [], 'opening candidate menu must not trigger generation checks');
      assert.deepEqual(saves, [], 'browsing candidates and closing menu must not save selections');
      assert.equal(await fs.readFile(configFile, 'utf8'), savedConfig, 'browsing candidates must not rewrite configuration');
      await page.close();
    }
  });
}
