'use strict';
(() => {
  for (const [key, zh, tw, ja, de] of [
    ['Routing decision', '判断结果', '判斷結果', 'ルーティング判断', 'Routing-Entscheidung'], ['Cloud routing reason', '转云原因', '轉雲原因', 'クラウド実行の理由', 'Grund für Cloud-Routing'], ['Judgment status', '判断调用状态', '判斷呼叫狀態', '判断の呼出状態', 'Status des Beurteilungsaufrufs'], ['Execution status', '执行调用状态', '執行呼叫狀態', '実行の呼出状態', 'Status des Ausführungsaufrufs'], ['Judgment duration (ms)', '判断耗时（毫秒）', '判斷耗時（毫秒）', '判断時間（ms）', 'Beurteilungsdauer (ms)'], ['Not observed', '未观测到调用', '未觀測到呼叫', '呼び出しの記録なし', 'Kein Aufruf beobachtet'], ['Failure reason', '失败原因', '失敗原因', '失敗の理由', 'Fehlergrund'], ['Local routing is unavailable', '本地判断暂不可用', '本機判斷暫時無法使用', 'ローカル判断を利用できません', 'Lokales Routing ist nicht verfügbar'],
    ['Judgment queue timed out', '判断排队超时', '判斷排隊逾時', '判断の待機がタイムアウト', 'Wartezeit für Beurteilung überschritten'], ['Judgment inference timed out', '判断推理超时', '判斷推理逾時', '判断の推論がタイムアウト', 'Zeit für Beurteilungsinferenz überschritten'], ['Judgment budget exhausted', '判断总预算耗尽', '判斷總預算耗盡', '判断の時間予算を消費', 'Beurteilungsbudget erschöpft'], ['Judgment failed', '本地判断失败', '本機判斷失敗', 'ローカル判断に失敗', 'Lokale Beurteilung fehlgeschlagen'], ['Judgment input exceeds its limit', '判断输入超过上限', '判斷輸入超過上限', '判断入力が上限を超過', 'Beurteilungseingabe überschreitet das Limit'], ['Request cannot be summarized safely', '请求限制无法完整压缩，已转云', '請求限制無法完整壓縮，已轉雲', '要求の制約を完全に要約できないためクラウドへ切替', 'Anfragebeschränkungen sind nicht vollständig zusammenfassbar; Cloud gewählt'], ['Context window exceeded', '上下文窗口不足', '上下文視窗不足', 'コンテキスト長の上限を超過', 'Kontextfenster überschritten'], ['Local token count is unavailable', '无法确认本地 Token 数', '無法確認本機 Token 數', 'ローカルトークン数を確認できません', 'Lokale Tokenzahl ist nicht verfügbar'],
    ['Local service returned an HTTP error', '本地服务返回 HTTP 错误', '本機服務傳回 HTTP 錯誤', 'ローカルサービスのHTTPエラー', 'Lokaler Dienst meldete einen HTTP-Fehler'], ['Execution queue timed out', '执行排队超时', '執行排隊逾時', '実行の待機がタイムアウト', 'Wartezeit für Ausführung überschritten'], ['Local execution timed out', '本地执行超时', '本機執行逾時', 'ローカル実行がタイムアウト', 'Zeit für lokale Ausführung überschritten'], ['Local execution is unavailable', '本地执行暂不可用', '本機執行暫時無法使用', 'ローカル実行を利用できません', 'Lokale Ausführung ist nicht verfügbar'], ['Local execution failed', '本地执行失败', '本機執行失敗', 'ローカル実行に失敗', 'Lokale Ausführung fehlgeschlagen'], ['Local output protocol is unsupported', '本地不支持本次输出协议', '本機不支援本次輸出協定', 'ローカル出力プロトコルに非対応', 'Lokales Ausgabeprotokoll wird nicht unterstützt'], ['Judgment returned an invalid decision', '判断结果格式无效', '判斷結果格式無效', '判断結果の形式が無効', 'Beurteilung lieferte eine ungültige Entscheidung'], ['Time reserved for cloud fallback', '本地剩余预算不足，已预留时间转云', '本機剩餘預算不足，已預留時間轉雲', 'ローカル予算が不足し、クラウド切替時間を確保', 'Lokales Zeitbudget reicht nicht; Zeit für Cloud-Fallback reserviert'],
    ['Whole request budget exhausted', '整条请求预算耗尽', '整筆請求預算耗盡', 'リクエスト全体の時間予算を消費', 'Gesamtbudget der Anfrage erschöpft'], ['Judgment selected cloud', '判断选择云端', '判斷選擇雲端', '判断がクラウドを選択', 'Beurteilung wählte Cloud'], ['Local settings fingerprint', '本地设置指纹', '本機設定指紋', 'ローカル設定の識別子', 'Kennung der lokalen Einstellungen'], ['Local context window', '本地上下文窗口', '本機上下文視窗', 'ローカルコンテキスト長', 'Lokales Kontextfenster'], ['Output token reserve', '输出预留 Token', '輸出預留 Token', '出力トークンの確保量', 'Reserve für Ausgabetokens'], ['Runtime settings changed during the request', '请求期间运行设置已变更，已转云', '請求期間執行設定已變更，已轉雲', 'リクエスト中に実行設定が変わり、クラウドへ切替', 'Laufzeiteinstellungen während der Anfrage geändert; Cloud gewählt'], ['Yes', '是', '是', 'はい', 'Ja'], ['No', '否', '否', 'いいえ', 'Nein'],
    ['Preflight', '前置检查', '前置檢查', '事前検査', 'Vorprüfung'], ['Configuration', '配置', '設定', '設定', 'Konfiguration'], ['Validation', '输入校验', '輸入驗證', '入力検証', 'Eingabeprüfung'], ['Cloud execution', '云端执行', '雲端執行', 'クラウド実行', 'Cloud-Ausführung'], ['Retry waiting', '重试等待', '重試等待', '再試行待機', 'Warten auf Wiederholung'], ['Response output', '响应输出', '回應輸出', 'レスポンス出力', 'Antwortausgabe'], ['Retained requests', '保留的请求', '保留的請求', '保存済みリクエスト', 'Gespeicherte Anfragen'], ['Local', '本地', '本機', 'ローカル', 'Lokal'],
    ['Only the final reported response usage is counted once. Usage of failed or retried attempts may be unknown; these totals are not billed usage across all attempts.', '最终响应报告的用量仅计入一次。失败或重试尝试的用量可能未知，汇总并非所有尝试的计费用量。', '最終回應回報的用量僅計入一次。失敗或重試嘗試的用量可能未知，彙總並非所有嘗試的計費用量。', '最終レスポンスの報告使用量を一度だけ集計します。失敗・再試行分は不明な場合があり、全試行の請求使用量ではありません。', 'Nur die gemeldete Nutzung der letzten Antwort wird einmal gezählt. Nutzung fehlgeschlagener oder wiederholter Versuche kann unbekannt sein; diese Summen sind keine Abrechnung aller Versuche.'], ['Generation requests only. Totals cover retained records, not all-time usage. Auxiliary operations are excluded.', '仅统计生成请求。汇总覆盖保留记录，并非历史全部用量，不包含辅助操作。', '僅統計生成請求。彙總涵蓋保留紀錄，並非歷來全部用量，不包含輔助操作。', '生成リクエストのみ。集計は保存済み記録が対象で、全期間の使用量や補助操作は含みません。', 'Nur Generierungsanfragen. Summen gelten für gespeicherte Datensätze, nicht für alle Zeiten. Hilfsoperationen sind ausgeschlossen.'],
    ['Actual reported usage. Missing fields remain unknown; explicit zero stays zero.', '实际报告用量。缺失字段保持未知，明确的零保留为零。', '實際回報用量。缺失欄位保持未知，明確的零保留為零。', '実際の報告使用量です。欠落項目は不明、明示されたゼロはゼロです。', 'Tatsächlich gemeldete Nutzung. Fehlende Werte bleiben unbekannt; ausdrücklich gemeldete Null bleibt Null.'], ['Final local and cloud destinations. One business request is counted once; retries and internal calls do not add requests.', '展示最终本地或云端去向。每次业务请求仅计一次，重试和内部调用不增加请求数。', '顯示最終本機或雲端去向。每次業務請求僅計一次，重試和內部呼叫不增加請求數。', '最終的なローカル・クラウド実行先です。業務リクエストは一度だけ数え、再試行・内部呼び出しは追加しません。', 'Endgültige lokale und Cloud-Ziele. Jede Geschäftsanfrage zählt einmal; Wiederholungen und interne Aufrufe erhöhen die Anfragezahl nicht.'],
    ['Destination breakdown', '执行去向分组', '執行去向分組', '実行先の内訳', 'Aufschlüsselung nach Ziel'], ['Share of retained generation requests', '占保留生成请求的比例', '占保留生成請求的比例', '保存済み生成リクエストに占める割合', 'Anteil gespeicherter Generierungsanfragen'], ['Estimated cloud savings', '预计云端节省', '預計雲端節省', '推定クラウド削減量', 'Geschätzte Cloud-Einsparung'], ['Eligible local successes', '符合估算条件的本地成功请求', '符合估算條件的本機成功請求', '推計対象のローカル成功数', 'Berücksichtigte lokale Erfolge'], ['Estimated avoided cloud tokens', '估算避免的云端 Token', '估算避免的雲端 Token', '回避したクラウドトークンの推計', 'Geschätzte vermiedene Cloud-Tokens'], ['Estimated savings by currency', '按币种估算节省金额', '按幣別估算節省金額', '通貨別の推定削減額', 'Geschätzte Einsparung nach Währung'], ['Unknown reference price requests', '参考价格未知的请求', '參考價格未知的請求', '参考価格が不明なリクエスト', 'Anfragen mit unbekanntem Referenzpreis'],
    ['The denominator includes all retained business generation requests, including failures and no-dispatch requests. Diagnostics and token counting are excluded.', '分母为保留区间内全部业务生成请求，包含失败及未派发请求。诊断和 Token 计数不计入。', '分母為保留區間內全部業務生成請求，包含失敗及未派發請求。診斷和 Token 計數不計入。', '分母は保存期間内の全業務生成リクエストで、失敗・未送信も含みます。診断・トークン計数は除外します。', 'Der Nenner umfasst alle gespeicherten Geschäftsgenerierungen einschließlich Fehlern und nicht gesendeter Anfragen. Diagnosen und Tokenzählung sind ausgeschlossen.'], ['Only successful local business generations qualify. Input plus output tokens estimate avoided cloud tokens; tokenizers may differ. Amounts use request-time Profile prices per million tokens, grouped by currency. Estimates are not bills or net savings.', '仅成功的本地业务生成请求参与估算。输入加输出 Token 估算避免的云端 Token，不同模型的分词器可能不同。金额采用请求时 Profile 每百万 Token 参考价，按币种分别汇总。估算不是账单或净节省。', '僅成功的本機業務生成請求參與估算。輸入加輸出 Token 估算避免的雲端 Token，不同模型的分詞器可能不同。金額採用請求時 Profile 每百萬 Token 參考價，按幣別分別彙總。估算不是帳單或淨節省。', '成功したローカル業務生成のみが対象です。入力＋出力トークンで回避量を推計しますが、トークナイザーは異なる場合があります。金額はリクエスト時のProfile参考価格（百万トークン当たり）を通貨別に集計します。請求額や純削減額ではありません。', 'Nur erfolgreiche lokale Geschäftsgenerierungen zählen. Eingabe plus Ausgabe schätzt vermiedene Cloud-Tokens; Tokenizer können abweichen. Beträge nutzen die damaligen Profile-Preise je Million Tokens, getrennt nach Währung. Schätzungen sind keine Rechnungen oder Nettoeinsparungen.'],
    ['Internal model calls', '内部模型调用', '內部模型呼叫', '内部モデル呼び出し', 'Interne Modellaufrufe'], ['Diagnostics and connection checks', '诊断与连接检查', '診斷與連線檢查', '診断・接続確認', 'Diagnosen und Verbindungsprüfungen'], ['No observed calls', '暂无观测到的调用', '暫無觀測到的呼叫', '観測された呼び出しなし', 'Keine beobachteten Aufrufe'], ['Internal calls do not add business requests or savings. Execution usage already included in a local business result is excluded from overhead. Queue/load is a measured combined stage; unknown stages are not zero.', '内部调用不增加业务请求数或节省。已进入本地业务结果的执行用量不再计入开销。排队/加载是实测组合阶段，未知阶段不填零。', '內部呼叫不增加業務請求數或節省。已進入本機業務結果的執行用量不再計入開銷。排隊/載入是實測組合階段，未知階段不填零。', '内部呼び出しは業務件数・削減量に加えません。ローカル結果に含まれる実行使用量はオーバーヘッドから除外します。待機／読込は実測の複合段階で、不明はゼロではありません。', 'Interne Aufrufe erhöhen weder Geschäftsanfragen noch Einsparungen. Bereits im lokalen Ergebnis enthaltene Ausführung wird vom Zusatzverbrauch ausgeschlossen. Warten/Laden ist eine gemeinsam gemessene Phase; unbekannte Phasen sind nicht Null.'],
    ['Judgment', '判断', '判斷', '判定', 'Beurteilung'], ['Execution', '执行', '執行', '実行', 'Ausführung'], ['Redaction', '脱敏', '去識別化', '匿名化', 'Anonymisierung'], ['Calls', '调用次数', '呼叫次數', '呼び出し数', 'Aufrufe'], ['Inference started', '已发起推理', '已發起推論', '推論開始済み', 'Inferenz gestartet'], ['Inference not started', '未发起推理', '未發起推論', '推論未開始', 'Inferenz nicht gestartet'], ['Business usage already counted', '已计入业务用量的调用', '已計入業務用量的呼叫', '業務使用量に計上済み', 'Bereits als Geschäftsnutzung gezählt'], ['Overhead calls', '额外开销调用', '額外開銷呼叫', '追加負荷の呼び出し', 'Aufrufe mit Zusatzverbrauch'],
    ['Overhead reported tokens', '额外开销实际 Token', '額外開銷實際 Token', '追加負荷の報告トークン', 'Gemeldete Tokens des Zusatzverbrauchs'], ['Known calls', '已知调用', '已知呼叫', '既知の呼び出し', 'Bekannte Aufrufe'], ['Unknown calls', '未知调用', '未知呼叫', '不明な呼び出し', 'Unbekannte Aufrufe'], ['Queue and load (ms)', '排队与加载（毫秒）', '排隊與載入（毫秒）', '待機と読込（ms）', 'Warten und Laden (ms)'], ['Queue (ms)', '排队（毫秒）', '排隊（毫秒）', '待機（ms）', 'Warten (ms)'], ['Load (ms)', '加载（毫秒）', '載入（毫秒）', '読込（ms）', 'Laden (ms)'], ['Inference (ms)', '推理（毫秒）', '推論（毫秒）', '推論（ms）', 'Inferenz (ms)'], ['Request headers (ms)', '响应头等待（毫秒）', '回應標頭等待（毫秒）', '応答ヘッダー待機（ms）', 'Warten auf Antwortheader (ms)'],
    ['Response consumption (ms)', '响应读取（毫秒）', '回應讀取（毫秒）', '応答読込（ms）', 'Lesen der Antwort (ms)'], ['Total time (ms)', '总耗时（毫秒）', '總耗時（毫秒）', '合計時間（ms）', 'Gesamtdauer (ms)'], ['Configured cloud model', '配置的云端模型', '設定的雲端模型', '設定されたクラウドモデル', 'Konfiguriertes Cloud-Modell'], ['Actual local model', '实际本地模型', '實際本機模型', '実際のローカルモデル', 'Tatsächliches lokales Modell'], ['Purpose', '用途', '用途', '用途', 'Zweck'], ['Connection check', '连接检查', '連線檢查', '接続確認', 'Verbindungsprüfung'], ['Not admitted', '未获准执行', '未獲准執行', '実行許可なし', 'Nicht zugelassen'], ['Model fingerprint', '模型指纹', '模型指紋', 'モデルの識別情報', 'Modell-Fingerabdruck'],
    ['Material version', '功能材料版本', '功能材料版本', '機能資料のバージョン', 'Version der Funktionsmaterialien'], ['Mode', '模式', '模式', 'モード', 'Modus'], ['Formal', '正式', '正式', '正式', 'Regulär'], ['Experimental', '实验', '實驗', '実験', 'Experimentell'], ['Reference price snapshot', '参考价格快照', '參考價格快照', '参考価格のスナップショット', 'Referenzpreis zum Anfragezeitpunkt'], ['Currency', '币种', '幣別', '通貨', 'Währung'], ['Input price per million tokens', '每百万输入 Token 参考价', '每百萬輸入 Token 參考價', '百万入力トークン当たりの価格', 'Preis je Million Eingabe-Tokens'], ['Output price per million tokens', '每百万输出 Token 参考价', '每百萬輸出 Token 參考價', '百万出力トークン当たりの価格', 'Preis je Million Ausgabe-Tokens'],
    ['Price version', '价格版本', '價格版本', '価格バージョン', 'Preisversion'], ['Price source', '价格来源', '價格來源', '価格の出典', 'Preisquelle'], ['No requests yet', '暂无请求', '暫無請求', 'リクエストはまだありません', 'Noch keine Anfragen'], ['No matching requests', '没有匹配的请求', '沒有符合的請求', '一致するリクエストはありません', 'Keine passenden Anfragen'], ['Invalid request filters', '请求筛选无效，请检查应用标识。', '請求篩選無效，請檢查應用程式識別碼。', '絞り込み条件が無効です。アプリIDを確認してください。', 'Ungültige Anfragefilter. Anwendungs-ID prüfen.'], ['Request ledger unavailable', '请求账本不可用', '請求帳本無法使用', 'リクエスト台帳を利用できません', 'Anfragenprotokoll nicht verfügbar'], ['Request observation is not connected yet', '请求观测尚未接通', '請求觀測尚未接通', 'リクエスト観測は未接続です', 'Anfragebeobachtung noch nicht angebunden'], ['Request no longer retained', '请求已不在保留范围', '請求已不在保留範圍', 'リクエストは保存期間外です', 'Anfrage nicht mehr gespeichert'],
    ['Loading requests…', '正在读取请求…', '正在讀取請求…', 'リクエストを読み込み中…', 'Anfragen werden geladen…'], ['Unknown', '未知', '未知', '不明', 'Unbekannt'], ['Application ID filter', '应用标识筛选', '應用程式識別碼篩選', 'アプリIDで絞り込み', 'Anwendungs-ID filtern'], ['All statuses', '全部状态', '全部狀態', 'すべての状態', 'Alle Zustände'], ['All operations', '全部操作', '全部操作', 'すべての操作', 'Alle Operationen'], ['Apply filters', '应用筛选', '套用篩選', '絞り込む', 'Filter anwenden'], ['Processing', '处理中', '處理中', '処理中', 'In Bearbeitung'], ['Succeeded', '成功', '成功', '成功', 'Erfolgreich'],
    ['Failed', '失败', '失敗', '失敗', 'Fehlgeschlagen'], ['Canceled', '客户端取消', '用戶端取消', 'クライアントがキャンセル', 'Vom Client abgebrochen'], ['Timed out', '超时', '逾時', 'タイムアウト', 'Zeitüberschreitung'], ['Interrupted', '中断', '中斷', '中断', 'Unterbrochen'], ['Generation', '生成', '生成', '生成', 'Generierung'], ['Token counting', 'Token 计数', 'Token 計數', 'トークン計数', 'Tokenzählung'], ['Operation', '操作', '操作', '操作', 'Operation'], ['Status', '状态', '狀態', '状態', 'Status'],
    ['Request start', '请求开始', '請求開始', 'リクエスト開始', 'Anfragestart'], ['Request end', '请求结束', '請求結束', 'リクエスト終了', 'Anfrageende'], ['Duration (ms)', '耗时（毫秒）', '耗時（毫秒）', '所要時間（ms）', 'Dauer (ms)'], ['Attempts', '尝试次数', '嘗試次數', '試行回数', 'Versuche'], ['Reported tokens', '实际报告 Token', '實際回報 Token', '報告されたトークン', 'Gemeldete Tokens'], ['Input tokens', '输入 Token', '輸入 Token', '入力トークン', 'Eingabe-Tokens'], ['Output tokens', '输出 Token', '輸出 Token', '出力トークン', 'Ausgabe-Tokens'], ['Total tokens', '总 Token', '總 Token', '合計トークン', 'Tokens gesamt'],
    ['Known requests', '已知用量请求', '已知用量請求', '使用量既知のリクエスト', 'Anfragen mit bekannten Werten'], ['Unknown requests', '未知用量请求', '未知用量請求', '使用量不明のリクエスト', 'Anfragen mit unbekannten Werten'], ['Request ID', '请求标识', '請求識別碼', 'リクエストID', 'Anfrage-ID'], ['Provider ID', '供应商标识', '供應商識別碼', 'プロバイダーID', 'Anbieter-ID'], ['Destination', '执行去向', '執行去向', '実行先', 'Ausführungsziel'], ['Cloud', '云端', '雲端', 'クラウド', 'Cloud'], ['No outbound attempt', '未出站', '未出站', '上流への試行なし', 'Kein ausgehender Versuch'], ['Output committed', '已提交输出', '已提交輸出', '出力送信済み', 'Ausgabe gesendet'],
    ['Error code', '错误码', '錯誤碼', 'エラーコード', 'Fehlercode'], ['Failure stage', '失败阶段', '失敗階段', '失敗段階', 'Fehlerphase'], ['HTTP status', 'HTTP 状态', 'HTTP 狀態', 'HTTPステータス', 'HTTP-Status'], ['Planned retry wait (ms)', '计划重试等待（毫秒）', '計畫重試等待（毫秒）', '予定再試行待機（ms）', 'Geplante Wiederholungswartezeit (ms)'], ['Actual retry wait (ms)', '实际重试等待（毫秒）', '實際重試等待（毫秒）', '実際の再試行待機（ms）', 'Tatsächliche Wiederholungswartezeit (ms)'], ['Retry wait error', '重试等待错误', '重試等待錯誤', '再試行待機エラー', 'Wiederholungswartefehler'], ['Next attempt started', '后续尝试已开始', '後續嘗試已開始', '次の試行が開始済み', 'Nächster Versuch gestartet'], ['Older requests', '更早的请求', '更早的請求', '以前のリクエスト', 'Ältere Anfragen'],
    ['Latest requests', '最新请求', '最新請求', '最新のリクエスト', 'Neueste Anfragen'], ['Request details', '请求详情', '請求詳情', 'リクエスト詳細', 'Anfragedetails'], ['Close details', '关闭详情', '關閉詳情', '詳細を閉じる', 'Details schließen'], ['Ledger write failures', '账本写入故障次数', '帳本寫入故障次數', '台帳の書き込み失敗数', 'Schreibfehler im Protokoll'], ['Missing internal observations', '内部观测缺失次数', '內部觀測缺失次數', '欠落した内部観測数', 'Fehlende interne Beobachtungen'], ['Internal statistics may be incomplete after an observation failure.', '内部观测发生故障，内部统计可能不完整。', '內部觀測發生故障，內部統計可能不完整。', '内部観測の障害により、内部統計が不完全な可能性があります。', 'Nach einem Beobachtungsfehler können interne Statistiken unvollständig sein.'], ['History may be incomplete after a storage failure.', '存储故障后，历史记录可能不完整。', '儲存故障後，歷史紀錄可能不完整。', '保存障害により履歴が不完全な可能性があります。', 'Nach einem Speicherfehler kann der Verlauf unvollständig sein.'], ['Retention limit', '保留上限', '保留上限', '保存上限', 'Aufbewahrungsgrenze'],
    ['Retention age (seconds)', '保留时长（秒）', '保留時長（秒）', '保存期間（秒）', 'Aufbewahrung (Sekunden)'], ['Oldest retained generation', '最早保留的生成请求', '最早保留的生成請求', '保存済み生成の最古日時', 'Älteste gespeicherte Generierung'], ['Newest retained generation', '最新保留的生成请求', '最新保留的生成請求', '保存済み生成の最新日時', 'Neueste gespeicherte Generierung'], ['Redaction status', '脱敏调用状态', '去識別化呼叫狀態', '匿名化の呼出状態', 'Status des Anonymisierungsaufrufs'], ['Redaction call duration (ms)', '脱敏调用耗时（毫秒）', '去識別化呼叫耗時（毫秒）', '匿名化の呼出時間（ms）', 'Anonymisierungsaufrufdauer (ms)'], ['Average inference (ms)', '平均推理耗时（毫秒）', '平均推論耗時（毫秒）', '平均推論時間（ms）', 'Durchschnittliche Inferenz (ms)'], ['Average total time (ms)', '平均总耗时（毫秒）', '平均總耗時（毫秒）', '平均合計時間（ms）', 'Durchschnittliche Gesamtdauer (ms)'], ['All destinations', '全部去向', '全部去向', 'すべての実行先', 'Alle Ausführungsziele'],
    ['Routing / failure reason', '转云/失败原因', '轉雲/失敗原因', '切替・失敗の理由', 'Routing-/Fehlergrund'], ['Redaction failed', '脱敏处理失败', '去識別化處理失敗', '匿名化処理に失敗', 'Anonymisierung fehlgeschlagen'], ['Redaction canceled', '脱敏已取消', '去識別化已取消', '匿名化がキャンセルされました', 'Anonymisierung abgebrochen'], ['Redaction runtime closed', '脱敏运行时已关闭', '去識別化執行階段已關閉', '匿名化ランタイムが終了しました', 'Anonymisierungslaufzeit geschlossen'], ['Redaction extraction failed', '敏感实体提取失败', '敏感實體擷取失敗', '機密エンティティの抽出に失敗', 'Entitätsextraktion fehlgeschlagen'], ['Redaction input exceeds limit', '脱敏输入超过上限', '去識別化輸入超過上限', '匿名化入力が上限を超過', 'Anonymisierungseingabe überschreitet Limit'], ['Invalid redaction input', '脱敏输入无效', '去識別化輸入無效', '無効な匿名化入力', 'Ungültige Anonymisierungseingabe'], ['Redaction returned invalid response', '脱敏模型返回格式无效', '去識別化模型傳回格式無效', '匿名化モデルの応答形式が無効', 'Anonymisierung lieferte ungültige Antwort'],
    ['Redaction result failed validation', '脱敏结果校验失败', '去識別化結果驗證失敗', '匿名化結果の検証に失敗', 'Validierung des Anonymisierungsergebnisses fehlgeschlagen'], ['Redaction entity conflict', '敏感实体映射冲突', '敏感實體映射衝突', '機密エンティティのマッピング競合', 'Entitätskonflikt bei der Anonymisierung'], ['Redaction mapping limit exceeded', '敏感实体映射数量超限', '敏感實體映射數量超限', 'エンティティマッピング数が上限を超過', 'Zuordnungslimit für Anonymisierung überschritten'], ['Redaction placeholder limit exceeded', '脱敏占位符数量超限', '去識別化佔位符數量超限', 'プレースホルダー数が上限を超過', 'Platzhalterlimit für Anonymisierung überschritten'], ['Redaction session released', '脱敏会话已释放', '去識別化工作階段已釋放', '匿名化セッションが解放されました', 'Anonymisierungssitzung freigegeben'], ['Redaction response exceeds limit', '脱敏响应超过上限', '去識別化回應超過上限', '匿名化応答が上限を超過', 'Anonymisierungsantwort überschreitet Limit'], ['Redaction specification failed', '脱敏规范校验未通过', '去識別化規範驗證未通過', '匿名化仕様の検証に失敗', 'Anonymisierungsspezifikation fehlgeschlagen'], ['Redaction timed out', '脱敏超时', '去識別化逾時', '匿名化がタイムアウト', 'Zeitüberschreitung bei der Anonymisierung'],
    ['Redaction unavailable', '脱敏服务不可用', '去識別化服務不可用', '匿名化サービスを利用できません', 'Anonymisierung nicht verfügbar'], ['Stream restore failed', '流式脱敏还原失败', '串流去識別化還原失敗', 'ストリーム復元処理に失敗', 'Stream-Wiederherstellung fehlgeschlagen'],
  ]) for (const [lang, text] of [['zh', zh], ['zh-TW', tw], ['ja', ja], ['de', de]]) (I18N[lang] ||= {})[key] = text;

  const statuses = { processing: 'Processing', succeeded: 'Succeeded', failed: 'Failed', canceled: 'Canceled', timed_out: 'Timed out', interrupted: 'Interrupted' };
  const stages = { preflight: 'Preflight', configuration: 'Configuration', configured: 'Configuration', validation: 'Validation', cloud: 'Cloud execution', retry_wait: 'Retry waiting', response: 'Response output', interrupted: 'Interrupted' };
  const operations = { generate: 'Generation', count_tokens: 'Token counting' }, destinations = { local: 'Local', cloud: 'Cloud', none: 'No outbound attempt' };
  const callPurposes = { judgment: 'Judgment', execution: 'Execution', redaction: 'Redaction' }, callResults = { ...statuses, rejected: 'Not admitted' };
  const routingReasons = {
    unavailable: 'Local routing is unavailable', judgment_queue_timeout: 'Judgment queue timed out', judgment_inference_timeout: 'Judgment inference timed out', judgment_stage_timeout: 'Judgment budget exhausted', judgment_failed: 'Judgment failed', judgment_input_exceeded: 'Judgment input exceeds its limit', judgment_summary_incomplete: 'Request cannot be summarized safely', context_exceeded: 'Context window exceeded',
    context_limit_exceeded: 'Context window exceeded', token_count_unavailable: 'Local token count is unavailable', local_generation_http_error: 'Local service returned an HTTP error', execution_queue_timeout: 'Execution queue timed out', execution_timeout: 'Local execution timed out', execution_unavailable: 'Local execution is unavailable', execution_failed: 'Local execution failed', execution_protocol_mismatch: 'Local output protocol is unsupported',
    invalid_decision: 'Judgment returned an invalid decision', local_budget_reserved: 'Time reserved for cloud fallback', request_budget_exhausted: 'Whole request budget exhausted', request_canceled: 'Canceled', local_generation_failed: 'Local execution failed', budget_exhausted: 'Whole request budget exhausted', runtime_settings_changed: 'Runtime settings changed during the request', local_http_error: 'Local service returned an HTTP error',
    redaction_failed: 'Redaction failed', redaction_canceled: 'Redaction canceled', redaction_closed: 'Redaction runtime closed', redaction_extraction_failed: 'Redaction extraction failed', redaction_input_limit: 'Redaction input exceeds limit', redaction_invalid_input: 'Invalid redaction input', redaction_invalid_response: 'Redaction returned invalid response', redaction_invalid_result: 'Redaction result failed validation',
    redaction_entity_conflict: 'Redaction entity conflict', redaction_mapping_limit: 'Redaction mapping limit exceeded', redaction_placeholder_limit: 'Redaction placeholder limit exceeded', redaction_released: 'Redaction session released', redaction_response_limit: 'Redaction response exceeds limit', redaction_specification_failed: 'Redaction specification failed', redaction_timeout: 'Redaction timed out', redaction_unavailable: 'Redaction unavailable', stream_restore_failed: 'Stream restore failed',
  };
  const reason = code => { const key = routingReasons[code] || routingReasons[String(code || '').replace(/^local_/, '')]; return key ? t(key) : code ? t('Unknown') : '—'; };
  const recordReason = record => {
    if (record.capabilityState) return reason(record.capabilityState);
    if (record.safeErrorCode) { const key = routingReasons[record.safeErrorCode] || routingReasons[String(record.safeErrorCode).replace(/^local_/, '')]; return key ? t(key) : record.safeErrorCode; }
    return '—';
  };
  const timingFields = { queueLoadMs: 'Queue and load (ms)', queueMs: 'Queue (ms)', loadMs: 'Load (ms)', requestHeadersMs: 'Request headers (ms)', consumeMs: 'Response consumption (ms)', inferenceMs: 'Inference (ms)', totalMs: 'Total time (ms)' };
  let records = [], summary = null, ledger = null, nextCursor = '', cursor = '', selected = null;
  let state = 'Management token required', detailError = '', loading = false, sequence = 0, status = '', operation = '', destinationFilter = '';
  const visible = () => view === 'usage';
  const num = value => Number.isSafeInteger(value) && value >= 0 ? value.toLocaleString(intlLang()) : t('Unknown');
  const milliseconds = value => typeof value === 'number' && Number.isFinite(value) && value >= 0 && value <= Number.MAX_SAFE_INTEGER ? value.toLocaleString(intlLang(), { maximumFractionDigits: 3 }) : t('Unknown');
  const requestDuration = record => record.status === 'processing' ? t('Processing') : record.executionKnown === false ? t('Unknown') : milliseconds(record.durationMs);
  const date = value => value && Number.isFinite(Date.parse(value)) ? new Date(value).toLocaleString(intlLang()) : t('Unknown');
  const label = (values, value) => values[value] ? t(values[value]) : t('Unknown');
  function button(text, click, className = 'text') { const b = el('button', className, t(text)); b.type = 'button'; b.onclick = click; b.disabled = loading; return b; }
  function empty(message) { const box = el('div', 'list router-empty'), inner = el('div', 'empty-state'); inner.setAttribute('role', 'status'); inner.append(el('b', '', t(message))); box.append(inner); return box; }
  function field(list, name, value) { list.append(el('dt', '', t(name)), el('dd', '', value)); }
  const destination = value => t(value === 'local' ? 'Local' : value === 'cloud' ? 'Cloud' : value === 'none' ? 'No outbound attempt' : 'Unknown');
  const actualModel = record => record.destination === 'local' ? record.localModel || t('Unknown') : record.destination === 'cloud' ? record.model || t('Unknown') : t('Unknown');
  const amount = value => typeof value === 'string' && /^(0|[1-9]\d*)(\.\d+)?$/.test(value) && value.length <= 256 ? value : t('Unknown');
  const priceNumber = value => typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value.toLocaleString(intlLang(), { maximumFractionDigits: 20 }) : t('Unknown');
  function aggregate(value, calls = false) { return num(value?.knownTokens) + ' · ' + t(calls ? 'Known calls' : 'Known requests') + ': ' + num(value?.knownRequests) + ' · ' + t(calls ? 'Unknown calls' : 'Unknown requests') + ': ' + num(value?.unknownRequests); }
  function section(name, className = '') { const box = el('section', 'list request-stat-section ' + className); box.append(el('h2', '', t(name))); return box; }
  function observationWarning() {
    if (!ledger?.observationFailures) return null;
    const warning = el('p', 'usage-note router-note request-observation-warning'); warning.setAttribute('role', 'status');
    warning.textContent = t('Internal statistics may be incomplete after an observation failure.') + ' ' + t('Missing internal observations') + ': ' + num(ledger.observationFailures); return warning;
  }
  function retainedRange() {
    const range = el('dl', 'request-fields');
    for (const [name, value] of [['Oldest retained generation', date(summary?.oldestStartedAt)], ['Newest retained generation', date(summary?.newestStartedAt)], ['Retention limit', num(ledger?.maxRecords)], ['Retention age (seconds)', num(ledger?.maxAgeSeconds)]]) field(range, name, value);
    return range;
  }
  function destinationBreakdown() {
    const box = section('Destination breakdown', 'request-destinations');
    box.append(el('p', 'router-note', t('Final local and cloud destinations. One business request is counted once; retries and internal calls do not add requests.')), el('p', 'router-note', t('The denominator includes all retained business generation requests, including failures and no-dispatch requests. Diagnostics and token counting are excluded.')));
    if (!summary?.destinations) { box.append(el('p', '', t('Unknown'))); return box; }
    const groups = el('div', 'request-stat-grid');
    for (const group of summary.destinations) {
      const card = el('div', 'request-stat-group'), fields = el('dl', 'request-fields'); card.dataset.destination = group.destination; card.append(el('h3', '', destination(group.destination)));
      field(fields, 'Requests', num(group.total));
      field(fields, 'Share of retained generation requests', typeof group.share === 'number' && Number.isFinite(group.share) && group.share >= 0 && group.share <= 1 ? group.share.toLocaleString(intlLang(), { style: 'percent', maximumFractionDigits: 2 }) : t('Unknown'));
      for (const [key, name] of [['succeeded', 'Succeeded'], ['failed', 'Failed'], ['canceled', 'Canceled'], ['timedOut', 'Timed out'], ['interrupted', 'Interrupted'], ['processing', 'Processing']]) field(fields, name, num(group[key]));
      for (const [key, name] of [['inputTokens', 'Input tokens'], ['outputTokens', 'Output tokens'], ['totalTokens', 'Total tokens']]) field(fields, name, aggregate(group[key]));
      card.append(fields); groups.append(card);
    }
    box.append(groups); return box;
  }
  function savings() {
    const box = section('Estimated cloud savings', 'request-savings'), value = summary?.savings, fields = el('dl', 'request-fields');
    box.append(el('p', 'router-note', t('Only successful local business generations qualify. Input plus output tokens estimate avoided cloud tokens; tokenizers may differ. Amounts use request-time Profile prices per million tokens, grouped by currency. Estimates are not bills or net savings.')));
    field(fields, 'Eligible local successes', num(value?.eligibleRequests));
    for (const [key, name] of [['inputTokens', 'Input tokens'], ['outputTokens', 'Output tokens'], ['avoidedTokens', 'Estimated avoided cloud tokens']]) field(fields, name, aggregate(value?.[key]));
    field(fields, 'Unknown reference price requests', num(value?.unknownPriceRequests)); box.append(fields, el('h3', '', t('Estimated savings by currency')));
    if (!value?.money?.length) box.append(el('p', '', t('Unknown')));
    for (const group of value?.money || []) {
      const row = el('p', 'request-money'); row.dataset.currency = group.currency;
      row.textContent = group.currency + ': ' + amount(group.knownAmount) + ' · ' + t('Known requests') + ': ' + num(group.knownRequests) + ' · ' + t('Unknown requests') + ': ' + num(group.unknownRequests); box.append(row);
    }
    return box;
  }
  function callsSummary(name, groups) {
    const box = section(name, name === 'Internal model calls' ? 'request-internal' : 'request-diagnostics');
    box.append(el('p', 'router-note', t('Internal calls do not add business requests or savings. Execution usage already included in a local business result is excluded from overhead. Queue/load is a measured combined stage; unknown stages are not zero.')));
    groups = groups?.filter(group => group.total !== 0);
    if (!groups?.length) { box.append(el('p', '', t('No observed calls'))); return box; }
    const avgTiming = timing => timing?.knownCalls > 0 && typeof timing.knownMs === 'number' && Number.isFinite(timing.knownMs) ? milliseconds(timing.knownMs / timing.knownCalls) : t('Unknown');
    for (const group of groups) {
      const card = el('div', 'request-stat-group'), fields = el('dl', 'request-fields'); card.dataset.purpose = group.purpose; card.append(el('h3', '', label(callPurposes, group.purpose)));
      for (const [key, title] of [['total', 'Calls'], ['inferenceStarted', 'Inference started'], ['notStarted', 'Inference not started'], ['businessUsageCalls', 'Business usage already counted'], ['overheadUsageCalls', 'Overhead calls']]) field(fields, title, num(group[key]));
      for (const [result, count] of Object.entries(group.results || {})) field(fields, callResults[result] || 'Unknown', num(count));
      for (const [key, title] of [['inputTokens', 'Input tokens'], ['outputTokens', 'Output tokens'], ['totalTokens', 'Total tokens']]) field(fields, title, aggregate(group[key], true));
      const overhead = el('div', 'request-overhead'), overheadFields = el('dl', 'request-fields'); overhead.append(el('b', '', t('Overhead reported tokens')));
      for (const [key, title] of [['overheadInputTokens', 'Input tokens'], ['overheadOutputTokens', 'Output tokens'], ['overheadTotalTokens', 'Total tokens']]) field(overheadFields, title, aggregate(group[key], true));
      for (const [key, title] of Object.entries(timingFields)) { const timing = group.timings?.[key]; field(fields, title, milliseconds(timing?.knownMs) + ' · ' + t('Known calls') + ': ' + num(timing?.knownCalls) + ' · ' + t('Unknown calls') + ': ' + num(timing?.unknownCalls)); }
      field(fields, 'Average inference (ms)', avgTiming(group.timings?.inferenceMs)); field(fields, 'Average total time (ms)', avgTiming(group.timings?.totalMs));
      overhead.append(overheadFields); card.append(fields, overhead); box.append(card);
    }
    return box;
  }
  function tokenText(usage) { return ['inputTokens', 'outputTokens', 'totalTokens'].map((k, i) => t(['Input tokens', 'Output tokens', 'Total tokens'][i]) + ': ' + num(usage?.[k])).join(' · '); }
  function detail() {
    const card = el('section', 'request-detail list'), head = el('div', 'row-head'), dl = el('dl', 'request-fields'); card.dataset.requestDetail = selected.requestId;
    head.append(el('b', '', t('Request details')), button('Close details', () => { selected = null; render(); })); card.append(head);
    for (const [name, value] of [
      ['Request ID', selected.requestId], ['Application ID', selected.appId], ['Provider ID', selected.providerId || t('Unknown')], ['Configured cloud model', selected.model || t('Unknown')], ['Actual local model', selected.localModel || t('Unknown')], ['Application path', selected.path],
      ['Operation', label(operations, selected.operation)], ['Status', label(statuses, selected.status)], ['Request start', date(selected.startedAt)], ['Request end', selected.executionKnown === false ? t('Unknown') : date(selected.endedAt)], ['Duration (ms)', requestDuration(selected)],
      ['Destination', selected.executionKnown === false ? t('Unknown') : destination(selected.destination)], ['Attempts', num(selected.executionKnown === false ? null : selected.attempts)], ['Output committed', t(selected.executionKnown === false ? 'Unknown' : selected.outputCommitted ? 'Yes' : 'No')],
      ['Error code', selected.safeErrorCode || '—'], ['Failure stage', selected.failureStage ? label(stages, selected.failureStage) : '—'], ['Reported tokens', tokenText(selected.usage)],
    ]) field(dl, name, value);
    field(dl, 'Routing decision', selected.decision ? destination(selected.decision) : t('Unknown'));
    const purposeStatus = purpose => { const calls = selected.calls?.filter(call => call.purpose === purpose); return calls?.length ? label(callResults, calls[calls.length - 1].result) : t('Not observed'); };
    field(dl, 'Judgment status', purposeStatus('judgment')); field(dl, 'Execution status', purposeStatus('execution')); field(dl, 'Redaction status', purposeStatus('redaction'));
    field(dl, 'Judgment duration (ms)', milliseconds(selected.judgmentMs));
    const redactionCalls = selected.calls?.filter(call => call.purpose === 'redaction') || [], lastRedaction = redactionCalls[redactionCalls.length - 1];
    field(dl, 'Redaction call duration (ms)', redactionCalls.length ? milliseconds(lastRedaction?.timings?.totalMs) : t('Not observed'));
    const judgmentCalls = selected.calls?.filter(call => call.purpose === 'judgment') || [], judgment = judgmentCalls[judgmentCalls.length - 1];
    const observedCloudDecision = selected.decision === 'cloud' && judgment?.result === 'succeeded' && judgment.inferenceStarted === true;
    field(dl, 'Cloud routing reason', selected.destination === 'cloud' ? selected.capabilityState ? reason(selected.capabilityState) : observedCloudDecision ? t('Judgment selected cloud') : t('Unknown') : '—');
    card.append(dl);
    const price = selected.referencePrice, priceSection = el('div', 'request-price'), priceFields = el('dl', 'request-fields'); priceSection.append(el('b', '', t('Reference price snapshot')));
    for (const [name, value] of [['Currency', price?.currency || t('Unknown')], ['Input price per million tokens', price?.unit === 'million-tokens' ? priceNumber(price.input) : t('Unknown')], ['Output price per million tokens', price?.unit === 'million-tokens' ? priceNumber(price.output) : t('Unknown')], ['Price version', price?.version || t('Unknown')], ['Price source', price?.source || t('Unknown')]]) field(priceFields, name, value);
    priceSection.append(priceFields); card.append(priceSection);
    if (selected.calls?.length) {
      const calls = el('div', 'request-call-details'); calls.append(el('h3', '', t('Internal model calls')));
      for (const call of selected.calls) {
        const fields = el('dl', 'request-fields');
        for (const [name, value] of [['Purpose', label(callPurposes, call.purpose)], ['Actual local model', call.modelId || t('Unknown')], ['Model fingerprint', call.fingerprint || t('Unknown')], ['Material version', call.materialVersion || t('Unknown')], ['Mode', label({ formal: 'Formal', experimental: 'Experimental' }, call.mode)], ['Status', label(callResults, call.result)], ['Inference started', t(call.inferenceStarted === true ? 'Yes' : call.inferenceStarted === false ? 'No' : 'Unknown')], ['Request start', date(call.startedAt)], ['Request end', date(call.endedAt)], ['Error code', call.safeErrorCode || '—'], ['Reported tokens', tokenText(call.usage)]]) field(fields, name, value);
        for (const [key, title] of Object.entries(timingFields)) field(fields, title, milliseconds(call.timings?.[key]));
        field(fields, 'Failure reason', reason(call.safeErrorCode));
        if (call.contextUsage || call.settings?.contextTokens) field(fields, 'Local context window', num(call.contextUsage?.contextTokens ?? call.settings.contextTokens));
        if (call.contextUsage) { field(fields, 'Input tokens', num(call.contextUsage.inputTokens)); field(fields, 'Output token reserve', num(call.contextUsage.outputTokens)); }
        if (call.httpStatus) field(fields, 'HTTP status', num(call.httpStatus));
        if (call.settingsFingerprint) field(fields, 'Local settings fingerprint', call.settingsFingerprint);
        if (call.settings?.contextTokens) for (const [key, title] of [['judgmentQueueSeconds', 'Judgment queue (seconds)'], ['judgmentInferenceSeconds', 'Judgment inference (seconds)'], ['executionQueueSeconds', 'Execution queue (seconds)'], ['executionSeconds', 'Local execution (seconds)'], ['loadSeconds', 'Model loading (seconds)'], ['requestSeconds', 'Whole request (seconds)'], ['cloudReserveSeconds', 'Cloud fallback reserve (seconds)']]) field(fields, title, num(call.settings[key]));
        calls.append(fields);
      }
      card.append(calls);
    }
    for (const attempt of selected.attemptDetails || []) {
      const section = el('div', 'request-attempt'), values = el('dl', 'request-fields'); section.append(el('b', '', t('Attempts') + ' ' + num(attempt.number)));
      for (const [name, value] of [['Request start', date(attempt.startedAt)], ['Duration (ms)', milliseconds(attempt.durationMs)], ['HTTP status', attempt.httpStatus ? num(attempt.httpStatus) : t('Unknown')], ['Error code', attempt.errorCode || '—'], ['Output committed', t(attempt.outputCommitted ? 'Yes' : 'No')], ['Planned retry wait (ms)', milliseconds(attempt.retryDelayMs)], ['Actual retry wait (ms)', milliseconds(attempt.retryWaitMs)], ['Retry wait error', attempt.retryErrorCode || '—'], ['Next attempt started', t(attempt.retried ? 'Yes' : 'No')], ['Reported tokens', tokenText(attempt.usage)]]) field(values, name, value);
      section.append(values); card.append(section);
    }
    return card;
  }
  function list() {
    if (state) return empty(state);
    const box = el('div'), warning = observationWarning(); if (warning) box.append(warning);
    if (detailError) box.append(empty(detailError));
    if (selected) box.append(detail());
    const visibleRecords = destinationFilter ? records.filter(r => r.executionKnown !== false && r.destination === destinationFilter) : records;
    if (!visibleRecords.length) { box.append(empty(status || operation || destinationFilter || $('#requestApp').value.trim() ? 'No matching requests' : 'No requests yet')); return box; }
    const tableWrap = el('div', 'list request-table-wrap'), table = el('table', 'request-table'), head = el('tr'), thead = el('thead'), body = el('tbody');
    for (const key of ['Application ID', 'Model', 'Operation', 'Status', 'Request start', 'Duration (ms)', 'Destination', 'Attempts', 'Reported tokens', 'Routing decision', 'Routing / failure reason']) head.append(el('th', '', t(key)));
    thead.append(head); table.append(thead);
    for (const record of visibleRecords) {
      const row = el('tr'), app = el('td'), open = button(record.appName || record.appId, () => loadDetail(record.requestId)); row.dataset.request = record.requestId;
      open.textContent = record.appName || record.appId; open.setAttribute('aria-label', t('Request details') + ': ' + record.requestId); app.append(open, el('small', '', record.appId)); row.append(app);
      for (const [index, value] of [actualModel(record), label(operations, record.operation), label(statuses, record.status), date(record.startedAt), requestDuration(record), record.executionKnown === false ? t('Unknown') : destination(record.destination), num(record.executionKnown === false ? null : record.attempts), tokenText(record.usage), record.decision ? destination(record.decision) : t('Unknown'), recordReason(record)].entries()) {
        const cell = el('td', '', value); if (index === 0) cell.title = value; row.append(cell);
      }
      body.append(row);
    }
    table.append(body); tableWrap.append(table); box.append(tableWrap);
    const bar = el('div', 'request-pages'); if (cursor) bar.append(button('Latest requests', () => load(''))); if (nextCursor) bar.append(button('Older requests', () => load(nextCursor))); box.append(bar); return box;
  }
  function overview() {
    if (state) return empty(state);
    const box = el('div');
    box.append(el('p', 'usage-note router-note', t('Generation requests only. Totals cover retained records, not all-time usage. Auxiliary operations are excluded.')), el('p', 'usage-note router-note', t('Actual reported usage. Missing fields remain unknown; explicit zero stays zero.')), el('p', 'usage-note router-note', t('Only the final reported response usage is counted once. Usage of failed or retried attempts may be unknown; these totals are not billed usage across all attempts.')));
    if (!summary) return empty('Request ledger unavailable');
    if (ledger?.failureCount) { const warning = el('p', 'usage-note router-note'); warning.setAttribute('role', 'status'); warning.textContent = t('History may be incomplete after a storage failure.') + ' ' + t('Ledger write failures') + ': ' + num(ledger.failureCount) + ' · ' + t('Error code') + ': ' + (ledger.lastErrorCode || t('Unknown')); box.append(warning); }
    const warning = observationWarning(); if (warning) box.append(warning);
    const stats = el('section', 'stats router-kpis'), usage = el('div', 'list request-usage');
    for (const [key, value] of [['Requests', summary.total], ['Succeeded', summary.succeeded], ['Failed', summary.failed], ['Canceled', summary.canceled], ['Timed out', summary.timedOut], ['Interrupted', summary.interrupted], ['Processing', summary.processing]]) { const kpi = el('div', 'kpi'); kpi.append(el('b', '', num(value)), el('span', '', t(key))); stats.append(kpi); }
    for (const [key, name] of [['inputTokens', 'Input tokens'], ['outputTokens', 'Output tokens'], ['totalTokens', 'Total tokens']]) {
      const value = summary[key], row = el('div', 'row'); row.append(el('b', '', t(name)), el('span', '', (value?.knownRequests ? num(value.knownTokens) : t('Unknown')) + ' · ' + t('Known requests') + ': ' + num(value?.knownRequests) + ' · ' + t('Unknown requests') + ': ' + num(value?.unknownRequests))); usage.append(row);
    }
    box.append(stats, usage, destinationBreakdown(), savings(), callsSummary('Internal model calls', summary.internalCalls), callsSummary('Diagnostics and connection checks', summary.checkCalls), retainedRange());
    if (!summary.total) box.append(empty('No requests yet')); return box;
  }
  function choices(selector, values, current, choose, all) {
    const container = $(selector); if (!container) return;
    container.replaceChildren(...[['', all], ...Object.entries(values)].map(([id, key]) => { const b = button(key, () => choose(id), 'mchip' + (id === current ? ' on' : '')); b.setAttribute('aria-pressed', String(id === current)); return b; }));
  }
  function render() {
    if (!managementToken) state = healthy ? 'Management token required' : 'Connection unavailable';
    $('#period').textContent = t('Retained requests');
    $('#requestScope').textContent = t('Final local and cloud destinations. One business request is counted once; retries and internal calls do not add requests.') + ' ' + t('Actual reported usage. Missing fields remain unknown; explicit zero stays zero.');
    $('#requestAppLabel').textContent = t('Application ID filter'); $('#requestApply').textContent = t('Apply filters'); $('#requestApply').disabled = loading;
    choices('#requestStatuses', statuses, status, id => { status = id; render(); }, 'All statuses');
    choices('#requestOperations', operations, operation, id => { operation = id; render(); }, 'All operations');
    choices('#requestDestinations', destinations, destinationFilter, id => { destinationFilter = id; render(); }, 'All destinations');
    $('#requestSummary').replaceChildren(overview()); $('#requestRecords').replaceChildren(list());
  }
  async function api(path, detailRequest = false) {
    const controller = new AbortController(), timer = setTimeout(() => controller.abort(), 5000);
    try {
      let response;
      try { response = await managementFetch(path, { headers: { Authorization: 'Bearer ' + managementToken }, credentials: 'omit', redirect: 'error', cache: 'no-store', signal: controller.signal }); }
      catch { throw new Error('Connection unavailable'); }
      if (response.status === 401) { managementToken = ''; serviceInfo = null; window.routerAppsDisconnected?.(); renderService(); throw new Error('Management token required'); }
      if (response.status === 400) throw new Error('Invalid request filters');
      if (response.status === 404) throw new Error(detailRequest ? 'Request no longer retained' : 'Request observation is not connected yet');
      if (!response.ok) throw new Error('Request ledger unavailable');
      let data; try { data = await response.json(); } catch { throw new Error('Request ledger unavailable'); }
      if (![1, 2].includes(data.schemaVersion) || data.ledger?.available === false) throw new Error('Request ledger unavailable'); return data;
    } finally { clearTimeout(timer); }
  }
  async function load(before = '') {
    if (!visible()) return;
    if (!managementToken) { render(); return; }
    const request = ++sequence; loading = true; state = 'Loading requests…'; records = []; summary = null; selected = null; detailError = ''; render();
    try {
      if (usageTab === 'usage') {
        const data = await api('/api/requests/summary'); if (request !== sequence || !managementToken) return; summary = data.summary; ledger = data.ledger;
      } else {
        const query = new URLSearchParams({ limit: '50' }); if (before) query.set('before', before); if (status) query.set('status', status); if (operation) query.set('operation', operation); const app = $('#requestApp').value.trim(); if (app) query.set('appId', app);
        const data = await api('/api/requests?' + query); if (request !== sequence || !managementToken) return; if (!Array.isArray(data.requests)) throw new Error('Request ledger unavailable'); records = data.requests; ledger = data.ledger; nextCursor = data.nextCursor || ''; cursor = before;
      }
      state = '';
    } catch (error) { if (request === sequence) { state = error.message; records = []; summary = null; selected = null; } }
    finally { if (request === sequence) { loading = false; render(); } }
  }
  async function loadDetail(id) {
    const request = ++sequence; loading = true; detailError = ''; selected = null; render();
    try { const data = await api('/api/requests/' + encodeURIComponent(id), true); if (request !== sequence || !managementToken) return; if (!data.request || data.request.requestId !== id) throw new Error('Request ledger unavailable'); selected = { ...data.request, calls: data.calls || [] }; }
    catch (error) { if (request === sequence) { detailError = error.message; if (error.message === 'Connection unavailable' || error.message === 'Management token required') { records = []; state = error.message; } } }
    finally { if (request === sequence) { loading = false; render(); } }
  }
  $('#requestFilters').onsubmit = event => { event.preventDefault(); load(''); };
  window.routerObservationsOpen = () => load(''); window.routerObservationsRender = render;
  window.routerObservationsDisconnected = () => { sequence++; loading = false; records = []; summary = null; selected = null; nextCursor = ''; state = healthy ? 'Management token required' : 'Connection unavailable'; render(); };
  render();
})();
