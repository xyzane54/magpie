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
test(`routing groups list, editor and persistence (${engine})`, { timeout: 120000 }, async (t) => {
  const home = await fs.mkdtemp(path.join(os.tmpdir(), 'api-router-rg-ui-'));
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
  for (const key of ['XDG_CONFIG_HOME', 'XDG_CACHE_HOME', 'XDG_DATA_HOME', 'XDG_STATE_HOME', 'APPDATA', 'LOCALAPPDATA']) {
    env[key] = path.join(home, key);
  }
  env.WORKBUDDY_CONFIG_DIR = path.join(home, 'workbuddy');

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
  config.providers = {
    openai: {
      id: 'openai',
      revision: 1,
      name: 'OpenAI',
      preset: 'openai',
      protocol: 'chat',
      baseURL: 'https://api.openai.com/v1',
      proxyMode: 'direct',
      credentialRef: 'cred_openai',
      discoveredModels: ['gpt-4o', 'deepseek-chat'],
    },
    deepseek: {
      id: 'deepseek',
      revision: 1,
      name: 'DeepSeek',
      preset: 'deepseek',
      protocol: 'chat',
      baseURL: 'https://api.deepseek.com/v1',
      proxyMode: 'direct',
      credentialRef: 'cred_deepseek',
      discoveredModels: ['deepseek-chat'],
    },
  };
  config.localModels = [{ id: 'spark-x2.5-1.7b', installed: true, enabled: true, status: 'ready' }];
  config.groups = [
    {
      id: 'coding',
      name: 'Coding Group',
      routing: 'order',
      members: ['local/spark-x2.5-1.7b', 'openai/gpt-4o'],
    },
  ];
  await fs.writeFile(configFile, JSON.stringify(config, null, 2), { mode: 0o600 });

  const credFile = path.join(layout.config, 'credentials.json');
  const creds = {
    schemaVersion: 1,
    providers: {
      cred_openai: { providerId: 'openai', apiKey: 'sk-test-openai', headers: {}, extraBody: {} },
      cred_deepseek: { providerId: 'deepseek', apiKey: 'sk-test-deepseek', headers: {}, extraBody: {} },
    },
  };
  await fs.writeFile(credFile, JSON.stringify(creds, null, 2), { mode: 0o600 });

  service = spawn(binary, ['serve'], { env, stdio: ['ignore', 'pipe', 'pipe'] });
  exited = once(service, 'exit');
  let output = '';
  service.stdout.on('data', chunk => { output += chunk; });
  service.stderr.on('data', chunk => { output += chunk; });

  const tokenFile = path.join(layout.state, 'router.lock', 'management-token');
  for (let i = 0; i < 200; i++) {
    if (service.exitCode !== null) assert.fail(`service exited early: ${output}`);
    try {
      if ((await fs.readFile(tokenFile, 'utf8')).trim()) break;
    } catch {}
    await new Promise(resolve => setTimeout(resolve, 25));
  }

  browser = await (engine === 'webkit' ? webkit : chromium).launch({
    ...(process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}),
  });
  const page = await browser.newPage({ locale: 'zh-CN' });
  const pageErrors = [];
  page.on('pageerror', err => pageErrors.push(err.message));

  const apiCalls = [];
  page.on('request', req => {
    const url = new URL(req.url());
    if (url.pathname.startsWith('/api/routing-groups')) {
      let body = null;
      try { body = req.postDataJSON(); } catch {}
      apiCalls.push({ method: req.method(), path: url.pathname + url.search, body });
    }
  });

  await skipTutorial(page);
  await page.goto(`http://${address}/?locale=zh&view=routing`);
  await page.waitForFunction(() => document.querySelector('#service')?.classList.contains('ok'));

  // 1. 双分区渲染测试：自定义路由组 + 同名模型自动路由组
  const codingCard = page.locator('#customRoutingGroupsList .rt-group-card[data-group-id="coding"]');
  await codingCard.waitFor({ state: 'visible' });
  assert.equal(await codingCard.locator('[data-copy-call="group/coding"]').textContent(), 'group/coding');
  assert.equal(await codingCard.locator('.rt-strategy-tag').isVisible(), true);
  assert.equal(await codingCard.locator('.rt-member-chip.is-local[data-member="local/spark-x2.5-1.7b"]').isVisible(), true);
  assert.equal(await codingCard.locator('.rt-member-chip[data-member="openai/gpt-4o"]').isVisible(), true);

  const autoCard = page.locator('#autoRoutingGroupsList .rt-group-card[data-group-id="auto-deepseek-chat"]');
  await autoCard.waitFor({ state: 'visible' });
  assert.equal(await autoCard.locator('.rt-group-title').textContent(), 'deepseek-chat');
  assert.equal(await autoCard.locator('[data-copy-call="group/auto-deepseek-chat"]').textContent(), 'group/auto-deepseek-chat');
  assert.equal(await autoCard.locator('.rt-member-chip').count(), 2);
  assert.equal(await autoCard.locator('[data-member="openai/deepseek-chat"]').isVisible(), true);
  assert.equal(await autoCard.locator('[data-member="deepseek/deepseek-chat"]').isVisible(), true);

  // 2. 新建端云混合路由组 + 智能默认策略测试
  await page.locator('#newRoutingGroupBtn').click();
  await page.locator('#routingGroupModal').waitFor({ state: 'visible' });
  assert.equal(await page.locator('#routingGroupModal [data-rg-strategy="smart_hybrid"]').evaluate(node => node.classList.contains('on')), true);

  await page.locator('#rgName').fill('Hybrid Coding');
  assert.equal(await page.locator('#rgCallBadge').textContent(), 'group/hybrid-coding');

  await page.locator('#rgAddModelBtn').click();
  await page.locator('#rgModelPicker').waitFor({ state: 'visible' });
  const localCand = page.locator('#rgPickerList [data-picker-ref="local/spark-x2.5-1.7b"]');
  const cloudCand = page.locator('#rgPickerList [data-picker-ref="openai/gpt-4o"]');
  await localCand.waitFor({ state: 'visible' });
  await cloudCand.waitFor({ state: 'visible' });

  await localCand.click();
  await cloudCand.click();

  // Smart hybrid is the explicit default for a new mixed local/cloud group.
  assert.equal(await page.locator('#routingGroupModal [data-rg-strategy="smart_hybrid"]').evaluate(node => node.classList.contains('on')), true);

  apiCalls.length = 0;
  await page.locator('#rgSaveBtn').click();
  await page.locator('#routingGroupModal').waitFor({ state: 'hidden' });

  const createCall = apiCalls.find(c => c.method === 'POST' && c.path === '/api/routing-groups');
  assert.ok(createCall, 'expected POST /api/routing-groups');
  assert.equal(createCall.body.id, 'hybrid-coding');
  assert.equal(createCall.body.routing, 'smart_hybrid');
  assert.deepEqual(createCall.body.members, ['local/spark-x2.5-1.7b', 'openai/gpt-4o']);

  const hybridCard = page.locator('#customRoutingGroupsList .rt-group-card[data-group-id="hybrid-coding"]');
  await hybridCard.waitFor({ state: 'visible' });
  assert.equal(await hybridCard.locator('.rt-strategy-tag').isVisible(), true);

  // 3. 同名自动路由组一键固定（Pin）测试
  apiCalls.length = 0;
  await autoCard.locator('[data-rg-pin="auto-deepseek-chat"]').click();
  await page.locator('#routingGroupModal').waitFor({ state: 'visible' });
  await page.locator('#rgSaveBtn').click();
  await page.locator('#routingGroupModal').waitFor({ state: 'hidden' });
  const pinnedCard = page.locator('#customRoutingGroupsList .rt-group-card[data-group-id="deepseek-chat"]');
  await pinnedCard.waitFor({ state: 'visible' });
  const pinCall = apiCalls.find(c => c.method === 'POST' && c.path === '/api/routing-groups');
  assert.ok(pinCall, 'expected POST /api/routing-groups when pinning auto group');
  assert.equal(pinCall.body.id, 'deepseek-chat');
  assert.equal(await pinnedCard.locator('.rt-member-chip').count(), 2);

  // 4. 一键启停与手动切换成员测试
  apiCalls.length = 0;
  await hybridCard.locator('[data-rg-toggle="hybrid-coding"]').click();
  await page.waitForFunction(() => document.querySelector('#customRoutingGroupsList .rt-group-card[data-group-id="hybrid-coding"]')?.classList.contains('is-disabled'));
  const switchOffCall = apiCalls.find(c => c.method === 'POST' && c.path === '/api/routing-groups/switch');
  assert.ok(switchOffCall, 'expected POST /api/routing-groups/switch');
  assert.deepEqual(switchOffCall.body, { id: 'hybrid-coding', enabled: false });

  await hybridCard.locator('[data-rg-toggle="hybrid-coding"]').click();
  await page.waitForFunction(() => !document.querySelector('#customRoutingGroupsList .rt-group-card[data-group-id="hybrid-coding"]')?.classList.contains('is-disabled'));

  // 设为 manual 策略后在列表中点击切换 active 成员
  await hybridCard.locator('[data-rg-edit="hybrid-coding"]').click();
  await page.locator('#routingGroupModal').waitFor({ state: 'visible' });
  await page.locator('#routingGroupModal [data-rg-strategy="manual"]').click();
  await page.locator('#rgSaveBtn').click();
  await page.locator('#routingGroupModal').waitFor({ state: 'hidden' });

  apiCalls.length = 0;
  await hybridCard.locator('[data-member="openai/gpt-4o"]').click();
  await page.waitForFunction(() => document.querySelector('#customRoutingGroupsList .rt-group-card[data-group-id="hybrid-coding"] [data-member="openai/gpt-4o"]')?.classList.contains('is-active'));
  const manualSwitchCall = apiCalls.find(c => c.method === 'POST' && c.path === '/api/routing-groups');
  assert.ok(manualSwitchCall, 'expected POST /api/routing-groups when switching active member');
  assert.equal(manualSwitchCall.body.active, 'openai/gpt-4o');

  // 5. Editor timing, affinity, context and matching options persist exactly.
  await hybridCard.locator('[data-rg-edit="hybrid-coding"]').click();
  await page.locator('#routingGroupModal').waitFor({ state: 'visible' });
  await page.locator('#rgFirstTokenInput').fill('15');
  await page.locator('#rgPatienceInput').fill('2');
  await page.locator('[data-rg-aff="session"]').click();
  await page.locator('#rgStickinessInput').fill('10');
  await page.locator('#rgContextInput').fill('32k');
  for (const pattern of ['gpt-4*', 'claude-*']) {
    await page.locator('#rgPatternNewInput').fill(pattern);
    await page.locator('#rgAddPatternBtn').click();
  }

  apiCalls.length = 0;
  await page.locator('#rgSaveBtn').click();
  await page.locator('#routingGroupModal').waitFor({ state: 'hidden' });
  const advSaveCall = apiCalls.find(c => c.method === 'POST' && c.path === '/api/routing-groups');
  assert.ok(advSaveCall);
  assert.equal(advSaveCall.body.firstToken, 15);
  assert.equal(advSaveCall.body.patience, 120);
  assert.equal(advSaveCall.body.affinity, 'session');
  assert.equal(advSaveCall.body.stickiness, 10);
  assert.equal(advSaveCall.body.context, 32000);
  assert.deepEqual(advSaveCall.body.patterns, ['gpt-4*', 'claude-*']);

  apiCalls.length = 0;
  page.once('dialog', dialog => dialog.accept());
  await hybridCard.locator('[data-rg-delete="hybrid-coding"]').click();
  await page.waitForFunction(() => !document.querySelector('#customRoutingGroupsList .rt-group-card[data-group-id="hybrid-coding"]'));
  const deleteCall = apiCalls.find(c => c.method === 'DELETE' && c.path === '/api/routing-groups?id=hybrid-coding');
  assert.ok(deleteCall, 'expected DELETE /api/routing-groups?id=hybrid-coding');

  assert.deepEqual(pageErrors, []);
});

}
