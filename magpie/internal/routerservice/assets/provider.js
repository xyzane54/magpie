'use strict';
// API-key configuration uses the original picker/editor visuals. It has no
// subscription discovery, account pools, catalog refresh or login callbacks.
(() => {
  for (const [key, zh, tw, ja, de] of [
    ['Qianwen AI Platform', '千问AI平台', '千問AI平台', 'Qianwen AI Platform', 'Qianwen AI Platform'],
    ['Alibaba Cloud Bailian (Beijing)', '阿里云百炼（北京）', '阿里雲百煉（北京）', 'Alibaba Cloud Bailian（北京）', 'Alibaba Cloud Bailian (Peking)'],
    ['Alibaba Cloud Model Studio (Singapore)', '阿里云 Model Studio（新加坡）', '阿里雲 Model Studio（新加坡）', 'Alibaba Cloud Model Studio（シンガポール）', 'Alibaba Cloud Model Studio (Singapur)'],
    ['API documentation', '接口文档', '介面文件', 'APIドキュメント', 'API-Dokumentation'],
    ['Use keys from {platform}. Qwen platforms and regions have separate keys.', '使用{platform}申请的密钥。不同千问平台及地域的密钥不能混用。', '使用{platform}申請的金鑰。不同千問平台及地域的金鑰不能混用。', '{platform}で発行されたキーを使用してください。Qwenのプラットフォームや地域ごとにキーが異なります。', 'Schlüssel von {platform} verwenden. Qwen-Plattformen und Regionen haben getrennte Schlüssel.'],
    ['Spark', '讯飞星火', '訊飛星火', 'Spark', 'Spark'],
    ['Spark MaaS', '讯飞星辰 MaaS', '訊飛星辰 MaaS', 'Spark MaaS', 'Spark MaaS'],
    ['Spark MaaS uses /v2 for Chat Completions, /anthropic for Anthropic Messages, and /v1 for Responses. Match the protocol to the address.', '星辰 MaaS 的 Chat Completions 使用 /v2，Anthropic Messages 使用 /anthropic，Responses 使用 /v1。请确保协议与地址匹配。', '星辰 MaaS 的 Chat Completions 使用 /v2，Anthropic Messages 使用 /anthropic，Responses 使用 /v1。請確保協定與位址相符。', 'Spark MaaSはChatに/v2、Anthropicに/anthropic、Responsesに/v1を使用します。プロトコルとアドレスを合わせてください。', 'Spark MaaS nutzt /v2 für Chat, /anthropic für Anthropic und /v1 für Responses. Protokoll und Adresse müssen übereinstimmen.'],
    ['Kimi (International)', 'Kimi（国际）', 'Kimi（國際）', 'Kimi（国際）', 'Kimi (International)'],
    ['Kimi (China)', 'Kimi（国内）', 'Kimi（中國）', 'Kimi（中国）', 'Kimi (China)'],
    ['API address', 'API 地址', 'API 位址', 'APIアドレス', 'API-Adresse'],
    ['Custom provider', '自定义供应商', '自訂供應商', 'カスタムプロバイダー', 'Eigener Anbieter'],
    ['API providers', '供应商', '供應商', 'プロバイダー', 'Anbieter'],
    ['API relays', '中转', '中繼', 'API中継', 'API-Vermittler'],
    ['Advanced settings', '高级设置', '進階設定', '詳細設定', 'Erweiterte Einstellungen'],
    ['Request headers', '请求头', '請求標頭', 'リクエストヘッダー', 'Anfrage-Header'],
    ['Extra body', '额外请求体', '額外請求本文', '追加ボディ', 'Zusätzlicher Body'],
    ['Base address, without /chat/completions or /messages.', '填写 API 基础地址，不含 /chat/completions 或 /messages。', '填寫 API 基礎位址，不含 /chat/completions 或 /messages。', 'APIのベースアドレスを入力します。/chat/completionsや/messagesは含めません。', 'API-Basisadresse ohne /chat/completions oder /messages.'],
    ['Saved locally in a separate credential file. Leave blank to keep the existing key.', '保存在独立的本机凭证文件中。编辑时留空保留已有密钥。', '儲存在獨立的本機憑證檔案中。編輯時留空保留現有金鑰。', 'ローカルの専用認証情報ファイルに保存します。空欄なら既存キーを保持します。', 'Lokal in einer separaten Zugangsdaten-Datei gespeichert. Leer lassen, um den vorhandenen Schlüssel zu behalten.'],
    ['JSON object of header names and string values. Leave blank to keep saved headers; {} clears them.', '填写请求头名称与字符串值的 JSON 对象。留空保留已有请求头，{} 清空。', '填寫標頭名稱與字串值的 JSON 物件。留空保留現有標頭，{} 清空。', 'ヘッダー名と文字列値のJSONオブジェクトです。空欄は保持、{}で削除します。', 'JSON-Objekt mit Headernamen und Zeichenketten. Leer behält gespeicherte Header, {} entfernt sie.'],
    ['JSON object. Leave blank to keep saved options; {} clears them. Model, stream, conversation and tools cannot be overridden.', '填写 JSON 对象。留空保留已有设置，{} 清空。不能覆盖模型、流式开关、对话或工具字段。', '填寫 JSON 物件。留空保留現有設定，{} 清空。不能覆寫模型、串流開關、對話或工具欄位。', 'JSONオブジェクトです。空欄は保持、{}で削除します。モデル、ストリーム、会話、ツールは上書きできません。', 'JSON-Objekt. Leer behält Einstellungen, {} entfernt sie. Modell, Stream, Gespräch und Werkzeuge dürfen nicht überschrieben werden.'],
    ['System uses environment proxy settings. Leave the custom URL blank to keep the saved proxy.', '系统代理使用环境中的代理设置。编辑时自定义地址留空，保留已保存的代理。', '系統代理使用環境中的代理設定。編輯時自訂位址留空，保留已儲存的代理。', 'システム設定は環境のプロキシ設定を使用します。カスタムURLが空欄なら保存済みプロキシを保持します。', 'System verwendet die Proxy-Einstellungen der Umgebung. Eine leere eigene URL behält den gespeicherten Proxy.'],
    ['System proxy', '系统代理', '系統代理', 'システムプロキシ', 'Systemproxy'],
    ['Saved; leave blank to keep', '已保存，留空保留', '已儲存，留空保留', '保存済み。空欄で保持', 'Gespeichert; leer lassen zum Beibehalten'],
    ['Saved headers: {names}', '已保存请求头：{names}', '已儲存標頭：{names}', '保存済みヘッダー: {names}', 'Gespeicherte Header: {names}'],
    ['Connect management to read or save providers.', '连接管理后可读取或保存供应商配置。', '連線管理後可讀取或儲存供應商設定。', 'プロバイダーの読込・保存には管理接続が必要です。', 'Zum Lesen oder Speichern von Anbietern die Verwaltung verbinden.'],
    ['No providers configured yet', '尚未配置供应商', '尚未設定供應商', 'プロバイダーは未設定です', 'Noch keine Anbieter konfiguriert'],
    ['Select a provider below, or add a custom API endpoint.', '选择下方供应商，或添加自定义 API 地址。', '選擇下方供應商，或新增自訂 API 位址。', '以下のプロバイダーを選ぶか、独自のAPIアドレスを追加してください。', 'Unten einen Anbieter wählen oder einen eigenen API-Endpunkt hinzufügen.'],
    ["Provider saved. Bind it to an application Profile to send cloud requests. Saving does not verify upstream access.", "供应商已保存。绑定应用 Profile 后可发送云端请求；保存不表示上游访问已验证。", "供應商已儲存。綁定應用程式 Profile 後可傳送雲端請求；儲存不表示已驗證上游存取。", "プロバイダーを保存しました。アプリのProfileに紐付けるとクラウドリクエストを送れます。保存だけでは接続は検証されません。", "Anbieter gespeichert. Zum Senden von Cloud-Anfragen mit einem Anwendungsprofil verbinden. Speichern prüft den Anbieterzugriff nicht."],
    ['Connect management, then click Save again.', '请先连接管理，再次点击“保存”。', '請先連線管理，再次點擊「儲存」。', '管理に接続してから、保存をもう一度押してください。', 'Die Verwaltung verbinden, dann erneut Speichern wählen.'],
    ['Provider configuration is invalid. Check fields and advanced JSON.', '供应商配置无效，请检查字段和高级设置中的 JSON。', '供應商設定無效，請檢查欄位及進階設定中的 JSON。', '設定が無効です。項目と詳細設定のJSONを確認してください。', 'Ungültige Konfiguration. Felder und JSON in den erweiterten Einstellungen prüfen.'],
    ['This provider changed. Reopen the editor before saving.', '该供应商配置已发生变化，请重新打开编辑框后保存。', '供應商設定已變更，請重新開啟編輯器後儲存。', '設定が変更されました。編集画面を開き直してください。', 'Der Anbieter wurde geändert. Den Editor vor dem Speichern neu öffnen.'],
    ['Provider configuration is unavailable. Check permissions and configuration files.', '配置存储不可用，请检查权限及配置文件。', '設定儲存無法使用，請檢查權限及設定檔案。', '設定を利用できません。権限と設定ファイルを確認してください。', 'Konfiguration nicht verfügbar. Berechtigungen und Konfigurationsdateien prüfen.'],
    ['Headers and extra body must be JSON objects.', '请求头和额外请求体必须是 JSON 对象。', '請求標頭和額外請求本文必須是 JSON 物件。', 'ヘッダーと追加ボディはJSONオブジェクトが必要です。', 'Header und zusätzlicher Body müssen JSON-Objekte sein.'],
    ['Header values must be strings.', '请求头的值必须是字符串。', '標頭的值必須是字串。', 'ヘッダー値は文字列である必要があります。', 'Headerwerte müssen Zeichenketten sein.'],
    ['Extra body cannot override model, stream, conversation or tools.', '额外请求体不能覆盖模型、流式开关、对话或工具字段。', '額外請求本文不能覆寫模型、串流開關、對話或工具欄位。', '追加ボディでモデル、ストリーム、会話、ツールは上書きできません。', 'Der zusätzliche Body darf Modell, Stream, Gespräch und Werkzeuge nicht überschreiben.'],
    ['Management token cannot be used as an API key.', '管理令牌不能用作云端 API Key。', '管理權杖不能用作雲端 API Key。', '管理トークンをAPIキーとして使用することはできません。', 'Das Verwaltungstoken darf nicht als API-Schlüssel verwendet werden.'],
    ['Confirm deletion', '确认删除', '確認刪除', '削除を確認', 'Löschen bestätigen'],
    ['Provider deleted', '供应商已删除', '供應商已刪除', 'プロバイダーを削除しました', 'Anbieter gelöscht'],
    ['Provider saved. Models discovered and cached.', '供应商已保存，已自动获取并缓存模型列表。', '供應商已儲存，已自動取得並快取模型清單。', 'プロバイダーを保存し、モデル一覧を取得・キャッシュしました。', 'Anbieter gespeichert und Modellliste zwischengespeichert.'],
    ['Provider saved. Using built-in preset models.', '供应商已保存，当前使用内置预设模型列表。', '供應商已儲存，目前使用內建預設模型清單。', 'プロバイダーを保存しました。内蔵のプリセットモデルを使用します。', 'Anbieter gespeichert. Integrierte Standardmodelle werden verwendet.'],
    ['Local on-device models', '本地端侧模型', '本機端側模型', 'ローカルオンデバイスモデル', 'Lokale Gerätemodelle'],
    ['{count} discovered models', '已探测 {count} 个模型', '已探測 {count} 個模型', '{count} 個の検出モデル', '{count} erkannte Modelle'],
    ['{count} preset models', '预设 {count} 个模型', '預設 {count} 個模型', '{count} 個のプリセットモデル', '{count} Standardmodelle'],
    ['No models cached', '暂无缓存模型', '暫無快取模型', 'キャッシュモデルなし', 'Keine Modelle zwischengespeichert'],
    ['Installed', '已安装', '已安裝', 'インストール済み', 'Installiert'],
  ]) for (const [lang, text] of [['zh', zh], ['zh-TW', tw], ['ja', ja], ['de', de]]) I18N[lang][key] = text;

  let presets = [], providers = [], current = null, selectedPreset = null;
  let selectedProtocol = 'chat', proxyMode = 'system', confirmingDelete = false, loading = null, formBusy = false, returnFocus = null;
  const state = { discoveredModels: [], effectiveModels: [], localModels: [], candidateModels: [], modelSource: '' };
  const protocolNames = { chat: 'OpenAI Chat Completions', responses: 'OpenAI Responses', anthropic: 'Anthropic Messages' };
  const iconFor = (name) => {
    const box = el('span', 'ic');
    if (name) { const image = el('img'); image.src = '/icons/' + name + '.svg'; image.alt = ''; box.append(image); } else box.textContent = '◇';
    return box;
  };
  function requireManagement() {
    if (managementToken) return true;
    $('#providerError').textContent = t('Checking connection…'); openManagement(); return false;
  }
  async function api(path, method = 'GET', body) {
    const response = await managementFetch('/api/cloud-providers' + path, {
      method, credentials: 'omit', cache: 'no-store', redirect: 'error',
      headers: { Authorization: 'Bearer ' + managementToken, ...(body ? { 'Content-Type': 'application/json' } : {}) },
      ...(body ? { body: JSON.stringify(body) } : {})
    });
    if (!response.ok) {
      if (response.status === 401) { managementToken = ''; serviceInfo = null; renderService(); window.routerAppsDisconnected?.(); providers = []; renderProviders(); throw new Error('Connect management, then click Save again.'); }
      if (response.status === 409) throw new Error('This provider changed. Reopen the editor before saving.');
      throw new Error(response.status === 400 ? 'Provider configuration is invalid. Check fields and advanced JSON.' : 'Provider configuration is unavailable. Check permissions and configuration files.');
    }
    return response.json();
  }
  function renderPresets() {
    const box = $('#providerPresets'); box.replaceChildren();
    for (const [kind, label] of [['vendor', 'API providers'], ['relay', 'API relays'], ['custom', 'Custom provider']]) {
      const heading = el('div', 'kind'); heading.append(el('b', '', t(label))); box.append(heading);
      const grid = el('div', 'grid');
      for (const preset of presets.filter(p => p.kind === kind)) {
        const button = el('button', 'tile'); button.type = 'button'; button.dataset.preset = preset.id;
        const name = el('span', 'nm'); name.append(el('span', 'n', t(preset.name)));
        button.append(iconFor(preset.icon), name); button.onclick = () => openProvider(preset); grid.append(button);
      }
      box.append(grid);
    }
  }
  function isReadyLocalModel(lm) {
    if (!lm) return false;
    const status = String(lm.status || '').toLowerCase();
    return Boolean(lm.installed === true || ['ready', 'running', 'installed', 'busy', 'idle'].includes(status));
  }
  function providerModelSummary(provider, preset) {
    const discovered = Array.isArray(provider?.discoveredModels) ? provider.discoveredModels : [];
    const effective = Array.isArray(provider?.effectiveModels) ? provider.effectiveModels : (Array.isArray(provider?.models) ? provider.models : (Array.isArray(preset?.models) ? preset.models : []));
    if (discovered.length > 0 || (provider?.modelSource === 'discovered' && effective.length > 0)) return { text: t('{count} discovered models', { count: discovered.length || effective.length }), source: 'discovered' };
    if (effective.length > 0) return { text: t('{count} preset models', { count: effective.length }), source: 'preset' };
    return { text: t('No models cached'), source: 'none' };
  }
  function renderLocalModelsSection() {
    const section = $('#localProvidersSection'), box = $('#localProviders');
    if (!section || !box) return;
    box.replaceChildren();
    const readyLocal = (state.localModels || []).filter(isReadyLocalModel);
    if (!managementToken || !readyLocal.length) { section.hidden = true; return; }
    section.hidden = false;
    for (const lm of readyLocal) {
      const id = lm.id || lm.modelId || '', selector = lm.selector || (id ? 'local/' + id : '');
      const row = el('div', 'row provider local-provider'); row.dataset.localModel = id; row.dataset.selector = selector;
      const who = el('div', 'who'); who.append(el('div', 'name', lm.name || id), el('div', 'sub', selector));
      const statusNorm = String(lm.status || '').toLowerCase(), isReady = statusNorm === 'ready' || statusNorm === 'running';
      const badge = el('span', 'router-state ok', t(isReady ? 'Ready' : 'Installed')); badge.dataset.status = statusNorm || 'installed';
      const jump = el('button', 'text', t('Local model')); jump.type = 'button'; jump.dataset.action = 'open-local-model';
      jump.onclick = () => { if (typeof show === 'function') show('local-model'); else $('#nav [data-view="local-model"]')?.click(); };
      row.append(iconFor('spark'), who, badge, jump); box.append(row);
    }
  }
  function renderProviders() {
    const box = $('#providers'); box.replaceChildren();
    $('#providerStatus').textContent = managementToken ? '' : t('Checking connection…');
    renderLocalModelsSection();
    if (!providers.length) {
      const empty = el('div', 'empty-state'); empty.append(el('b', '', t('No providers configured yet')), el('span', '', t('Select a provider below, or add a custom API endpoint.')));
      if (managementToken) box.append(empty);
      return;
    }
    for (const provider of providers) {
      const row = el('div', 'row provider'); row.dataset.provider = provider.id;
      const preset = presets.find(p => p.id === provider.preset);
      const who = el('div', 'who'); who.append(el('div', 'name', provider.name), el('div', 'sub', provider.baseURL));
      const summaryInfo = providerModelSummary(provider, preset);
      const summary = el('span', 'router-state provider-models-summary', summaryInfo.text); summary.dataset.modelSource = summaryInfo.source;
      const edit = el('button', 'text', t('Edit')); edit.type = 'button'; edit.onclick = () => openProvider(preset || presets.at(-1), provider);
      row.append(iconFor(preset?.icon), who, summary, edit); box.append(row);
    }
  }
  async function openPage() {
    if (!loading) loading = fetch('/provider-presets.json', { cache: 'no-store' }).then(r => r.json()).then(data => { presets = data; renderPresets(); });
    try {
      await loading;
      if (managementToken) { const data = await api(''); providers = data.providers || []; state.localModels = data.localModels || []; state.candidateModels = data.candidateModels || []; }
      renderProviders();
    } catch (error) { $('#providerStatus').textContent = t(error.message || 'Provider configuration is unavailable. Check permissions and configuration files.'); }
  }
  function endpointHelp() {
    let hostname = ''; try { hostname = new URL($('#cloudBase').value).hostname; } catch {}
    const known = { 'maas.qianwenaiapi.com': 'qianwen-ai', 'dashscope.aliyuncs.com': 'qwen-cn', 'dashscope-intl.aliyuncs.com': 'qwen', 'maas-api.cn-huabei-1.xf-yun.com': 'spark-maas' };
    const platform = presets.find(p => p.id === known[hostname]);
    $('#cloudPlatformHint').textContent = platform?.id === 'spark-maas' ? t('Spark MaaS uses /v2 for Chat Completions, /anthropic for Anthropic Messages, and /v1 for Responses. Match the protocol to the address.') : platform ? t('Use keys from {platform}. Qwen platforms and regions have separate keys.', { platform: t(platform.name) }) : '';
    for (const [selector, url] of [['#cloudGetKey', platform?.keysURL || selectedPreset?.keysURL], ['#cloudDocs', platform?.docsURL || selectedPreset?.docsURL]]) {
      const link = $(selector); link.hidden = !url || !/^https:\/\//.test(url); if (link.hidden) link.removeAttribute('href'); else link.href = url;
    }
  }
  function protocols() {
    chooseButtons($('#cloudProtocol'), Object.entries(protocolNames), selectedProtocol, id => {
      const oldBase = selectedPreset?.endpoints?.[selectedProtocol] || '';
      if (!$('#cloudBase').value || $('#cloudBase').value === oldBase) $('#cloudBase').value = selectedPreset?.endpoints?.[id] || '';
      selectedProtocol = id; protocols(); endpointHelp();
    });
    chooseButtons($('#cloudProxyMode'), [['system', t('System proxy')], ['direct', t('Direct')], ['custom', t('Custom')]], proxyMode, id => { proxyMode = id; protocols(); });
    $('#cloudProxyURL').hidden = proxyMode !== 'custom';
  }
  function openProvider(preset, provider = null) {
    current = provider; selectedPreset = preset; returnFocus = document.activeElement;
    selectedProtocol = provider?.protocol || preset.defaultProtocol || Object.keys(preset.endpoints)[0] || 'chat';
    proxyMode = provider?.proxyMode || 'system'; confirmingDelete = false;
    state.discoveredModels = Array.isArray(provider?.discoveredModels) ? [...provider.discoveredModels] : [];
    state.effectiveModels = Array.isArray(provider?.effectiveModels) ? [...provider.effectiveModels] : (Array.isArray(provider?.models) ? [...provider.models] : [...(preset?.models || [])]);
    state.modelSource = provider?.modelSource || (state.discoveredModels.length ? 'discovered' : (state.effectiveModels.length ? 'preset' : ''));
    $('#providerForm').reset(); $('#providerIcon').replaceChildren(...iconFor(preset.icon).childNodes); $('#providerTitle').textContent = provider?.name || t(preset.name);
    for (const [selector, url] of [['#providerWebsite', preset.website], ['#cloudGetKey', preset.keysURL]]) {
      const link = $(selector); link.hidden = !url || !/^https:\/\//.test(url);
      if (!link.hidden) { link.href = url; if (selector === '#providerWebsite') link.textContent = new URL(url).host + ' ↗'; } else link.removeAttribute('href');
    }
    $('#cloudName').value = provider?.name || (preset.id === 'custom' ? '' : t(preset.name));
    $('#cloudBase').value = provider?.baseURL || preset.endpoints[selectedProtocol] || '';
    $('#cloudKey').type = 'password'; $('#cloudKeyShow').textContent = t('Show');
    $('#cloudKey').placeholder = provider?.hasKey ? t('Saved; leave blank to keep') : '';
    $('#cloudHeaders').placeholder = provider?.headerNames?.length ? t('Saved; leave blank to keep') : '{"X-Project": "project-id"}';
    $('#cloudHeaderHint').textContent = provider?.headerNames?.length ? t('Saved headers: {names}', { names: provider.headerNames.join(', ') }) : '';
    $('#cloudExtraBody').placeholder = provider?.hasExtraBody ? t('Saved; leave blank to keep') : '{"prompt_cache_key": "${sessionId}"}';
    $('#cloudProxyURL').placeholder = provider?.hasProxyURL ? t('Saved; leave blank to keep') : 'http://127.0.0.1:7890';
    $('#providerDelete').hidden = !provider; $('#providerDelete').textContent = t('Delete'); $('#providerError').textContent = ''; $('#cloudAdvanced').open = false;
    protocols(); endpointHelp(); $('#providerModal').hidden = false; $('#cloudKey').focus({ preventScroll: true });
  }
  function closeProvider() { if (!formBusy) { $('#providerModal').hidden = true; $('#providerForm').reset(); current = null; returnFocus?.focus({ preventScroll: true }); } }
  function jsonField(id) {
    const text = $(id).value.trim(); if (!text) return undefined;
    let result; try { result = JSON.parse(text); } catch { throw new Error('Headers and extra body must be JSON objects.'); }
    if (!result || Array.isArray(result) || typeof result !== 'object') throw new Error('Headers and extra body must be JSON objects.');
    return result;
  }
  function draft() {
    const headers = jsonField('#cloudHeaders'), extraBody = jsonField('#cloudExtraBody');
    if (headers && Object.values(headers).some(value => typeof value !== 'string')) throw new Error('Header values must be strings.');
    const reserved = ['model', 'stream', 'messages', 'input', 'contents', 'system', 'systemInstruction', 'system_instruction', 'instructions', 'tools', 'previous_response_id'];
    if (extraBody && Object.keys(extraBody).some(key => reserved.includes(key.replace(/^!/, '')))) throw new Error('Extra body cannot override model, stream, conversation or tools.');
    const apiKey = $('#cloudKey').value.trim(); if (apiKey && apiKey === managementToken) throw new Error('Management token cannot be used as an API key.');
    return { id: current?.id || '', revision: current?.revision || 0, name: $('#cloudName').value.trim(), preset: selectedPreset.id, protocol: selectedProtocol, baseURL: $('#cloudBase').value.trim(), proxyMode,
      ...(apiKey ? { apiKey } : {}), ...(headers ? { headers } : {}), ...(extraBody ? { extraBody } : {}), ...($('#cloudProxyURL').value.trim() ? { proxyURL: $('#cloudProxyURL').value.trim() } : {}) };
  }
  function hasUnsavedConnectionChanges(d) {
    return !current || !current.id || Boolean(d.apiKey || d.headers || d.extraBody || d.proxyURL || d.protocol !== current.protocol || d.baseURL !== current.baseURL || (d.proxyMode || 'system') !== (current.proxyMode || 'system'));
  }
  async function fetchModels(target = null, options = {}) {
    const d = target || draft(), canPersist = Boolean(d.id && (options.persist ?? !hasUnsavedConnectionChanges(d))), useFallback = Boolean(options.fallback ?? canPersist);
    const presetObj = presets.find(p => p.id === (d.preset || selectedPreset?.id)) || selectedPreset, presetFallbackModels = Array.isArray(presetObj?.models) ? [...presetObj.models] : [];
    const payload = target && options.persist ? { id: d.id, persist: true, fallback: true } : { ...d, ...(canPersist ? { persist: true } : {}), ...(useFallback ? { fallback: true } : {}) };
    const query = canPersist ? '?persist=1&fallback=1' : (useFallback ? '?fallback=1' : '');
    try {
      const result = await api('/models' + query, 'POST', payload);
      const rawModels = Array.isArray(result?.models) && result.models.length ? result.models : (Array.isArray(result?.documentedModels) && result.documentedModels.length ? result.documentedModels : []);
      if (result?.source === 'discovered') { state.discoveredModels = [...rawModels]; state.effectiveModels = [...rawModels]; state.modelSource = 'discovered'; }
      else if (result?.source === 'none') { state.discoveredModels = []; state.effectiveModels = []; state.modelSource = 'none'; }
      else if (!result?.fallback && result?.source !== 'preset' && rawModels.length) { state.discoveredModels = [...rawModels]; state.effectiveModels = [...rawModels]; state.modelSource = result?.source || 'discovered'; }
      else { const models = rawModels.length ? rawModels : presetFallbackModels; state.discoveredModels = []; state.effectiveModels = [...models]; state.modelSource = models.length ? 'preset' : 'none'; }
      if (d.id) { const idx = providers.findIndex(p => p.id === d.id); if (idx >= 0) providers[idx] = { ...providers[idx], discoveredModels: [...state.discoveredModels], models: [...state.effectiveModels], effectiveModels: [...state.effectiveModels], modelSource: state.modelSource }; }
      return { models: state.effectiveModels, discoveredModels: state.discoveredModels, source: state.modelSource, fallback: Boolean(result?.fallback || state.modelSource === 'preset' || state.modelSource === 'none') };
    } catch (error) {
      if (!useFallback) throw error;
      const cached = Array.isArray(d?.discoveredModels) && d.discoveredModels.length ? [...d.discoveredModels] : [];
      if (cached.length) { state.discoveredModels = cached; state.effectiveModels = cached; state.modelSource = 'discovered'; }
      else { state.discoveredModels = []; state.effectiveModels = [...presetFallbackModels]; state.modelSource = presetFallbackModels.length ? 'preset' : 'none'; }
      return { models: state.effectiveModels, discoveredModels: state.discoveredModels, source: state.modelSource, fallback: true };
    }
  }
  async function saveProvider() {
    const saved = await api('', 'POST', draft()), savedProvider = saved?.provider;
    const fetchResult = savedProvider?.id ? await fetchModels(savedProvider, { persist: true, fallback: true }) : null;
    $('#cloudKey').value = ''; formBusy = false; closeProvider(); await openPage();
    if (fetchResult && fetchResult.source === 'discovered' && fetchResult.discoveredModels?.length) $('#providerStatus').textContent = t('Provider saved. Models discovered and cached.');
    else if (fetchResult && fetchResult.source === 'preset' && fetchResult.models?.length) $('#providerStatus').textContent = t('Provider saved. Using built-in preset models.');
    else $('#providerStatus').textContent = t("Provider saved. Bind it to an application Profile to send cloud requests. Saving does not verify upstream access.");
  }
  async function perform(action) {
    if (formBusy || !requireManagement()) return;
    formBusy = true; $('#providerForm').classList.add('busy'); $('#providerError').textContent = '';
    try { await action(); }
    catch (error) { $('#providerError').textContent = t(error instanceof TypeError || error instanceof SyntaxError ? 'Provider configuration is unavailable. Check permissions and configuration files.' : error.message); }
    finally { formBusy = false; $('#providerForm').classList.remove('busy'); }
  }
  $('#providerForm').onsubmit = event => { event.preventDefault(); perform(saveProvider); };
  if ($('#cloudFetchModels')) $('#cloudFetchModels').onclick = () => perform(() => fetchModels(null, { fallback: true }));
  $('#providerDelete').onclick = () => {
    if (!confirmingDelete) { confirmingDelete = true; $('#providerDelete').textContent = t('Confirm deletion'); return; }
    perform(async () => { await api('', 'DELETE', { id: current.id, revision: current.revision }); formBusy = false; closeProvider(); await openPage(); $('#providerStatus').textContent = t('Provider deleted'); });
  };
  $('#cloudBase').addEventListener('input', endpointHelp); $('#providerCancel').onclick = closeProvider; $('#providerClose').onclick = closeProvider;
  $('#providerModal').onclick = event => { if (event.target === $('#providerModal')) closeProvider(); };
  $('#cloudKeyShow').onclick = () => { const show = $('#cloudKey').type === 'password'; $('#cloudKey').type = show ? 'text' : 'password'; $('#cloudKeyShow').textContent = t(show ? 'Hide' : 'Show'); };
  $('#addProvider').onclick = () => { $('#addSheet').hidden = !$('#addSheet').hidden; };
  document.addEventListener('keydown', event => {
    if ($('#providerModal').hidden) return;
    if (event.key === 'Escape') closeProvider();
    if (event.key === 'Tab') {
      const controls = $$('#providerModal input, #providerModal button, #providerModal textarea, #providerModal summary').filter(node => node.offsetParent && !node.disabled);
      const edge = event.shiftKey ? controls[0] : controls.at(-1);
      if (document.activeElement === edge) { event.preventDefault(); controls[event.shiftKey ? controls.length - 1 : 0].focus({ preventScroll: true }); }
    }
  });
  window.routerProvidersOpen = openPage; window.routerProvidersRender = () => { renderPresets(); renderProviders(); };
  window.routerProvidersConnected = () => { if (view === 'providers') openPage(); };
  window.routerProviderState = state; window.routerProviderFetchModels = fetchModels; window.routerProviderSave = saveProvider;
  translateStatic(); renderNavigation(); if (view === 'providers') openPage();
})();
