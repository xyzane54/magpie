'use strict';
(() => {
  const icons = { loomy: 'loomy.png', workbuddy: 'workbuddy-color.svg' };
  const defaultApps = () => [{ id: 'loomy', name: 'Loomy', supported: true, enabled: false, models: [], revision: 0, editSeq: 0 }, { id: 'workbuddy', name: 'WorkBuddy', supported: true, enabled: false, models: [], revision: 0, editSeq: 0 }];
  const apps = defaultApps(), saveQueues = { loomy: Promise.resolve(), workbuddy: Promise.resolve() }, savingCount = { loomy: 0, workbuddy: 0 }, expandedApps = new Set();
  let hosts = {}, routingGroups = [], cloudProviders = [], localModels = [], activePop = null;
  for (const [key, zh, tw, ja, de] of [
    ['Configured', '已配置', '已設定', '設定済み', 'Konfiguriert'], ['No models', '未选择模型', '未選擇模型', 'モデル未選択', 'Keine Modelle'],
    ['Choose models', '选择路由组或模型', '選擇路由組或模型', 'グループまたはモデルを選択', 'Gruppen oder Modelle wählen'],
    ['Routing groups', '路由组', '路由組', 'ルーティンググループ', 'Routing-Gruppen'], ['Local models', '本地模型', '本機模型', 'ローカルモデル', 'Lokale Modelle'],
    ['Custom models', '自定义模型', '自訂模型', 'カスタムモデル', 'Eigene Modelle'], ['Create in Routing', '前往路由页创建', '前往路由頁建立', 'ルーティングで作成', 'Unter Routing erstellen'],
    ['No routing groups yet.', '暂无可用路由组。', '暫無可用路由組。', 'ルーティンググループがありません。', 'Noch keine Routing-Gruppen.'],
    ['Filter models...', '搜索路由组或模型…', '搜尋路由組或模型…', 'グループやモデルを検索…', 'Modelle filtern…'],
    ['Show all', '全部展开', '全部展開', 'すべて展開', 'Alle zeigen'], ['Hide all', '全部收起', '全部收起', 'すべて折りたたむ', 'Alle ausblenden'],
    ['All', '全选', '全選', 'すべて', 'Alle'], ['Clear', '清空', '清空', 'クリア', 'Leeren'], ['Clear all', '清空已选', '清空已選', '選択をクリア', 'Auswahl leeren'],
    ['Add', '添加', '新增', '追加', 'Hinzufügen'], ['Primary', '主模型', '主模型', '主モデル', 'Primär'],
    ['Select at least one routing group or model before connecting.', '请先选择至少一个路由组或模型后再开启连接。', '請先選擇至少一個路由組或模型後再開啟連線。', '接続する前に少なくとも1つのグループまたはモデルを選択してください。', 'Bitte vor dem Verbinden mindestens eine Gruppe oder ein Modell auswählen.'],
    ['Host configuration directory was not found. Launch the host application first or use setup to specify its directory.', '未检测到宿主配置目录，请先启动宿主应用或通过配置向导指定目录。', '未偵測到宿主設定目錄，請先啟動宿主應用或透過設定精靈指定目錄。', 'ホスト設定ディレクトリが見つかりません。先にホストを起動するか設定でパスを指定してください。', 'Host-Konfigurationsverzeichnis nicht gefunden. Starten Sie zuerst die Host-App oder geben Sie den Pfad an.'],
    ['Disconnected and restored original host configuration.', '已断开连接并恢复宿主原始配置。', '已中斷連線並恢復宿主原始設定。', '接続を解除し、元のホスト設定を復元しました。', 'Verbindung getrennt und ursprüngliche Host-Konfiguration wiederhergestellt.'],
    ['Details', '详情', '詳情', '詳細', 'Details'], ['Collapse', '收起', '收起', '折りたたむ', 'Einklappen'],
    ['In {agent}', '在 {agent} 里', '在 {agent} 裡', '{agent} 内', 'In {agent}'],
    ['{agent} model menu only lists models selected here; you can switch them there. The list is read at startup; quit and reopen after changes.', '{agent} 的模型菜单只列出这里选中的模型；可在那里切换。列表在启动时读取，改动后需退出重开。', '{agent} 的模型選單只列出這裡選中的模型；可在那裡切換。列表在啟動時讀取，改動後需退出重開。', '{agent} のモデルメニューにはここで選択したモデルのみが表示されます。起動時に読み込まれるため、変更後は再起動が必要です。', 'Das Modellmenü von {agent} zeigt nur die hier ausgewählten Modelle. Die Liste wird beim Start gelesen; nach Änderungen neu starten.'],
    ['Model list', '模型列表', '模型列表', 'モデル一覧', 'Modellliste'], ['Pick', '挑选', '挑選', '選択', 'Auswählen'],
    ['New sessions', '新会话默认', '新工作階段預設', '新規セッション', 'Neue Sitzungen'],
    ['Disconnect & restore', '断开并还原', '中斷並還原', '切断して復元', 'Trennen & wiederherstellen'],
    ['Modified {path}', '改动了 {path}', '改動了 {path}', '{path} を変更', '{path} geändert'],
    ['local_first', '本地优先', '本機優先', 'ローカル優先', 'Lokal zuerst'], ['failover', '故障转移', '故障轉移', 'フェイルオーバー', 'Failover'],
    ['round_robin', '轮询', '輪詢', 'ラウンドロビン', 'Round-Robin'], ['weighted_round_robin', '加权轮询', '加權輪詢', '加重ラウンドロビン', 'Gewichtetes Round-Robin'],
  ]) for (const [lang, text] of [['zh', zh], ['zh-TW', tw], ['ja', ja], ['de', de]]) (I18N[lang] ||= {})[key] = text;

  const dedup = (list) => { const out = [], seen = new Set(); for (const raw of list || []) { const m = String(raw || '').trim(); if (m && !seen.has(m)) { seen.add(m); out.push(m); } } return out; };
  const appModels = (app) => Array.isArray(app?.models) && app.models.length ? dedup(app.models) : (app?.cloud?.providerId && app?.cloud?.model ? dedup([`${app.cloud.providerId}/${app.cloud.model}`]) : []);
  const shortModel = (id) => { const s = String(id || '').trim(), i = s.indexOf('/'); return i > 0 ? s.slice(i + 1) : s; };
  const menuSaid = (models) => !models.length ? t('No models') : (models.length <= 2 ? models.map(shortModel).join(', ') : `${models.slice(0, 2).map(shortModel).join(', ')} +${models.length - 2}`);
  const setStatus = (msg) => { const n = $('#appStatus'); if (n && msg !== undefined) n.textContent = msg; };
  const hostErrText = (data) => data?.error?.code === 'host_config_unavailable' ? t('Host configuration directory was not found. Launch the host application first or use setup to specify its directory.') : (data?.error?.message || t('Connection failed. Check that the service is running.'));
  const chevronRightSvg = '<svg viewBox="0 0 12 12" width="11" height="11"><path d="M4.5 2.5L8 6l-3.5 3.5" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>';
  const caretDownSvg = '<svg viewBox="0 0 12 12" width="11" height="11"><path d="M3 4.5L6 7.5l3-3" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>';

  function fillMenuButton(btn, app) {
    const models = appModels(app), v = el('span', 'v', menuSaid(models)), caret = el('span', 'caret');
    btn.classList.toggle('empty', !models.length); btn.title = models.length ? models.join('\n') : t('Choose models');
    caret.innerHTML = caretDownSvg;
    btn.replaceChildren(v, caret);
  }
  function fillPrimaryButton(btn, app) {
    const models = appModels(app), primary = models[0] ? shortModel(models[0]) : t('No models'), v = el('span', 'v', primary), caret = el('span', 'caret');
    btn.classList.toggle('empty', !models.length); btn.title = models[0] || t('Choose models');
    caret.innerHTML = caretDownSvg;
    btn.replaceChildren(v, caret);
  }
  function fillDetailButton(btn, open) {
    btn.replaceChildren(document.createTextNode(open ? t('Collapse') : t('Details') + ' '));
    if (!open) {
      const chv = el('span', 'chv'); chv.innerHTML = chevronRightSvg;
      btn.append(chv);
    }
  }
  function closeAgentModels() { if (!activePop) return; activePop.cleanup?.(); activePop.pop?.remove(); activePop.btn?.classList.remove('open'); activePop = null; }
  async function syncRevisions() {
    try { const res = await managementFetch('/api/apps'); if (!res?.ok) return; const data = await res.json(); for (const item of data.apps || []) { const target = apps.find((a) => a.id === item.id); if (target && typeof item.revision === 'number') target.revision = item.revision; } } catch {}
  }
  function enqueueSave(app) {
    const id = app.id, snapshot = [...appModels(app)];
    savingCount[id] = (savingCount[id] || 0) + 1;
    saveQueues[id] = (saveQueues[id] || Promise.resolve()).catch(() => {}).then(async () => {
      if (app.enabled) {
        if (!snapshot.length) {
          await managementFetch('/api/hosts/auto', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ host: id, enabled: false, action: 'disconnect' }) });
          app.enabled = false; await syncRevisions(); updateRowState(app); return;
        } else {
          const res = await managementFetch('/api/hosts/auto', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ host: id, enabled: true, models: snapshot }) });
          const data = await res.json().catch(() => ({}));
          if (!res.ok) { setStatus(`${app.name}: ${hostErrText(data)}`); return; }
          await syncRevisions(); setStatus(`${app.name}: ${t('Connected')} · ${snapshot[0]}`); updateRowState(app); return;
        }
      }
      for (let attempt = 0; attempt < 2; attempt++) {
        const body = { id, revision: Number(app.revision) || 0, name: app.name, enabled: false, models: snapshot, cloud: { providerId: '', model: '' }, credentialMode: app.credentialMode || 'stored', redactionEnabled: Boolean(app.redactionEnabled), referencePrice: app.referencePrice || null };
        const res = await managementFetch('/api/apps', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
        const data = await res.json().catch(() => ({}));
        if (res.ok && data.app) { app.revision = data.app.revision; updateRowState(app); return; }
        if (res.status === 409 && attempt === 0) { await syncRevisions(); continue; }
        if (data?.error?.message) setStatus(`${app.name}: ${data.error.message}`);
        return;
      }
    }).finally(() => { savingCount[id] = Math.max(0, (savingCount[id] || 1) - 1); });
    return saveQueues[id];
  }

  function buildCandidateSections(selected) {
    const sections = [], covered = new Set();
    const groups = (routingGroups || []).filter((g) => g?.id && g.enabled !== false).map((g) => {
      const fullId = `group/${g.id}`; covered.add(fullId);
      return { id: fullId, label: `${g.name || g.id} (${g.id})`, tag: t(g.strategy || 'failover') };
    });
    sections.push({ key: 'routing-groups', title: t('Routing groups'), items: groups, isRouting: true });
    for (const p of (cloudProviders || []).filter((x) => x?.id && x.enabled !== false)) {
      const raw = p.effectiveModels?.length ? p.effectiveModels : (p.discoveredModels?.length ? p.discoveredModels : (p.presetModels?.length ? p.presetModels : (p.models || [])));
      const extra = selected.filter((m) => m.startsWith(p.id + '/')).map((m) => m.slice(p.id.length + 1));
      const items = dedup([...raw, ...extra]).map((m) => {
        const fullId = m.startsWith(p.id + '/') ? m : `${p.id}/${m}`; covered.add(fullId);
        return { id: fullId, label: shortModel(fullId), tag: p.id };
      });
      if (items.length) sections.push({ key: `provider-${p.id}`, title: p.name || p.id, items });
    }
    const localRaw = (localModels || []).filter((m) => m && (m.id || m.fullModelId)).map((m) => ({ id: m.fullModelId || `local/${m.id}`, label: m.name || m.id || shortModel(m.fullModelId), tag: 'local' }));
    for (const m of selected.filter((x) => x.startsWith('local/'))) if (!localRaw.some((r) => r.id === m)) localRaw.push({ id: m, label: shortModel(m), tag: 'local' });
    for (const item of localRaw) covered.add(item.id);
    if (localRaw.length) sections.push({ key: 'local-models', title: t('Local models'), items: localRaw });
    const custom = selected.filter((m) => !covered.has(m)).map((m) => ({ id: m, label: m, tag: 'custom' }));
    if (custom.length) sections.push({ key: 'custom-models', title: t('Custom models'), items: custom });
    return sections;
  }

  function openAgentModels(app, btn) {
    if (activePop?.btn === btn) { closeAgentModels(); return; }
    closeAgentModels(); btn.classList.add('open');
    const pop = el('div', 'pop am-pop'); pop.setAttribute('role', 'dialog'); pop.dataset.app = app.id;
    const head = el('div', 'am-head'), titleWrap = el('div', 'am-title'), countBadge = el('span', 'am-count');
    titleWrap.append(el('b', '', app.name), countBadge); head.append(titleWrap);
    const tools = el('div', 'am-tools'), searchBox = el('label', 'am-search');
    searchBox.innerHTML = '<svg viewBox="0 0 16 16" width="12" height="12" aria-hidden="true"><circle cx="7" cy="7" r="4.8" fill="none" stroke="currentColor" stroke-width="1.5"/><path d="M10.6 10.6L14 14" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>';
    const searchInput = el('input'); searchInput.type = 'text'; searchInput.placeholder = t('Filter models...'); searchInput.autocomplete = 'off';
    const clearSearch = el('button', 'am-clear', '×'); clearSearch.type = 'button'; clearSearch.hidden = true;
    searchBox.append(searchInput, clearSearch);
    const seg = el('div', 'am-seg'), showAllBtn = el('button', 'on', t('Show all')), hideAllBtn = el('button', '', t('Hide all'));
    showAllBtn.type = 'button'; showAllBtn.dataset.action = 'show-all'; hideAllBtn.type = 'button'; hideAllBtn.dataset.action = 'hide-all';
    seg.append(showAllBtn, hideAllBtn); tools.append(searchBox, seg);
    const customRow = el('div', 'am-custom'), customInput = el('input', 'am-custom-input'), customAdd = el('button', 'text am-custom-add', t('Add'));
    customInput.type = 'text'; customInput.placeholder = 'group/id or provider/model'; customAdd.type = 'button';
    customRow.append(customInput, customAdd);
    const listEl = el('div', 'am-list'), foot = el('div', 'am-foot'), footNote = el('span', 'am-note'), resetBtn = el('button', 'text am-reset', t('Clear all'));
    resetBtn.type = 'button'; foot.append(footNote, resetBtn);
    pop.append(head, tools, customRow, listEl, foot); document.body.append(pop);

    const folded = new Set(); let query = '';
    const position = () => {
      const r = (activePop?.btn || btn).getBoundingClientRect(), w = Math.min(380, innerWidth - 24);
      pop.style.width = w + 'px'; pop.style.left = Math.max(12, Math.min(r.left, innerWidth - w - 12)) + 'px'; pop.style.top = Math.min(r.bottom + 6, Math.max(12, innerHeight - 420)) + 'px';
    };
    const commit = (nextModels) => { app.editSeq = (app.editSeq || 0) + 1; app.models = dedup(nextModels); updateRowState(app); paint(); enqueueSave(app); };
    const paint = () => {
      const sel = appModels(app), selSet = new Set(sel), sections = buildCandidateSections(sel), q = query.trim().toLowerCase();
      countBadge.textContent = String(sel.length);
      footNote.textContent = sel.length ? `${t('Primary')}: ${shortModel(sel[0])}` : t('No models');
      resetBtn.disabled = !sel.length; listEl.replaceChildren();
      for (const sec of sections) {
        const filtered = q ? sec.items.filter((it) => it.id.toLowerCase().includes(q) || it.label.toLowerCase().includes(q) || sec.title.toLowerCase().includes(q)) : sec.items;
        if (!filtered.length && (!sec.isRouting || q)) continue;
        const isFolded = !q && folded.has(sec.key), gEl = el('div', 'am-g' + (isFolded ? ' fold' : '')); gEl.dataset.section = sec.key;
        const gh = el('div', 'am-gh'), foldBtn = el('button', 'am-fold'); foldBtn.type = 'button';
        foldBtn.append(el('span', 'am-arr'), el('span', 'am-gn', sec.title), el('span', 'am-gc', `${sec.items.filter((it) => selSet.has(it.id)).length}/${sec.items.length}`));
        foldBtn.onclick = () => { if (folded.has(sec.key)) folded.delete(sec.key); else folded.add(sec.key); paint(); };
        gh.append(foldBtn);
        if (filtered.length) {
          const allSelected = filtered.every((it) => selSet.has(it.id)), allBtn = el('button', 'am-all', allSelected ? t('Clear') : t('All'));
          allBtn.type = 'button'; allBtn.onclick = () => commit(allSelected ? sel.filter((m) => !filtered.some((it) => it.id === m)) : [...sel, ...filtered.map((it) => it.id)]);
          gh.append(allBtn);
        }
        const rows = el('div', 'am-rows');
        if (!filtered.length && sec.isRouting) {
          const empty = el('div', 'am-empty', t('No routing groups yet.') + ' '), goBtn = el('button', 'text am-go-routing', t('Create in Routing'));
          goBtn.type = 'button'; goBtn.onclick = () => { closeAgentModels(); if (typeof show === 'function') show('routing'); };
          empty.append(goBtn); rows.append(empty);
        }
        for (const it of filtered) {
          const idx = sel.indexOf(it.id), on = idx >= 0;
          const mr = el('div', 'am-mr' + (on ? ' on' : '')); mr.dataset.model = it.id; mr.setAttribute('role', 'option'); mr.setAttribute('aria-selected', String(on));
          mr.append(el('span', 'ck', on ? '✓' : ''), el('span', 'am-mn', it.label));
          if (it.tag) mr.append(el('span', 'am-tag' + (idx === 0 ? ' is-primary' : ''), idx === 0 ? `#1 · ${it.tag}` : (on ? `#${idx + 1} · ${it.tag}` : it.tag)));
          if (on) {
            const ord = el('span', 'am-order'), up = el('button', 'am-mv', '↑'), down = el('button', 'am-mv', '↓');
            up.type = 'button'; up.dataset.move = 'up'; up.disabled = idx === 0;
            down.type = 'button'; down.dataset.move = 'down'; down.disabled = idx === sel.length - 1;
            up.onclick = (e) => { e.stopPropagation(); const next = [...sel]; [next[idx - 1], next[idx]] = [next[idx], next[idx - 1]]; commit(next); };
            down.onclick = (e) => { e.stopPropagation(); const next = [...sel]; [next[idx + 1], next[idx]] = [next[idx], next[idx + 1]]; commit(next); };
            ord.append(up, down); mr.append(ord);
          }
          mr.onclick = () => commit(on ? sel.filter((m) => m !== it.id) : [...sel, it.id]);
          rows.append(mr);
        }
        gEl.append(gh, rows); listEl.append(gEl);
      }
    };
    searchInput.oninput = () => { query = searchInput.value; clearSearch.hidden = !query; paint(); };
    clearSearch.onclick = () => { query = ''; searchInput.value = ''; clearSearch.hidden = true; paint(); };
    showAllBtn.onclick = () => { folded.clear(); showAllBtn.classList.add('on'); hideAllBtn.classList.remove('on'); paint(); };
    hideAllBtn.onclick = () => { for (const s of buildCandidateSections(appModels(app))) folded.add(s.key); hideAllBtn.classList.add('on'); showAllBtn.classList.remove('on'); paint(); };
    const addCustom = () => { const v = customInput.value.trim(); if (!v || !v.includes('/')) return; customInput.value = ''; commit([...appModels(app), v]); };
    customAdd.onclick = addCustom; customInput.onkeydown = (e) => { if (e.key === 'Enter') { e.preventDefault(); addCustom(); } };
    resetBtn.onclick = () => commit([]);
    const onDocDown = (e) => { if (!pop.contains(e.target) && !(activePop?.btn || btn).contains(e.target)) closeAgentModels(); };
    const onKey = (e) => { if (e.key === 'Escape') closeAgentModels(); };
    document.addEventListener('pointerdown', onDocDown, true); document.addEventListener('keydown', onKey); window.addEventListener('resize', position);
    activePop = { pop, btn, appId: app.id, paint, cleanup: () => { document.removeEventListener('pointerdown', onDocDown, true); document.removeEventListener('keydown', onKey); window.removeEventListener('resize', position); } };
    const sig = () => JSON.stringify([routingGroups, cloudProviders, localModels]), beforeSig = sig();
    position(); paint();
    refreshCandidates().then(() => { if (activePop?.pop === pop && sig() !== beforeSig) paint(); });
  }

  async function toggleHostSwitch(app, sw, menuBtn) {
    const wantOn = !app.enabled, models = appModels(app);
    if (wantOn && !models.length) { setStatus(`${app.name}: ${t('Select at least one routing group or model before connecting.')}`); openAgentModels(app, menuBtn); return; }
    app.editSeq = (app.editSeq || 0) + 1; if (sw) sw.disabled = true;
    try {
      await (saveQueues[app.id] || Promise.resolve()).catch(() => {});
      const payload = wantOn ? { host: app.id, enabled: true, models } : { host: app.id, enabled: false, action: 'disconnect' };
      const res = await managementFetch('/api/hosts/auto', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) { app.enabled = !wantOn; setStatus(`${app.name}: ${hostErrText(data)}`); updateRowState(app); return; }
      app.enabled = wantOn;
      if (data?.host) hosts[app.id] = data.host;
      const rm = data?.models || data?.host?.models;
      if (wantOn && Array.isArray(rm) && rm.length) app.models = dedup(rm);
      await syncRevisions();
      setStatus(wantOn ? `${app.name}: ${t('Connected')} · ${appModels(app)[0] || ''}` : `${app.name}: ${t('Disconnected and restored original host configuration.')}`);
      updateRowState(app);
    } finally { if (sw) sw.disabled = false; }
  }

  const stateLabel = (isOn, models) => isOn ? (models[0] ? `${t('Connected')} · ${models[0]}` : t('Connected')) : (models.length ? `${t('Configured')} · ${models[0]}` : t('Not connected yet'));

  function buildExpandPanel(app, sw, menuBtn) {
    const exp = el('div', 'ag-exp'), grid = el('div', 'ag-settings'), models = appModels(app), selSet = new Set(models);
    exp.onclick = (e) => e.stopPropagation();
    grid.append(
      el('span', 'ag-k', t('In {agent}').replace('{agent}', app.name)),
      el('div', 'ag-v ag-note', t('{agent} model menu only lists models selected here; you can switch them there. The list is read at startup; quit and reopen after changes.').replace('{agent}', app.name))
    );
    const listVal = el('div', 'ag-v'), chips = el('div', 'ag-chips');
    for (const sec of buildCandidateSections(models)) {
      const n = sec.items.filter((it) => selSet.has(it.id)).length;
      if (!n) continue;
      const c = el('span', 'ag-chip', sec.title + ' '); c.append(el('span', 'n', String(n))); chips.append(c);
    }
    if (!chips.childElementCount) chips.append(el('span', 'ag-empty', t('No models')));
    const pickBtn = el('button', 'ag-quiet', t('Pick') + ' '); pickBtn.type = 'button';
    const chv = el('span', 'chv'); chv.innerHTML = chevronRightSvg; pickBtn.append(chv);
    pickBtn.onclick = (e) => { e.stopPropagation(); openAgentModels(app, pickBtn); };
    listVal.append(chips, pickBtn);
    grid.append(el('span', 'ag-k', t('Model list')), listVal);

    const defVal = el('div', 'ag-v'), startBtn = el('button', 'field ag-pick'); startBtn.type = 'button';
    fillPrimaryButton(startBtn, app);
    startBtn.onclick = (e) => { e.stopPropagation(); openAgentModels(app, startBtn); };
    defVal.append(startBtn);
    grid.append(el('span', 'ag-k', t('New sessions')), defVal);

    const foot = el('div', 'ag-foot'), h = hosts[app.id] || {}, cfgPath = h.configPath || h.configRoot || h.path || '';
    const footText = app.enabled && cfgPath ? t('Modified {path}').replace('{path}', cfgPath) : (cfgPath || stateLabel(Boolean(app.enabled), models));
    foot.append(el('span', 'mono', footText));
    if (app.enabled) {
      const disc = el('button', 'ag-quiet red', t('Disconnect & restore')); disc.type = 'button';
      disc.onclick = (e) => { e.stopPropagation(); toggleHostSwitch(app, sw, menuBtn); };
      foot.append(disc);
    }
    exp.append(grid, foot);
    return exp;
  }

  function updateRowState(app) {
    const row = $(`#agents .row[data-app="${app.id}"]`); if (!row) return;
    const isOn = Boolean(app.enabled), open = expandedApps.has(app.id), models = appModels(app), state = row.querySelector('.router-state'), sw = row.querySelector('.lib-switch.ag-conn');
    row.classList.toggle('off', !isOn);
    row.classList.toggle('open', open);
    if (state) { state.className = 'router-state' + (isOn ? ' ok' : ''); state.textContent = stateLabel(isOn, models); }
    const db = row.querySelector('.ag-link'); if (db) fillDetailButton(db, open);
    const mb = row.querySelector('.field.ag-menu'); if (mb) fillMenuButton(mb, app);
    if (sw) { sw.classList.toggle('on', isOn); sw.setAttribute('aria-checked', String(isOn)); }
    const oldExp = row.querySelector('.ag-exp');
    if (open) {
      const nextExp = buildExpandPanel(app, sw, mb);
      if (oldExp) oldExp.replaceWith(nextExp); else row.append(nextExp);
    } else if (oldExp) {
      oldExp.remove();
    }
  }

  function renderRows() {
    const container = $('#agents'); if (!container) return;
    if (apps.every((app) => container.querySelector(`.row.agent.conn[data-app="${app.id}"] .field.ag-menu`))) { for (const app of apps) updateRowState(app); return; }
    container.replaceChildren(...apps.map((app) => {
      const isOn = Boolean(app.enabled), open = expandedApps.has(app.id), models = appModels(app);
      const row = el('div', 'row agent conn' + (isOn ? '' : ' off') + (open ? ' open' : '')); row.dataset.app = app.id;
      const ic = el('span', 'ic'), img = el('img'); img.src = '/icons/' + (icons[app.id] || 'loomy.png'); img.alt = ''; ic.append(img);
      const who = el('div', 'who'), sub = el('div', 'sub'), state = el('span', 'router-state' + (isOn ? ' ok' : ''), stateLabel(isOn, models));
      sub.append(state); who.append(el('div', 'name', app.name), sub);
      const fields = el('div', 'fields'), detailBtn = el('button', 'ag-link'), menuBtn = el('button', 'field solo ag-menu');
      detailBtn.type = 'button'; fillDetailButton(detailBtn, open);
      const toggleExpand = (e) => { e?.stopPropagation(); if (expandedApps.has(app.id)) expandedApps.delete(app.id); else expandedApps.add(app.id); updateRowState(app); };
      detailBtn.onclick = toggleExpand;
      menuBtn.type = 'button'; fillMenuButton(menuBtn, app);
      menuBtn.onclick = (e) => { e.stopPropagation(); openAgentModels(app, menuBtn); };
      fields.append(detailBtn, menuBtn);
      const actions = el('div', 'ag-actions');
      const sw = el('button', 'lib-switch ag-conn' + (isOn ? ' on' : '')); sw.type = 'button'; sw.setAttribute('role', 'switch'); sw.setAttribute('aria-checked', String(isOn));
      sw.append(el('i')); sw.onclick = (e) => { e.stopPropagation(); toggleHostSwitch(app, sw, menuBtn); };
      actions.append(sw);
      row.onclick = (e) => { if (e.target.closest('button, input, a, .ag-exp')) return; toggleExpand(e); };
      row.append(ic, who, fields, actions);
      if (open) row.append(buildExpandPanel(app, sw, menuBtn));
      return row;
    }));
  }

  async function refreshCandidates() {
    const [rgRes, cpRes] = await Promise.all([managementFetch('/api/routing-groups').catch(() => null), managementFetch('/api/cloud-providers').catch(() => null)]);
    if (rgRes?.ok) { const d = await rgRes.json().catch(() => ({})); routingGroups = Array.isArray(d.groups) ? d.groups : []; }
    if (cpRes?.ok) { const d = await cpRes.json().catch(() => ({})); cloudProviders = Array.isArray(d.providers) ? d.providers : []; localModels = Array.isArray(d.localModels) ? d.localModels : []; }
  }

  async function load() {
    if (view !== 'agents') return;
    const seqBefore = { loomy: apps[0].editSeq || 0, workbuddy: apps[1].editSeq || 0 };
    const [appRes, hostRes] = await Promise.all([managementFetch('/api/apps').catch(() => null), managementFetch('/api/hosts/auto').catch(() => null), refreshCandidates()]);
    const appData = appRes?.ok ? await appRes.json().catch(() => ({})) : {}, hostData = hostRes?.ok ? await hostRes.json().catch(() => ({})) : {};
    hosts = {}; for (const h of (hostData.items || hostData.hosts || [])) if (h?.host) hosts[h.host] = h;
    const byId = new Map((appData.apps || []).map((a) => [a.id, a]));
    for (const target of apps) {
      const a = byId.get(target.id); if (typeof a?.revision === 'number') target.revision = a.revision;
      if ((target.editSeq || 0) !== seqBefore[target.id] || savingCount[target.id] > 0) continue;
      const h = hosts[target.id], connected = Boolean(h ? (h.connected || h.enabled) : a?.enabled);
      const models = dedup(connected && h?.models?.length ? h.models : (Array.isArray(a?.models) && a.models.length ? a.models : (h?.models?.length ? h.models : appModels(a || target))));
      Object.assign(target, a || {}, { enabled: connected, models: models.length ? models : target.models });
    }
    renderRows(); if (activePop) activePop.paint();
  }

  window.routerAppsOpen = load; window.routerAppsRender = renderRows;
  window.routerAppsDisconnected = () => { closeAgentModels(); for (const [i, def] of defaultApps().entries()) Object.assign(apps[i], def); renderRows(); };
  renderRows();
})();
