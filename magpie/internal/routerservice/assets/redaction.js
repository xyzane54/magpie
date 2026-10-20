(() => {
  'use strict';
  for (const [key, zh, tw, ja, de] of [
    ['Redaction intent', '脱敏意图', '脫敏意圖', '匿名化の設定意図', 'Anonymisierungsabsicht'],
    ['Enable redaction in an application Profile to view its saved intent here.', '在应用 Profile 中开启脱敏后，可在此查看脱敏意图与生效状态。', '在應用程式 Profile 中開啟脫敏後，可在此檢視脫敏意圖與生效狀態。', 'アプリのProfileで匿名化を有効にすると、保存した設定と有効状態をここで確認できます。', 'Nach Aktivierung der Anonymisierung in einem Anwendungsprofil werden hier die gespeicherte Absicht und der Status angezeigt.'],
    ['Saved intent is preserved when the local model is disabled. Local execution does not use redaction.', '关闭本地模型会保留脱敏意图。本地执行不脱敏。', '關閉本機模型會保留脫敏意圖。本機執行不脫敏。', 'ローカルモデルを無効にしても設定意図は保持されます。ローカル実行は匿名化しません。', 'Die Absicht bleibt beim Deaktivieren des lokalen Modells gespeichert. Lokale Ausführung nutzt keine Anonymisierung.'],
    ['Production redaction is unavailable. Recognition quality, latency and integration acceptance are pending.', '生产脱敏不可用，识别质量、时延与集成验收待完成。', '正式脫敏不可用，識別品質、延遲與整合驗收待完成。', '本番の匿名化は利用できません。認識精度・処理時間・統合の受け入れ確認は未完了です。', 'Produktive Anonymisierung ist nicht verfügbar. Die Prüfung von Erkennungsqualität und Latenz sowie die Integrationsabnahme stehen aus.'],
    ['Intent on · inactive', '意图开启 · 未生效', '意圖開啟 · 未生效', '設定オン・無効', 'Absicht an · inaktiv'],
    ['Intent off · inactive', '意图关闭 · 未生效', '意圖關閉 · 未生效', '設定オフ・無効', 'Absicht aus · inaktiv'],
    ['Disable all redaction intents', '关闭全部脱敏意图', '關閉全部脫敏意圖', 'すべての匿名化設定をオフ', 'Alle Anonymisierungsabsichten deaktivieren'],
    ['Redaction statistics are not connected.', '脱敏统计尚未接通。', '脫敏統計尚未接通。', '匿名化の統計は未接続です。', 'Anonymisierungsstatistiken sind nicht angebunden.'],
    ['Redaction management unavailable. Refresh and check configuration.', '脱敏管理不可用，请刷新并检查配置。', '脫敏管理不可用，請重新整理並檢查設定。', '匿名化管理は利用できません。更新して設定を確認してください。', 'Anonymisierungsverwaltung nicht verfügbar. Aktualisieren und Konfiguration prüfen.'],
    ['Profiles changed. Refresh before changing redaction intent.', 'Profile 已变更，请刷新后再修改脱敏意图。', 'Profile 已變更，請重新整理後再修改脫敏意圖。', 'Profileが変更されました。更新してから匿名化の設定を変更してください。', 'Profile geändert. Vor dem Ändern der Absicht aktualisieren.'],
    ['Redaction intents disabled.', '脱敏意图已关闭。', '脫敏意圖已關閉。', '匿名化の設定をオフにしました。', 'Anonymisierungsabsichten deaktiviert.'],
    ['Processed requests', '处理请求数', '處理請求數', '処理リクエスト数', 'Verarbeitete Anfragen'],
    ['Extracted entities', '提取实体数', '擷取實體數', '抽出エンティティ数', 'Extrahierte Entitäten'],
    ['History cache hits', '历史缓存命中', '歷史快取命中', '履歴キャッシュヒット', 'Treffer im Verlaufscache'],
    ['Blocked requests', '阻断请求数', '阻擋請求數', '遮断リクエスト数', 'Blockierte Anfragen'],
    ['Redaction duration (ms)', '脱敏累计耗时（毫秒）', '脫敏累計耗時（毫秒）', '匿名化の累計時間（ミリ秒）', 'Anonymisierungsdauer (ms)'],
    ['Production pipeline connected', '生产管线已接通', '正式管線已接通', '本番パイプライン接続済み', 'Produktionspipeline verbunden'],
    ['Model and dependencies ready', '模型与依赖已准备', '模型與依賴已準備', 'モデルと依存関係の準備完了', 'Modell und Abhängigkeiten bereit'],
    ['Recognition acceptance', '识别能力验收', '識別能力驗收', '認識受け入れ', 'Erkennungsabnahme'],
    ['Cloud redaction active', '云端脱敏生效', '雲端去識別化生效', 'クラウド匿名化有効', 'Cloud-Anonymisierung aktiv'],
    ['Active', '生效', '生效', '有効', 'Aktiv'],
    ['Inactive', '未生效', '未生效', '無効', 'Inaktiv'],
    ['Production redaction is connected but recognition acceptance is pending.', '生产脱敏管线已接通，但识别能力仍待验收。', '正式去識別化管線已接通，但識別能力仍待驗收。', '本番匿名化パイプラインは接続済みですが、認識受け入れ確認は保留中です。', 'Die Produktionspipeline ist verbunden, aber die Erkennungsabnahme steht aus.'],
    ['Production redaction is active for eligible cloud requests.', '符合条件的云端请求已启用生产脱敏。', '符合條件的雲端請求已啟用正式去識別化。', '対象となるクラウドリクエストで本番匿名化が有効です。', 'Produktive Anonymisierung ist für geeignete Cloud-Anfragen aktiv.'],
    ['yes', '是', '是', 'はい', 'Ja'],
    ['no', '否', '否', 'いいえ', 'Nein'],
    ['available', '可用', '可用', '利用可能', 'Verfügbar'],
    ['unknown', '未知', '未知', '不明', 'Unbekannt'],
    ['unavailable', '不可用', '不可用', '利用不可', 'Nicht verfügbar'],
    ['experimental', '实验性试用', '實驗性試用', '試験利用', 'Experimenteller Test'],
    ['Experimental trial ready', '实验性试用已准备', '實驗性試用已準備', '試験利用の準備完了', 'Experimenteller Test bereit'],
    ['Formal quality accepted', '正式质量已验收', '正式品質已驗收', '正式な品質受け入れ済み', 'Formale Qualitätsabnahme erfolgt'],
    ['Total redaction budget', '脱敏总预算', '去識別化總預算', '匿名化の合計制限時間', 'Gesamtes Zeitlimit für Anonymisierung'],
    ['30 seconds', '30秒', '30秒', '30秒', '30 Sekunden'],
    ['Experimental trial active', '实验性试用生效', '實驗性試用生效', '試験利用が有効', 'Experimenteller Test aktiv'],
    ['Experimental trial: formal quality is not accepted. Misses and false positives are possible. Invalid results still block cloud requests. The total redaction budget is 30 seconds.', '实验性试用，未正式质量验收；可能漏检或误报。无效结果仍阻断云端请求，脱敏总预算30秒。', '實驗性試用，未正式品質驗收；可能漏檢或誤報。無效結果仍阻擋雲端請求，去識別化總預算30秒。', '試験利用であり、正式な品質受け入れは未完了です。見逃しや誤検出の可能性があります。無効な結果ではクラウドリクエストを遮断します。匿名化の合計制限時間は30秒です。', 'Experimenteller Test ohne formale Qualitätsabnahme. Übersehene Daten und Fehlalarme sind möglich. Ungültige Ergebnisse blockieren Cloud-Anfragen weiterhin. Das gesamte Zeitlimit beträgt 30 Sekunden.'],
    ['Select an application Profile to enable the experimental trial.', '可在应用 Profile 中选择开启实验性试用。', '可在應用程式 Profile 中選擇開啟實驗性試用。', 'アプリのProfileで試験利用を有効にできます。', 'Den experimentellen Test im jeweiligen Anwendungsprofil aktivieren.'],
    ['The experimental trial is not ready. Existing intent is retained and new enabling is blocked.', '实验性试用尚未准备好，保留已有意图并阻止新增开启。', '實驗性試用尚未準備好，保留既有意圖並阻止新增開啟。', '試験利用の準備が未完了です。保存済みの設定意図を保持し、新しい有効化は遮断します。', 'Der experimentelle Test ist nicht bereit. Gespeicherte Absichten bleiben erhalten; neue Aktivierungen werden blockiert.'],
    ['Session cache hit rate', '会话缓存命中率', '工作階段快取命中率', 'セッションキャッシュヒット率', 'Sitzungscache-Trefferquote'],
    ['Average redaction duration (ms)', '平均单次脱敏耗时（毫秒）', '平均單次脫敏耗時（毫秒）', '平均匿名化時間（ミリ秒）', 'Durchschnittliche Anonymisierungsdauer (ms)'],
    ['Blocked reasons', '阻断原因分布', '阻擋原因分佈', '遮断理由の内訳', 'Aufschlüsselung der Blockierungsgründe'],
    ['None', '无', '無', 'なし', 'Keine'],
    ['Redaction failed', '脱敏处理失败', '脫敏處理失敗', '匿名化処理に失敗', 'Anonymisierung fehlgeschlagen'],
    ['Redaction canceled', '脱敏已取消', '脫敏已取消', '匿名化がキャンセルされました', 'Anonymisierung abgebrochen'],
    ['Redaction runtime closed', '脱敏运行时已关闭', '脫敏執行階段已關閉', '匿名化ランタイムが終了しました', 'Anonymisierungslaufzeit geschlossen'],
    ['Redaction extraction failed', '敏感实体提取失败', '敏感實體擷取失敗', '機密エンティティの抽出に失敗', 'Entitätsextraktion fehlgeschlagen'],
    ['Redaction input exceeds limit', '脱敏输入超过上限', '脫敏輸入超過上限', '匿名化入力が上限を超過', 'Anonymisierungseingabe überschreitet Limit'],
    ['Invalid redaction input', '脱敏输入无效', '脫敏輸入無效', '無効な匿名化入力', 'Ungültige Anonymisierungseingabe'],
    ['Redaction returned invalid response', '脱敏模型返回格式无效', '脫敏模型傳回格式無效', '匿名化モデルの応答形式が無効', 'Anonymisierung lieferte ungültige Antwort'],
    ['Redaction result failed validation', '脱敏结果校验失败', '脫敏結果驗證失敗', '匿名化結果の検証に失敗', 'Validierung des Anonymisierungsergebnisses fehlgeschlagen'],
    ['Redaction entity conflict', '敏感实体映射冲突', '敏感實體映射衝突', '機密エンティティのマッピング競合', 'Entitätskonflikt bei der Anonymisierung'],
    ['Redaction mapping limit exceeded', '敏感实体映射数量超限', '敏感實體映射數量超限', 'エンティティマッピング数が上限を超過', 'Zuordnungslimit für Anonymisierung überschritten'],
    ['Redaction placeholder limit exceeded', '脱敏占位符数量超限', '脫敏佔位符數量超限', 'プレースホルダー数が上限を超過', 'Platzhalterlimit für Anonymisierung überschritten'],
    ['Redaction session released', '脱敏会话已释放', '脫敏工作階段已釋放', '匿名化セッションが解放されました', 'Anonymisierungssitzung freigegeben'],
    ['Redaction response exceeds limit', '脱敏响应超过上限', '脫敏回應超過上限', '匿名化応答が上限を超過', 'Anonymisierungsantwort überschreitet Limit'],
    ['Redaction specification failed', '脱敏规范校验未通过', '脫敏規範驗證未通過', '匿名化仕様の検証に失敗', 'Anonymisierungsspezifikation fehlgeschlagen'],
    ['Redaction timed out', '脱敏超时', '脫敏逾時', '匿名化がタイムアウト', 'Zeitüberschreitung bei der Anonymisierung'],
    ['Redaction unavailable', '脱敏服务不可用', '脫敏服務不可用', '匿名化サービスを利用できません', 'Anonymisierung nicht verfügbar'],
  ]) for (const [language, value] of [['zh', zh], ['zh-TW', tw], ['ja', ja], ['de', de]]) (I18N[language] ||= {})[key] = value;

  const failureLabels = {
    redaction_failed: 'Redaction failed',
    redaction_canceled: 'Redaction canceled',
    redaction_closed: 'Redaction runtime closed',
    redaction_extraction_failed: 'Redaction extraction failed',
    redaction_input_limit: 'Redaction input exceeds limit',
    redaction_invalid_input: 'Invalid redaction input',
    redaction_invalid_response: 'Redaction returned invalid response',
    redaction_invalid_result: 'Redaction result failed validation',
    redaction_entity_conflict: 'Redaction entity conflict',
    redaction_mapping_limit: 'Redaction mapping limit exceeded',
    redaction_placeholder_limit: 'Redaction placeholder limit exceeded',
    redaction_released: 'Redaction session released',
    redaction_response_limit: 'Redaction response exceeds limit',
    redaction_specification_failed: 'Redaction specification failed',
    redaction_timeout: 'Redaction timed out',
    redaction_unavailable: 'Redaction unavailable',
  };

  let data = null, error = '', working = false, generation = 0;
  const previousOpen = window.routerAppsOpen, previousRender = window.routerAppsRender, previousDisconnect = window.routerAppsDisconnected;
  const previousLocalRender = window.routerLocalRender, previousLocalDisconnect = window.routerLocalDisconnected;
  function experimental() { return data?.capability === 'experimental' && data.experimental === true && data.recognitionReady === false && data.formalQualityAccepted === false && data.budgetMs === 30000; }
  function canEnable() { return !!managementToken && !working && data?.modelReady === true && (data.capability === 'available' && data.recognitionReady === true || experimental() && data.productionIntegrated === true && data.trialReady === true); }
  function render() {
    const enabled = !!managementToken && (!!data?.intents?.some(p => p.enabled) || data?.cloudRedactionActive === true || Number(data?.statistics?.processed || 0) > 0 || Number(data?.statistics?.blocked || 0) > 0);
    $('#redactionPanel').hidden = !enabled;
    $('#redactionSetup').hidden = !managementToken || !data || enabled;
    $('#redactionSetup').textContent = t('Enable redaction in an application Profile to view its saved intent here.');
    $('#redactionStatus').hidden = !managementToken || !error;
    $('#redactionTitle').textContent = t('Redaction intent');
    $('#redactionScope').textContent = t('Saved intent is preserved when the local model is disabled. Local execution does not use redaction.');
    const box = $('#redactionContent'); box.replaceChildren();
    const disable = $('#redactionDisableAll'); disable.textContent = t('Disable all redaction intents');
    disable.disabled = working || !managementToken || !data?.intents?.some(p => p.enabled);
    if (!managementToken || !data) {
      $('#redactionStatus').textContent = t(error || (managementToken ? 'Redaction management unavailable. Refresh and check configuration.' : 'Management token required'));
      if (window.routerRedactionApplyProfileControl) window.routerRedactionApplyProfileControl();
      return;
    }
    for (const [label, value] of [['Production pipeline connected', data.productionIntegrated ? 'yes' : 'no'], ['Model and dependencies ready', data.modelReady ? 'yes' : 'no'], ['Recognition acceptance', experimental() ? 'unknown' : data.recognitionReady ? 'available' : (data.capability || 'unknown')], ['Cloud redaction active', data.cloudRedactionActive ? 'yes' : 'no']]) {
      const row = el('div', 'row pref'); row.append(el('div', 'who', t(label)), el('span', 'router-state', t(value))); box.append(row);
    }
    if (experimental()) for (const [label, value] of [['Experimental trial ready', data.trialReady ? 'yes' : 'no'], ['Formal quality accepted', 'no'], ['Total redaction budget', '30 seconds']]) {
      const row = el('div', 'row pref'); row.append(el('div', 'who', t(label)), el('span', 'router-state', t(value))); box.append(row);
    }
    for (const intent of data.intents) {
      const row = el('div', 'row pref'), who = el('div', 'who'); who.append(el('div', 'name', intent.id));
      const label = intent.active ? experimental() ? 'Experimental trial active' : 'Active' : data.productionIntegrated ? 'Inactive' : (intent.enabled ? 'Intent on · inactive' : 'Intent off · inactive');
      row.append(who, el('span', 'router-state', t(label))); box.append(row);
    }
    const noteKey = experimental() ? 'Experimental trial: formal quality is not accepted. Misses and false positives are possible. Invalid results still block cloud requests. The total redaction budget is 30 seconds.' : data.cloudRedactionActive ? 'Production redaction is active for eligible cloud requests.' : data.productionIntegrated ? 'Production redaction is connected but recognition acceptance is pending.' : 'Production redaction is unavailable. Recognition quality, latency and integration acceptance are pending.';
    const note = el('p', 'usage-note router-note', t(noteKey)); box.append(note);
    if (experimental()) box.append(el('p', 'usage-note router-note', t(canEnable() ? 'Select an application Profile to enable the experimental trial.' : 'The experimental trial is not ready. Existing intent is retained and new enabling is blocked.')));
    if (!data.statsAvailable) box.append(el('p', 'usage-note router-note', t('Redaction statistics are not connected.')));
    else {
      for (const [name, key] of [['Processed requests', 'processed'], ['Extracted entities', 'entities'], ['History cache hits', 'cacheHits'], ['Blocked requests', 'blocked'], ['Redaction duration (ms)', 'durationMs']]) {
        const row = el('div', 'row pref'); row.append(el('div', 'who', t(name)), el('span', 'router-state', Number(data.statistics?.[key] || 0).toLocaleString(intlLang()))); box.append(row);
      }
      const processed = Number(data.statistics?.processed || 0), cacheHits = Number(data.statistics?.cacheHits || 0), durationMs = Number(data.statistics?.durationMs || 0);
      const failures = Object.entries(data.statistics?.failures || {}).filter(([, count]) => Number(count) > 0);
      const metrics = el('dl', 'request-fields redaction-metrics');
      for (const [label, value] of [
        ['Session cache hit rate', processed > 0 ? (cacheHits / processed).toLocaleString(intlLang(), { style: 'percent', maximumFractionDigits: 1 }) : '—'],
        ['Average redaction duration (ms)', processed > 0 ? (durationMs / processed).toLocaleString(intlLang(), { maximumFractionDigits: 2 }) : '—'],
        ['Blocked reasons', failures.length ? failures.map(([code, count]) => `${t(failureLabels[code] || code)} (${code}): ${Number(count).toLocaleString(intlLang())}`).join(' · ') : t('None')],
      ]) metrics.append(el('dt', '', t(label)), el('dd', '', value));
      box.append(metrics);
    }
    $('#redactionStatus').textContent = error ? t(error) : '';
    if (window.routerRedactionApplyProfileControl) window.routerRedactionApplyProfileControl();
  }
  async function api(edits) {
    const controller = new AbortController(), timer = setTimeout(() => controller.abort(), 6000);
    try {
      const response = await managementFetch('/api/redaction', { method: edits ? 'POST' : 'GET', headers: { Authorization: 'Bearer ' + managementToken, ...(edits ? { 'Content-Type': 'application/json' } : {}) }, ...(edits ? { body: JSON.stringify({ edits }) } : {}), credentials: 'omit', cache: 'no-store', redirect: 'error', signal: controller.signal });
      if (response.status === 401) { managementToken = ''; serviceInfo = null; previousDisconnect?.(); renderService(); throw new Error('Management token required'); }
      if (response.status === 409) throw new Error('Profiles changed. Refresh before changing redaction intent.');
      if (!response.ok) throw new Error('Redaction management unavailable. Refresh and check configuration.');
      const value = await response.json();
      if (value.schemaVersion !== 1 || !Array.isArray(value.intents) || !['unknown', 'unavailable', 'available', 'experimental'].includes(value.capability) || typeof value.productionIntegrated !== 'boolean') throw new Error('Redaction management unavailable. Refresh and check configuration.');
      if (value.capability === 'experimental' && (value.experimental !== true || value.recognitionReady !== false || value.formalQualityAccepted !== false || value.budgetMs !== 30000 || typeof value.trialReady !== 'boolean' || typeof value.modelReady !== 'boolean')) throw new Error('Redaction management unavailable. Refresh and check configuration.');
      return value;
    } finally { clearTimeout(timer); }
  }
  async function load() {
    if (!['agents', 'local-model'].includes(view)) return;
    if (working) return;
    const request = ++generation;
    if (!managementToken) { data = null; render(); return; }
    try { const value = await api(); if (request === generation && managementToken) { data = value; error = ''; } }
    catch (failure) { if (request === generation) { data = null; error = ['Management token required', 'Profiles changed. Refresh before changing redaction intent.'].includes(failure.message) ? failure.message : 'Redaction management unavailable. Refresh and check configuration.'; } }
    if (request === generation) render();
  }
  $('#redactionDisableAll').onclick = async () => {
    if (working || !managementToken || !data) return;
    const request = ++generation, edits = data.intents.filter(p => p.enabled).map(p => ({ id: p.id, revision: p.revision, enabled: false }));
    if (!edits.length) return;
    working = true; error = ''; render();
    try { const value = await api(edits); if (request === generation && managementToken) { data = value; error = 'Redaction intents disabled.'; await previousOpen?.(); } }
    catch (failure) { if (request === generation) { data = null; error = ['Management token required', 'Profiles changed. Refresh before changing redaction intent.'].includes(failure.message) ? failure.message : 'Redaction management unavailable. Refresh and check configuration.'; } }
    finally { working = false; render(); }
  };
  window.routerAppsOpen = async () => { await Promise.all([previousOpen?.(), load()]); };
  window.routerAppsRender = () => { previousRender?.(); render(); };
  window.routerAppsDisconnected = () => { generation++; data = null; error = ''; previousDisconnect?.(); render(); };
  window.routerRedactionRefresh = load;
  window.routerLocalRender = () => { previousLocalRender?.(); render(); };
  window.routerLocalDisconnected = () => { generation++; data = null; error = ''; previousLocalDisconnect?.(); render(); };
  window.routerRedactionApplyProfileControl = () => {
    const input = $('#profileRedaction');
    if (!input) return;
    input.disabled = !canEnable() || $('#profileForm')?.classList.contains('busy');
    const hint = $('#profileRedactionHint');
    if (hint) hint.textContent = t(experimental() ? 'Experimental trial: formal quality is not accepted. Misses and false positives are possible. Invalid results still block cloud requests. The total redaction budget is 30 seconds.' : 'Unavailable; saved intent is not active. Local runtime is shared globally.');
  };
  render(); if (['agents', 'local-model'].includes(view)) load();
})();
