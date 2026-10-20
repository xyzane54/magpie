'use strict';
(() => {
  for (const [key, zh, tw, ja, de] of [
    ['Service management', '服务管理', '服務管理', 'サービス管理', 'Dienstverwaltung'],
    ['Process ID', '进程 ID', '程序 ID', 'プロセス ID', 'Prozess-ID'],
    ['Program location', '程序位置', '程式位置', 'プログラムの場所', 'Programmpfad'],
    ['Start at login', '登录时启动', '登入時啟動', 'ログイン時に起動', 'Bei Anmeldung starten'],
    ['Enable autostart', '开启自启', '開啟自動啟動', '自動起動を有効化', 'Autostart aktivieren'],
    ['Disable autostart', '关闭自启', '關閉自動啟動', '自動起動を無効化', 'Autostart deaktivieren'],
    ['Autostart is unavailable on this platform.', '此平台不支持登录自启。', '此平台不支援登入自動啟動。', 'この環境ではログイン時の起動を利用できません。', 'Autostart ist auf dieser Plattform nicht verfügbar.'],
    ['Autostart status is unknown. Refresh before changing it.', '自启状态未知，请刷新后再更改。', '自動啟動狀態未知，請重新整理後再變更。', '自動起動の状態が不明です。更新してから変更してください。', 'Autostart-Status unbekannt. Vor Änderungen aktualisieren.'],
    ['Experimental redaction at startup', '启动时实验脱敏', '啟動時實驗去識別化', '起動時の実験的匿名化', 'Experimentelle Anonymisierung beim Start'],
    ['Stop Router', '停止 Router', '停止 Router', 'Router を停止', 'Router stoppen'],
    ['Restart Router', '重启 Router', '重新啟動 Router', 'Router を再起動', 'Router neu starten'],
    ['Start from PowerShell', '从 PowerShell 启动', '從 PowerShell 啟動', 'PowerShell から起動', 'Aus PowerShell starten'],
    ['Keep this command before stopping. Run it in PowerShell to start Router again.', '停止前请保存此命令，在 PowerShell 中运行即可重新启动 Router。', '停止前請儲存此命令，在 PowerShell 中執行即可重新啟動 Router。', '停止前にこのコマンドを保存してください。PowerShell で実行すると Router を再起動できます。', 'Diesen Befehl vor dem Stoppen aufbewahren. In PowerShell ausführen, um Router erneut zu starten.'],
    ['I have saved the command and can open PowerShell independently.', '我已保存命令，并且可以独立打开 PowerShell。', '我已儲存命令，並且可以獨立開啟 PowerShell。', 'コマンドを保存し、PowerShell を別途開けることを確認しました。', 'Ich habe den Befehl gespeichert und kann PowerShell unabhängig öffnen.'],
    ['Stopping disconnects this page. Active requests will be drained or canceled. Host connections stay configured.', '停止后此页面将失联，正在处理的请求会排空或取消，宿主接入配置保留。', '停止後此頁面將中斷連線，處理中的請求會排空或取消，宿主接入設定保留。', '停止するとこのページは切断されます。処理中のリクエストは終了または取消され、ホスト設定は保持されます。', 'Beim Stoppen wird diese Seite getrennt. Aktive Anfragen werden abgeschlossen oder abgebrochen; Host-Einstellungen bleiben erhalten.'],
    ['Restarting disconnects this page temporarily. Save the start command in case recovery is needed.', '重启期间页面会暂时失联，请保存启动命令以便必要时恢复。', '重新啟動期間頁面會暫時中斷，請儲存啟動命令以便必要時復原。', '再起動中は一時的に切断されます。復旧に備えて起動コマンドを保存してください。', 'Beim Neustart wird die Seite kurz getrennt. Startbefehl für eine mögliche Wiederherstellung aufbewahren.'],
    ['Confirm stop', '确认停止', '確認停止', '停止を確認', 'Stoppen bestätigen'],
    ['Confirm restart', '确认重启', '確認重新啟動', '再起動を確認', 'Neustart bestätigen'],
    ['Service controls are unavailable. Refresh or use the installed CLI.', '服务控制不可用，请刷新或使用已安装的命令行程序。', '服務控制無法使用，請重新整理或使用已安裝的命令列程式。', 'サービス操作を利用できません。更新するか、インストール済み CLI を使用してください。', 'Dienststeuerung nicht verfügbar. Aktualisieren oder die installierte CLI verwenden.'],
    ['Service operation failed. Refresh to inspect the current state.', '服务操作失败，请刷新确认当前状态。', '服務操作失敗，請重新整理確認目前狀態。', 'サービス操作に失敗しました。更新して現在の状態を確認してください。', 'Dienstvorgang fehlgeschlagen. Aktuellen Status durch Aktualisieren prüfen.'],
    ['Autostart setting saved.', '自启设置已保存。', '自動啟動設定已儲存。', '自動起動の設定を保存しました。', 'Autostart-Einstellung gespeichert.'],
    ['Stop requested. Use the saved PowerShell command to start Router again.', '已请求停止。重新启动时请使用保存的 PowerShell 命令。', '已要求停止。重新啟動時請使用儲存的 PowerShell 命令。', '停止を要求しました。再起動には保存した PowerShell コマンドを使用してください。', 'Stoppen angefordert. Zum erneuten Starten den gespeicherten PowerShell-Befehl verwenden.'],
    ['Restart requested. Waiting for a new Router process…', '已请求重启，正在等待新的 Router 进程…', '已要求重新啟動，正在等候新的 Router 程序…', '再起動を要求しました。新しい Router プロセスを待っています…', 'Neustart angefordert. Auf neuen Router-Prozess wird gewartet…'],
    ['Router restarted. Management connection restored.', 'Router 已重启，管理连接已自动恢复。', 'Router 已重新啟動，管理連線已自動恢復。', 'Router が再起動し、管理接続を自動復元しました。', 'Router neu gestartet. Verwaltungsverbindung automatisch wiederhergestellt.'],
    ['Restart could not be confirmed. Use the saved PowerShell command, then refresh.', '尚未确认重启成功。请使用保存的 PowerShell 命令，然后刷新页面。', '尚未確認重新啟動成功。請使用儲存的 PowerShell 命令，再重新整理頁面。', '再起動を確認できません。保存した PowerShell コマンドを実行し、ページを更新してください。', 'Neustart nicht bestätigt. Gespeicherten PowerShell-Befehl ausführen und Seite aktualisieren.'],
  ]) for (const [lang, text] of [['zh', zh], ['zh-TW', tw], ['ja', ja], ['de', de]]) (I18N[lang] ||= {})[key] = text;

  let working = false, confirmation = '', acknowledged = false, command = '', message = '', phase = '', timer, generation = 0;
  const active = () => healthy && managementToken && serviceInfo?.running === true;
  function row(name, value, id) {
    const node = el('div', 'row pref'), who = el('div', 'who');
    who.append(el('div', 'name', t(name))); node.append(who);
    const text = el('code', 'router-service-value', value); if (id) text.id = id; node.append(text); return node;
  }
  function button(name, id, action, disabled = false) {
    const node = el('button', 'text', t(name)); node.type = 'button'; node.id = id;
    node.disabled = working || disabled; node.onclick = action; return node;
  }
  function render() {
    const root = $('#serviceControl'); root.replaceChildren();
    root.append(el('div', 'row-head router-label', t('Service management')));
    if (active() && phase === 'stop') { phase = ''; message = ''; }
    if (active() && typeof serviceInfo.startCommand === 'string') command = serviceInfo.startCommand;
    const list = el('div', 'list prefs');
    list.append(row('Service', t(active() ? 'Running' : 'Connection unavailable'), 'serviceControlState'));
    if (active()) {
      list.append(row('Process ID', String(serviceInfo.pid || '—'), 'serviceControlPID'), row('Program location', serviceInfo.executable || '—', 'serviceControlExecutable'));
      list.append(row('Experimental redaction at startup', t(serviceInfo.experimentalRedaction ? 'Enabled' : 'Disabled'), 'serviceControlTrial'));
      const autostart = serviceInfo.autostart;
      const known = autostart?.supported === true && !autostart.error && typeof autostart.enabled === 'boolean';
      const auto = row('Start at login', t(autostart?.supported === false ? 'Autostart is unavailable on this platform.' : known ? autostart.enabled ? 'Enabled' : 'Disabled' : 'Autostart status is unknown. Refresh before changing it.'), 'serviceControlAutostartState');
      auto.append(button(known && autostart.enabled ? 'Disable autostart' : 'Enable autostart', 'serviceControlAutostart', () => action('autostart', { enabled: !autostart.enabled }), !known)); list.append(auto);
    }
    root.append(list);
    if (command) {
      root.append(el('h3', 'router-label', t('Start from PowerShell')));
      const code = el('code', 'router-service-command', command); code.id = 'serviceStartCommand'; root.append(code);
      root.append(el('p', 'hint router-note', t('Keep this command before stopping. Run it in PowerShell to start Router again.')));
    }
    const actions = el('div', 'request-choices');
    for (const [name, actionName] of [['Stop Router', 'stop'], ['Restart Router', 'restart']]) {
      actions.append(button(name, 'serviceControl-' + actionName, () => { confirmation = actionName; acknowledged = false; render(); }, !active() || !command));
    }
    actions.append(button('Refresh now', 'serviceControlRefresh', () => refresh(), working)); root.append(actions);
    if (confirmation && active()) {
      const panel = el('div', 'router-service-confirm'); panel.id = 'serviceControlConfirm';
      panel.append(el('p', 'hint router-note', t(confirmation === 'stop' ? 'Stopping disconnects this page. Active requests will be drained or canceled. Host connections stay configured.' : 'Restarting disconnects this page temporarily. Save the start command in case recovery is needed.')));
      const label = el('label', 'router-service-ack'), check = el('input'); check.type = 'checkbox'; check.id = 'serviceControlAck'; check.checked = acknowledged;
      check.onchange = () => { acknowledged = check.checked; $('#serviceControlExecute').disabled = working || !acknowledged; };
      label.append(check, el('span', '', t('I have saved the command and can open PowerShell independently.'))); panel.append(label);
      const controls = el('div', 'request-choices');
      controls.append(button('Cancel', 'serviceControlCancel', () => { confirmation = ''; acknowledged = false; render(); }), button(confirmation === 'stop' ? 'Confirm stop' : 'Confirm restart', 'serviceControlExecute', () => action(confirmation), !acknowledged)); panel.append(controls); root.append(panel);
    }
    if (active() && !command) root.append(el('p', 'hint router-note', t('Service controls are unavailable. Refresh or use the installed CLI.')));
    const status = el('p', 'usage-note router-note', message ? t(message) : ''); status.id = 'serviceControlMessage'; status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite'); root.append(status);
  }
  async function request(path, method = 'GET', body, token = managementToken) {
    const controller = new AbortController(), timeout = setTimeout(() => controller.abort(), 3000);
    try {
      const send = path === '/healthz' ? fetch : managementFetch;
      return await send(path, { method, headers: { ...(token ? { Authorization: 'Bearer ' + token } : {}), ...(method === 'POST' && serviceInfo?.processStamp ? { 'X-API-Router-Instance': serviceInfo.processStamp } : {}), ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }), credentials: 'omit', cache: 'no-store', redirect: 'error', signal: controller.signal });
    } finally { clearTimeout(timeout); }
  }
  function clearPrivateState() {
    serviceInfo = null; managementToken = ''; healthy = false; renderService();
    window.routerAppsDisconnected?.(); window.routerProvidersDisconnected?.(); window.routerObservationsDisconnected?.(); window.routerLocalDisconnected?.();
  }
  async function awaitRestart(oldPID, attempt = 0, run = generation) {
    if (run !== generation) return;
    try {
      const response = await request('/healthz'); const data = await response.json();
      if (response.ok && data.service === 'api-router' && data.status === 'ok' && Number.isInteger(data.pid) && data.pid > 0 && data.pid !== oldPID) {
        await refreshService();
        if (serviceInfo?.pid !== data.pid) throw new Error('Restart not ready');
        working = false; phase = ''; message = 'Router restarted. Management connection restored.';
        window.routerProvidersConnected?.(); window.routerAppsOpen?.(); window.routerObservationsOpen?.(); window.routerLocalOpen?.();
        renderService(); render(); return;
      }
    } catch {}
    if (run !== generation) return;
    if (attempt >= 14) { working = false; phase = ''; message = 'Restart could not be confirmed. Use the saved PowerShell command, then refresh.'; render(); return; }
    timer = setTimeout(() => awaitRestart(oldPID, attempt + 1, run), 1000);
  }
  async function action(name, body) {
    if (working || !active() || (name !== 'autostart' && (!acknowledged || confirmation !== name || !command))) return;
    const oldPID = serviceInfo.pid; working = true; message = ''; render();
    try {
      const response = await request('/api/service/' + name, 'POST', body || {});
      const result = await response.json();
      if (!response.ok) {
        if (response.status === 401) { clearPrivateState(); throw new Error('Invalid management token. Please try again.'); }
        throw new Error('Service operation failed. Refresh to inspect the current state.');
      }
      if (name === 'autostart') {
        if (result.service !== 'api-router' || result.running !== true || result.autostart?.enabled !== body.enabled) throw new Error('Service operation failed. Refresh to inspect the current state.');
        serviceInfo = result; message = 'Autostart setting saved.';
      } else {
        if (result[name === 'stop' ? 'stopping' : 'restarting'] !== true) throw new Error('Service operation failed. Refresh to inspect the current state.');
        if (typeof result.startCommand === 'string' && result.startCommand) command = result.startCommand;
        confirmation = ''; acknowledged = false; phase = name;
        message = name === 'stop' ? 'Stop requested. Use the saved PowerShell command to start Router again.' : 'Restart requested. Waiting for a new Router process…';
        clearPrivateState();
        if (name === 'restart') { generation++; awaitRestart(oldPID); }
      }
    } catch (error) { message = error.message === 'Invalid management token. Please try again.' ? error.message : 'Service operation failed. Refresh to inspect the current state.'; }
    finally { if (phase !== 'restart') working = false; render(); }
  }
  window.routerServiceRender = render;
  window.routerServiceDisconnected = () => { confirmation = ''; acknowledged = false; render(); };
  window.addEventListener('pagehide', () => { generation++; clearTimeout(timer); });
  render();
})();
