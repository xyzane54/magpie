'use strict';
(() => {
  for (const [key, zh, tw, ja, de] of [
    ['Connect Loomy', '接入 Loomy', '接入 Loomy', 'Loomyを接続', 'Loomy verbinden'],
    ['Loomy local OpenCode only. Saving a stored-key Profile auto-connects the detected directory. Close Loomy before applying, then reopen it and select API Router. Project .opencode settings can override this directory.', '仅接入 Loomy 本机 OpenCode。保存存储凭证 Profile 会自动接入检测到的目录。手动接入前请退出 Loomy，完成后重启并选择 API Router。项目 .opencode 配置可能覆盖此目录。', '僅接入 Loomy 本機 OpenCode。儲存憑證 Profile 會自動接入偵測到的目錄。手動接入前請退出 Loomy，完成後重啟並選擇 API Router。專案 .opencode 設定可能覆寫此目錄。', 'LoomyのローカルOpenCodeのみです。保存済みキーのProfileで検出したディレクトリへ自動接続します。手動適用前にLoomyを終了し、再起動後API Routerを選択してください。プロジェクトの.opencode設定が優先する場合があります。', 'Nur lokales OpenCode von Loomy. Ein gespeichertes Profil verbindet automatisch das erkannte Verzeichnis. Loomy vor manueller Anwendung schließen, danach neu starten und API Router wählen. Projekt-.opencode kann diesen Ordner überschreiben.'],
    ['Preview writes nothing. Apply checks the selected target and creates a private first backup. Use this page for a custom directory or recovery.', '预览不写入。确认接入会检查选定目标并首次备份。自定义目录或恢复请使用本页。', '預覽不寫入。確認接入會檢查選定目標並首次備份。自訂目錄或還原請使用本頁。', 'プレビューは書き込みません。適用時に選択した実行先を検証し初回バックアップを作成します。別のディレクトリや復元にはこのページを使います。', 'Vorschau schreibt nichts. Anwenden prüft das gewählte Ziel und erstellt eine private erste Sicherung. Für ein eigenes Verzeichnis oder die Wiederherstellung diese Seite verwenden.'],
    ['Local OpenCode directory', '本机 OpenCode 配置目录', '本機 OpenCode 設定目錄', 'ローカルOpenCode設定フォルダー', 'Lokaler OpenCode-Konfigurationsordner'],
    ['Preview', '预览', '預覽', 'プレビュー', 'Vorschau'],
    ['Apply connection', '确认接入', '確認接入', '接続を適用', 'Verbindung anwenden'],
    ['Preview recovery', '预览恢复', '預覽復原', '復元を確認', 'Wiederherstellung prüfen'],
    ['Restore host settings', '恢复宿主配置', '復原宿主設定', 'ホスト設定を復元', 'Host-Einstellungen wiederherstellen'],
    ['Loomy local OpenCode only. Saving a stored-key Profile auto-connects the detected directory. Close Loomy before applying, then reopen it and select API Router. Project .opencode settings can override this directory.', '仅接入 Loomy 本机 OpenCode。请先保存存储凭证 Profile。接入或恢复前退出 Loomy，完成后重启并选择 API Router。项目 .opencode 配置可能覆盖此目录。', '僅接入 Loomy 本機 OpenCode。請先儲存憑證 Profile。接入或復原前退出 Loomy，完成後重啟並選擇 API Router。專案 .opencode 設定可能覆寫此目錄。', 'LoomyのローカルOpenCodeのみです。保存済みキーのProfileを先に保存してください。適用・復元前にLoomyを終了し、再起動後API Routerを選択してください。プロジェクトの.opencode設定が優先する場合があります。', 'Nur lokales OpenCode von Loomy. Zuerst das Profil mit gespeichertem Schlüssel speichern. Loomy vor Anwenden oder Wiederherstellen beenden, danach neu starten und API Router wählen. Projekt-.opencode kann diesen Ordner überschreiben.'],
    ['Preview writes nothing. Apply checks the selected target and creates a private first backup. Use this page for a custom directory or recovery.', '预览不写入。确认接入会检查已保存的目标并首次备份。打开本页、保存 Profile 与接入 Loomy 是独立操作。', '預覽不寫入。確認接入會檢查已儲存的目標並首次備份。開啟本頁、儲存 Profile 與接入 Loomy 是獨立操作。', 'プレビューは書き込みません。適用時に保存済みの実行先を検証し初回バックアップを作成します。ページを開くこと、Profileの保存、Loomy接続は別操作です。', 'Vorschau schreibt nichts. Anwenden prüft das gespeicherte Ziel und erstellt eine private erste Sicherung. Seite öffnen, Profil speichern und Loomy verbinden sind getrennte Vorgänge.'],
    ['Host operation failed. Check the directory, stored Profile, model and recovery record; preview again after resolving conflicts.', '操作未完成。请检查目录、存储凭证 Profile、模型与恢复记录；解决冲突后重新预览。', '操作未完成。請檢查目錄、儲存憑證 Profile、模型與復原記錄；解決衝突後重新預覽。', '操作できません。フォルダー、保存済みProfile、モデル、復元記録を確認し、競合解決後に再確認してください。', 'Vorgang fehlgeschlagen. Ordner, gespeichertes Profil, Modell und Wiederherstellungsprotokoll prüfen; Konflikte lösen und erneut prüfen.'],
    ['Completed', '已完成', '已完成', '完了', 'Abgeschlossen'],
    ['Restore bypasses Router. Keep this command and the private backup directory so recovery works with Router stopped.', '恢复后请求绕过 Router。请保留以下命令及私有备份目录，Router 停止后仍可恢复。', '復原後請求繞過 Router。請保留以下命令與私有備份目錄，Router 停止後仍可復原。', '復元後はRouterを経由しません。Router停止中も復元できるよう、このコマンドと非公開バックアップを保管してください。', 'Wiederherstellung umgeht Router. Befehl und privaten Sicherungsordner aufbewahren, um bei gestopptem Router wiederherzustellen.'],
    ['Session refresh and expiry are unverified. No credentials are imported and automatic host connection requires stored mode.', '会话凭证刷新与过期行为未验证。不导入凭证，自动接入要求使用存储凭证模式。', '工作階段憑證重新整理與到期行為未驗證。不匯入憑證，自動接入要求使用儲存憑證模式。', 'セッションの更新・失効動作は未検証です。認証情報は取り込まず、自動接続は保存済みキーモードを必要とします。', 'Sitzungsaktualisierung und Ablauf sind ungeprüft. Keine Zugangsdaten werden importiert; automatische Verbindung erfordert gespeicherten Modus.'],
  ]) { for (const [locale, value] of [['zh', zh], ['zh-TW', tw], ['ja', ja], ['de', de]]) I18N[locale][key] = value; }
  const translated = (tag, style, key) => { const node = el(tag, style, key); node.dataset.t = ''; return node; };
  const modal = el('div', 'modal'); modal.id = 'loomyModal'; modal.hidden = true; modal.setAttribute('role', 'dialog'); modal.setAttribute('aria-modal', 'true'); modal.setAttribute('aria-labelledby', 'loomyTitle');
  const dialog = el('div', 'dialog'), editor = el('div', 'editor framed provider-editor'), head = el('div', 'ehead'), title = translated('b', '', 'Connect Loomy'); title.id = 'loomyTitle';
  const close = el('button', 'text', '×'); close.id = 'loomyClose'; close.type = 'button'; close.setAttribute('aria-label', t('Close'));
  head.append(title, el('span', 'grow'), close); editor.append(head);
  const body = el('div', 'ebody');
  function field(id, label) { const name = translated('label', '', label); name.htmlFor = id; const input = el('input'); input.id = id; input.autocomplete = 'off'; body.append(name, input); return input; }
  const root = field('loomyRoot', 'Local OpenCode directory'), model = field('loomyModel', 'Model');
  root.placeholder = 'C:\\Users\\Public\\Loomy\\<user hash>\\opencode';
  for (const key of ['Loomy local OpenCode only. Saving a stored-key Profile auto-connects the detected directory. Close Loomy before applying, then reopen it and select API Router. Project .opencode settings can override this directory.', 'Preview writes nothing. Apply checks the selected target and creates a private first backup. Use this page for a custom directory or recovery.', 'Session refresh and expiry are unverified. No credentials are imported and automatic host connection requires stored mode.']) { const p = translated('p', 'hint', key); p.style.gridColumn = '1 / -1'; body.append(p); }
  editor.append(body);
  const status = el('div', 'editor-error'); status.id = 'loomyStatus'; status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
  const plan = el('pre', 'hint'); plan.id = 'loomyPlan'; const recovery = el('pre', 'hint'); recovery.id = 'loomyRecovery';
  for (const node of [plan, recovery]) { node.style.whiteSpace = 'pre-wrap'; node.style.overflowWrap = 'anywhere'; node.style.padding = '0 18px'; }
  editor.append(status, plan, recovery);
  const bar = el('div', 'bar'); bar.style.flexWrap = 'wrap';
  const actions = [['loomyCheck', 'Connection test', 'check'], ['loomyPreview', 'Preview', 'preview'], ['loomyApply', 'Apply connection', 'apply'], ['loomyRestorePreview', 'Preview recovery', 'restore-preview'], ['loomyRestore', 'Restore host settings', 'restore']];
  let digest = '', previewAction = '', busy = false, returnFocus;
  const buttons = [];
  function invalidate() { digest = ''; previewAction = ''; update(); }
  function update() { for (const [button, action] of buttons) button.disabled = busy || (action === 'apply' && (!digest || previewAction !== 'preview')) || (action === 'restore' && (!digest || previewAction !== 'restore-preview')); root.disabled = model.disabled = close.disabled = busy; }
  root.oninput = model.oninput = invalidate;
  const quote = value => "'" + value.replaceAll("'", "''") + "'";
  async function run(action) {
    if (busy || !managementToken) return;
    busy = true; update(); status.textContent = '';
    const controller = new AbortController(), timer = setTimeout(() => controller.abort(), 45000);
    try {
      const response = await managementFetch('/api/hosts/loomy', { method: 'POST', credentials: 'omit', redirect: 'error', signal: controller.signal, headers: { Authorization: 'Bearer ' + managementToken, 'Content-Type': 'application/json' }, body: JSON.stringify({action, configRoot: root.value, model: model.value, digest}) });
      const result = await response.json(); if (!response.ok) throw new Error();
      if (action === 'preview' || action === 'restore-preview') { digest = result.digest; previewAction = action; } else invalidate();
      plan.textContent = result.plan ? JSON.stringify(result.plan, null, 2) : '';
      if (result.stateDir) {
        const args = ' --host loomy --config-root ' + quote(root.value) + ' --state-dir ' + quote(result.stateDir);
        recovery.textContent = t('Restore bypasses Router. Keep this command and the private backup directory so recovery works with Router stopped.') + '\napi-router host restore-preview' + args + '\napi-router host restore' + args + ' --plan-digest ' + (action === 'restore-preview' ? quote(result.digest) : "'<digest from restore-preview>'");
      }
      status.textContent = t('Completed');
    } catch { invalidate(); status.textContent = t('Host operation failed. Check the directory, stored Profile, model and recovery record; preview again after resolving conflicts.'); }
    finally { clearTimeout(timer); busy = false; update(); }
  }
  for (const [id, label, action] of actions) { const button = translated('button', 'text', label); button.id = id; button.type = 'button'; button.onclick = () => run(action); buttons.push([button, action]); bar.append(button); }
  editor.append(bar); dialog.append(editor); modal.append(dialog); document.body.append(modal);
  function dismiss() { if (!busy) { modal.hidden = true; invalidate(); returnFocus?.focus({preventScroll:true}); } }
  close.onclick = dismiss; modal.onclick = event => { if (event.target === modal) dismiss(); };
  modal.onkeydown = event => { if (event.key === 'Escape') dismiss(); if (event.key === 'Tab') { const controls = [...modal.querySelectorAll('input,button')].filter(n => !n.disabled); const edge = event.shiftKey ? controls[0] : controls.at(-1); if (document.activeElement === edge) { event.preventDefault(); controls[event.shiftKey ? controls.length-1 : 0].focus({preventScroll:true}); } } };
  const connectLoomyBtn = $('#connectLoomy');
  if (connectLoomyBtn) connectLoomyBtn.onclick = async () => {
    if (!managementToken) { openManagement(); return; }
    returnFocus = document.activeElement; invalidate(); status.textContent = plan.textContent = recovery.textContent = '';
    close.setAttribute('aria-label', t('Close')); translateStatic();
    try { const response = await managementFetch('/api/apps', {headers:{Authorization:'Bearer '+managementToken},credentials:'omit',redirect:'error'}); if (!response.ok) return; const data = await response.json(); model.value = data.apps.find(a => a.id === 'loomy')?.cloud.model || ''; } catch { return; }
    modal.hidden = false; root.focus({preventScroll:true});
  };
  translateStatic(); update();
})();
