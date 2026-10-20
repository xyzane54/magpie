'use strict';
// Product adapter for the retained Magpie client shell.
const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];
const params = new URLSearchParams(location.search);
let preferences = {};
try { preferences = JSON.parse(localStorage.getItem('api-router.ui') || '{}') || {}; } catch {}
const savePreferences = () => { try { localStorage.setItem('api-router.ui', JSON.stringify(preferences)); } catch {} };
const el = (tag, className, text) => {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};

for (const [key, zh, tw, ja, de] of [
  ['Routing trace is not connected yet', '路由记录尚未接入', '路由紀錄尚未接入', 'ルーティング記録は未接続です', 'Routing-Verlauf noch nicht angebunden'],
  ['Per-request decisions, local or cloud destinations, timing and failure stages will appear here.', '这里将展示逐请求判断、本地或云端的实际去向、耗时和失败阶段。', '這裡將顯示逐請求判斷、本機或雲端的實際去向、耗時和失敗階段。', 'ここに各リクエストの判断、ローカルまたはクラウドの実行先、所要時間、失敗した段階が表示されます。', 'Hier erscheinen Entscheidungen je Anfrage, lokale oder Cloud-Ziele, Dauer und Fehlerphasen.'],
  ['Routing records will appear when the request trace is connected.', '请求追踪接入后，这里将显示路由记录。', '請求追蹤接入後，這裡將顯示路由紀錄。', 'リクエスト追跡の接続後、ルーティング記録を表示します。', 'Routing-Verlauf erscheint, sobald die Anfrageverfolgung angebunden ist.'],
  ['Connect management', '连接管理', '連線管理', '管理に接続', 'Verwaltung verbinden'],
  ['Select routing groups or models for Loomy and WorkBuddy, then toggle the switch to write host configuration files directly.', '为 Loomy 与 WorkBuddy 选择路由组或模型，开启右侧开关即可一键写入宿主配置文件。', '為 Loomy 與 WorkBuddy 選擇路由組或模型，開啟右側開關即可一鍵寫入宿主設定檔。', 'LoomyとWorkBuddyのルーティンググループまたはモデルを選択し、スイッチでホスト設定を直接書き込みます。', 'Routing-Gruppen oder Modelle für Loomy und WorkBuddy auswählen und per Schalter direkt in die Host-Konfiguration schreiben.'],
  ["Once the switch is turned on, models configured in api-router will appear as the api-router provider in the agent's model list.", '开关打开后，api-router中配置的模型会作为api-router供应商出现在agent的模型列表里', '開關打開後，api-router中配置的模型會作為api-router供應商出現在agent的模型列表裡', 'スイッチをオンにすると、api-routerで設定されたモデルがagentのモデル一覧にapi-routerプロバイダーとして表示されます。', 'Wenn der Schalter aktiviert ist, erscheinen die in api-router konfigurierten Modelle als api-router-Anbieter in der Modellliste des Agenten.'],
  ['Details', '详情', '詳情', '詳細', 'Details'],
  ['Collapse', '收起', '收起', '折りたたむ', 'Einklappen'],
  ['Model list', '模型列表', '模型列表', 'モデル一覧', 'Modellliste'],
  ['Pick', '挑选', '挑選', '選択', 'Auswählen'],
  ['New sessions', '新会话默认', '新工作階段預設', '新規セッション', 'Neue Sitzungen'],
  ['Disconnect & restore', '断开并还原', '中斷並還原', '切断して復元', 'Trennen & wiederherstellen'],
  ['Service', '服务', '服務', 'サービス', 'Dienst'],
  ['Local access only', '仅允许本机访问', '僅允許本機存取', 'この端末からのみアクセス可能', 'Nur lokaler Zugriff'],
  ['Listen address', '监听地址', '監聽位址', '待受アドレス', 'Listen-Adresse'],
  ['Model requests', '模型请求', '模型請求', 'モデルリクエスト', 'Modellanfragen'],
  ['Ready', '可用', '可用', '利用可能', 'Verfügbar'],
  ['Not connected yet', '尚未接入', '尚未接入', '未接続', 'Noch nicht angebunden'],
  ['Local model', '本地模型', '本機模型', 'ローカルモデル', 'Lokales Modell'],
  ['Cloud requests are connected. Explicit 1.7B or 4B local routing and execution become available after enable, install and capability checks; redaction is separate.', '云端请求已接通。明确启用、安装并完成能力检查后，1.7B 或 4B 本地路由与执行可用；脱敏单独管理。', '雲端請求已接通。明確啟用、安裝並完成能力檢查後，1.7B 或 4B 本機路由與執行可用；去識別化獨立管理。', 'クラウドリクエストは利用できます。1.7Bまたは4Bを有効化・インストールし、機能確認を完了するとローカルルーティングと実行を使えます。匿名化は別管理です。'],
  ['Manual refresh', '手动刷新', '手動重新整理', '手動更新', 'Manuell aktualisieren'],
  ['Only requests passing through API-router are counted. Host session files are not scanned.', '仅统计经过 API-router 的请求，不扫描宿主会话文件。', '僅統計經過 API-router 的請求，不掃描宿主工作階段檔案。', 'API-routerを通るリクエストのみを集計します。ホストのセッションファイルはスキャンしません。', 'Es werden nur Anfragen über API-router gezählt. Sitzungsdateien anderer Anwendungen werden nicht durchsucht.'],
  ['Estimated cost', '估算成本', '估算成本', '推定コスト', 'Geschätzte Kosten'],
  ['Latency', '延迟', '延遲', '遅延', 'Latenz'],
  ['Statistics are not connected yet', '用量统计尚未接入', '用量統計尚未接入', '使用統計は未接続です', 'Nutzungsstatistik noch nicht angebunden'],
  ['Usage will appear when the statistics ledger is connected.', '统计账本接入后，这里将显示用量。', '統計帳本接入後，這裡將顯示用量。', '使用統計の接続後、使用量を表示します。', 'Nutzung erscheint, sobald das Statistikbuch angebunden ist.'],
  ['Request history is not connected yet', '请求记录尚未接入', '請求紀錄尚未接入', 'リクエスト履歴は未接続です', 'Anfrageverlauf noch nicht angebunden'],
  ['Request counts, tokens, cost and latency will appear here.', '这里将记录请求数、Token、成本和延迟。', '這裡將記錄請求數、Token、成本和延遲。', 'ここにリクエスト数、トークン、コスト、遅延が記録されます。', 'Hier werden Anfragezahlen, Tokens, Kosten und Latenz erfasst.'],
  ['Through API-router', '经过 API-router 的', '經過 API-router 的', 'API-router経由', 'Über API-router'],
  ['Management access', '管理访问', '管理存取', '管理アクセス', 'Verwaltungszugriff'],
  ['Separate from cloud API keys', '与云端 API Key 分开使用', '與雲端 API Key 分開使用', 'クラウドAPIキーとは別に使用します', 'Getrennt von Cloud-API-Schlüsseln'],
  ['Local model router', '本机模型路由服务', '本機模型路由服務', 'ローカルモデルルーター', 'Lokaler Modellrouter'],
  ['Management token required', '需要管理令牌', '需要管理權杖', '管理トークンが必要です', 'Verwaltungstoken erforderlich'],
  ['Started at', '启动时间', '啟動時間', '起動時刻', 'Gestartet am'],
  ['Invalid management token. Please try again.', '管理令牌无效，请重新输入。', '管理權杖無效，請重新輸入。', '管理トークンが無効です。再入力してください。', 'Ungültiges Verwaltungstoken. Bitte erneut eingeben.'],
  ['Connection failed. Check that the service is running.', '连接失败，请检查服务是否仍在运行。', '連線失敗，請檢查服務是否仍在執行。', '接続できません。サービスが実行中か確認してください。', 'Verbindung fehlgeschlagen. Prüfen Sie, ob der Dienst läuft.'],
  ['Running', '运行中', '執行中', '実行中', 'Läuft'],
  ['Checking connection…', '正在检查连接…', '正在檢查連線…', '接続を確認中…', 'Verbindung wird geprüft…'],
  ['Connected', '已连接', '已連線', '接続済み', 'Verbunden'],
  ['Connection unavailable', '服务不可访问', '無法存取服務', '接続できません', 'Verbindung nicht verfügbar'],
  ['Connect', '连接', '連線', '接続', 'Verbinden'],
  ['Refresh now', '立即刷新', '立即重新整理', '今すぐ更新', 'Jetzt aktualisieren'],
  ['Toggle sidebar', '切换侧栏', '切換側邊欄', 'サイドバーを切り替え', 'Seitenleiste umschalten'],
  ['Sidebar width', '侧栏宽度', '側邊欄寬度', 'サイドバーの幅', 'Breite der Seitenleiste'],
]) {
  for (const [lang, text] of [['zh', zh], ['zh-TW', tw], ['ja', ja], ['de', de || zh]]) (I18N[lang] ||= {})[key] = text;
}

const views = ['agents', 'providers', 'local-model', 'routing', 'gateway', 'usage', 'settings'];
let view = views.includes(params.get('view')) ? params.get('view') : views.includes(preferences.view) ? preferences.view : 'usage';
let usageTab = ['usage', 'requests'].includes(params.get('tab')) ? params.get('tab') : params.has('tab') ? 'usage' : ['usage', 'requests'].includes(preferences.usageTab) ? preferences.usageTab : 'usage';
if (params.get('tab') === 'context' || params.get('view') === 'context') {
  view = 'usage'; usageTab = 'usage';
  params.set('view', view); params.set('tab', usageTab);
  history.replaceState(null, '', location.pathname + '?' + params + location.hash);
}
if (preferences.usageTab === 'context' || preferences.view === 'context') {
  preferences.usageTab = 'usage'; preferences.view = 'usage'; savePreferences();
}
let settingsTab = 'general', managementToken = '', serviceInfo = null, healthy = false, busy = false, managementSession;
const sideQuery = matchMedia('(min-width: 1024px)'), phoneQuery = matchMedia('(max-width: 760px)');
let sidePick = ['side', 'top'].includes(preferences.navigation) ? preferences.navigation : '';

function applyAppearance() {
  if (['light', 'dark'].includes(preferences.theme)) document.documentElement.dataset.theme = preferences.theme;
  else delete document.documentElement.dataset.theme;
}
applyAppearance();
setLocale(preferences.language || 'zh');

function chooseButtons(container, items, selected, choose) {
  container.replaceChildren(...items.map(([id, name]) => {
    const button = el('button', 'opt' + (selected === id ? ' on' : ''), name);
    button.type = 'button'; button.setAttribute('aria-pressed', String(selected === id));
    button.onclick = () => choose(id);
    return button;
  }));
  requestAnimationFrame(placeSelections);
}

function placeSelections() {
  for (const control of $$('.segs, #nav')) {
    if (control.id === 'nav' && document.body.classList.contains('side-nav')) { control.querySelector(':scope > .thumb')?.remove(); continue; }
    const selected = control.querySelector(':scope > button.on');
    if (!selected || !control.offsetWidth) continue;
    let thumb = control.querySelector(':scope > .thumb');
    if (!thumb) { thumb = el('i', 'thumb'); control.prepend(thumb); }
    Object.assign(thumb.style, { left: selected.offsetLeft + 'px', top: selected.offsetTop + 'px', bottom: 'auto', width: selected.offsetWidth + 'px', height: selected.offsetHeight + 'px' });
  }
}
function renderPreferences() {
  chooseButtons($('#themeSegs'), [['system', t('System')], ['light', t('Light')], ['dark', t('Dark')]], preferences.theme || 'system', (id) => {
    preferences.theme = id; savePreferences(); applyAppearance(); renderPreferences();
  });
  chooseButtons($('#langSegs'), [['zh', '简体中文'], ['en', 'English'], ['zh-TW', '繁體中文'], ['ja', '日本語'], ['de', 'Deutsch']], locale, (id) => {
    preferences.language = id; savePreferences(); setLocale(id); renderPreferences(); renderAgents(); renderNavigation(); renderService(); window.routerProvidersRender?.(); window.routerObservationsRender?.(); window.routerLocalRender?.(); window.routerTutorialRender?.();
  });
}
function renderAgents() {
  if (window.routerAppsRender) { window.routerAppsRender(); return; }
  $('#agents').replaceChildren(...[['Loomy', 'loomy.png', 'loomy'], ['WorkBuddy', 'workbuddy-color.svg', 'workbuddy']].map(([name, icon, id]) => {
    const row = el('div', 'row agent conn off'); row.dataset.app = id;
    const picture = el('span', 'ic'), image = el('img'); image.src = '/icons/' + icon; image.alt = ''; picture.append(image);
    const who = el('div', 'who'); who.append(el('div', 'name', name), el('div', 'sub', t('Not connected yet')));
    row.append(picture, who, el('div', 'fields'), el('div', 'ag-actions'));
    return row;
  }));
}

function layoutNavigation() {
  const sidebar = !phoneQuery.matches && (sidePick ? sidePick === 'side' : sideQuery.matches);
  document.body.classList.toggle('side-nav', sidebar);
  $('#side').hidden = !sidebar; $('#crumb').hidden = !sidebar; $('#sideToggle').hidden = phoneQuery.matches;
  const nav = $('#nav'), brand = $('.brand'), prefs = $('#prefs');
  if (sidebar) { $('#side .side-top').append(brand); $('#side .side-scroll').append(nav); $('#side .side-foot').prepend(prefs); }
  else { $('.top').prepend(brand); $('#crumb').after(nav); $('.actions').append(prefs); }
  const width = Math.max(168, Math.min(320, innerWidth * .36, Number(preferences.sidebarWidth) || 212));
  document.body.style.setProperty('--side-w', width + 'px');
  $('#sideEdge').setAttribute('aria-valuenow', String(Math.round(width)));
  $('#sideEdge').setAttribute('aria-label', t('Sidebar width'));
  renderNavigation();
}
function renderNavigation() {
  for (const page of $$('.view')) page.hidden = page.id !== 'view-' + view;
  for (const button of $$('#nav [data-view], #prefs')) {
    const on = (button.id === 'prefs' ? 'settings' : button.dataset.view) === view;
    button.classList.toggle('on', on);
    if (on) button.setAttribute('aria-current', 'page'); else button.removeAttribute('aria-current');
  }
  for (const button of $$('#usageTab [data-tab]')) {
    button.classList.toggle('on', button.dataset.tab === usageTab);
    button.setAttribute('aria-selected', String(button.dataset.tab === usageTab));
  }
  for (const [id, tab] of [['usagePane', 'usage'], ['ledgerPane', 'requests']]) $('#' + id).hidden = usageTab !== tab;
  for (const tab of ['general', 'about']) {
    $('#setPage-' + tab).hidden = settingsTab !== tab;
    $('#setTab-' + tab).classList.toggle('on', settingsTab === tab);
    $('#setTab-' + tab).setAttribute('aria-selected', String(settingsTab === tab));
  }
  for (const node of $$('.nav-sub')) node.remove();
  const current = view === 'settings' ? $('#prefs') : $('#nav [data-view="' + view + '"]');
  const items = view === 'usage' ? $$('#usageTab button') : view === 'settings' ? $$('#setTabs button') : [];
  if (document.body.classList.contains('side-nav') && items.length) {
    const list = el('div', 'nav-sub'); list.setAttribute('role', 'tablist');
    const highlight = el('i', 'nav-hl'); list.append(highlight);
    for (const [i, item] of items.entries()) {
      const selected = item.classList.contains('on');
      const button = el('button', 'nav-s' + (selected ? ' cur' : ''), item.textContent);
      button.type = 'button'; button.dataset.of = item.id;
      button.setAttribute('role', 'tab'); button.setAttribute('aria-selected', String(selected));
      button.onclick = () => item.click();
      if (selected) highlight.style.transform = 'translateY(' + (2 + i * 27) + 'px)';
      list.append(button);
    }
    current.after(list);
  }
  const pageName = view === 'settings' ? t('Settings') : current.querySelector('span[data-t]').textContent;
  const parts = view === 'settings' ? [pageName] : [t(view === 'agents' ? 'Work' : view === 'usage' ? 'Insights' : 'Models'), pageName];
  const tab = items.find((item) => item.classList.contains('on'));
  if (tab) parts.push(tab.textContent);
  $('#crumb').replaceChildren(...parts.flatMap((name, i) => [...(i ? [el('i', '', '/')] : []), el(i === parts.length - 1 ? 'b' : 'span', '', name)]));
  $('#pageTitle').textContent = parts.at(-1);
  document.title = parts.at(-1) + ' · API-router';
  $('#sideToggle').setAttribute('title', t('Toggle sidebar'));
  requestAnimationFrame(placeSelections);
}
function show(next) {
  view = views.includes(next) ? next : 'usage';
  preferences.view = view; savePreferences();
  renderNavigation();
  if (view === 'providers') window.routerProvidersOpen?.();
  if (view === 'agents') window.routerAppsOpen?.();
  window.routerObservationsOpen?.();
  window.routerLocalOpen?.();
}

function renderService() {
  $('#service').textContent = t(healthy ? 'Running' : 'Connection unavailable');
  $('#service').classList.toggle('ok', healthy);
  $('#address').textContent = location.host;
  $('#sideGw').classList.toggle('off', !healthy);
  $('#sideGw span').textContent = healthy ? t('Gateway') + ' ' + location.host : t('Connection unavailable');
  $('#version').textContent = serviceInfo?.version || '—';
  $('#started').textContent = serviceInfo ? new Date(serviceInfo.startedAt).toLocaleString(intlLang()) : '—';
  $('#model').textContent = t(serviceInfo?.modelAPIReady ? 'Ready' : 'Not connected yet');
  $('#runtime').textContent = t(serviceInfo?.runtimeReady ? 'Ready' : 'Not connected yet');
  window.routerObservationsRender?.();
  window.routerServiceRender?.();
}
async function ensureManagement() {
  if (managementToken) return managementToken;
  if (!managementSession) managementSession = (async () => {
    const controller = new AbortController(), timeout = setTimeout(() => controller.abort(), 3000);
    try {
      const response = await fetch('/api/management/session', { method: 'POST', headers: { 'X-API-Router-UI': '1' }, mode: 'same-origin', referrerPolicy: 'same-origin', credentials: 'omit', cache: 'no-store', redirect: 'error', signal: controller.signal });
      if (!response.ok) throw new Error('Connection failed. Check that the service is running.');
      const data = await response.json();
      if (data.schemaVersion !== 1 || typeof data.token !== 'string' || !/^[a-f0-9]{64}$/.test(data.token)) throw new Error('Connection failed. Check that the service is running.');
      return (managementToken = data.token);
    } finally { clearTimeout(timeout); }
  })().finally(() => { managementSession = null; });
  return managementSession;
}

async function managementFetch(path, options = {}) {
  for (let attempt = 0; attempt < 2; attempt++) {
    const token = await ensureManagement(), headers = new Headers(options.headers);
    headers.set('Authorization', 'Bearer ' + token);
    const response = await fetch(path, { ...options, headers, mode: 'same-origin', referrerPolicy: 'same-origin', credentials: 'omit', cache: 'no-store', redirect: 'error' });
    if (response.status !== 401 || response.headers.get('WWW-Authenticate') !== 'Bearer') return response;
    if (managementToken === token) managementToken = '';
    if (attempt === 1) return response;
  }
}

async function refreshService() {
  const controller = new AbortController(), timeout = setTimeout(() => controller.abort(), 3000);
  try {
    const response = await managementFetch('/api/service', { signal: controller.signal });
    if (!response.ok) throw new Error('Connection failed. Check that the service is running.');
    const data = await response.json();
    if (data.service !== 'api-router') throw new Error('Connection failed. Check that the service is running.');
    healthy = true; serviceInfo = data; $('#status').textContent = ''; renderService();
  } catch (error) {
    healthy = false; serviceInfo = null; managementToken = ''; renderService();
    window.routerAppsDisconnected?.(); window.routerProvidersDisconnected?.();
    window.routerObservationsDisconnected?.(); window.routerLocalDisconnected?.(); window.routerServiceDisconnected?.();
    throw error;
  } finally { clearTimeout(timeout); }
}
async function refresh() {
  if (busy) return;
  busy = true; $('#sync').classList.add('spin');
  try { await refreshService(); window.routerProvidersConnected?.(); if (view === 'agents') await window.routerAppsOpen?.(); await window.routerObservationsOpen?.(); await window.routerLocalOpen?.(); }
  catch (error) { $('#status').textContent = t(error.message === 'Invalid management token. Please try again.' ? error.message : 'Connection failed. Check that the service is running.'); }
  finally { busy = false; $('#sync').classList.remove('spin'); }
}
function openManagement() { return refresh(); }
for (const button of $$('#nav [data-view]')) button.onclick = () => show(button.dataset.view);
$('#prefs').onclick = () => show('settings');
$('#sideGw').onclick = () => show('gateway');
for (const button of $$('#usageTab [data-tab]')) button.onclick = () => { usageTab = button.dataset.tab; preferences.usageTab = usageTab; savePreferences(); renderNavigation(); window.routerObservationsOpen?.(); };
for (const button of $$('#setTabs [data-set]')) button.onclick = () => { settingsTab = button.dataset.set; renderNavigation(); };
for (const button of $$('#period [data-period]')) button.onclick = () => {
  for (const item of $$('#period [data-period]')) { item.classList.toggle('on', item === button); item.setAttribute('aria-pressed', String(item === button)); }
  placeSelections();
};
$('#sideToggle').setAttribute('title', t('Toggle sidebar'));
$('#sideToggle').onclick = () => { sidePick = document.body.classList.contains('side-nav') ? 'top' : 'side'; preferences.navigation = sidePick; savePreferences(); layoutNavigation(); };
$('#sideEdge').addEventListener('pointerdown', (event) => {
  if (event.button !== 0) return;
  event.preventDefault(); const edge = event.currentTarget; edge.setPointerCapture(event.pointerId);
  const move = (pointer) => { preferences.sidebarWidth = pointer.clientX; layoutNavigation(); };
  const end = () => { edge.removeEventListener('pointermove', move); edge.removeEventListener('lostpointercapture', end); savePreferences(); };
  edge.addEventListener('pointermove', move); edge.addEventListener('lostpointercapture', end);
});
$('#sideEdge').addEventListener('keydown', (event) => {
  if (!['ArrowLeft', 'ArrowRight'].includes(event.key)) return;
  event.preventDefault(); preferences.sidebarWidth = $('#side').offsetWidth + (event.key === 'ArrowLeft' ? -8 : 8); savePreferences(); layoutNavigation();
});
$('#sync').onclick = refresh; $('#usageReload').onclick = refresh;
sideQuery.addEventListener('change', layoutNavigation); phoneQuery.addEventListener('change', layoutNavigation);
window.addEventListener('resize', layoutNavigation);
renderPreferences(); renderAgents(); layoutNavigation(); renderService();
refresh();
