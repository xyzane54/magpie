'use strict';
(() => {
  // This guide keeps reading preferences only. It never calls management or model APIs.
  for (const [key, zh, tw, ja, de] of [
    ['Getting started', '新手入门', '新手入門', 'はじめに', 'Erste Schritte'],
    ['Start here', '从这里开始', '從這裡開始', 'ここから始める', 'Hier beginnen'],
    ['You can skip now and return from Usage guide.', '可以暂时跳过，稍后从“使用指南”继续。', '可以暫時略過，稍後從「使用指南」繼續。', '今はスキップし、後で「使用ガイド」から再開できます。', 'Jetzt überspringen und später über die Anleitung fortsetzen.'],
    ['Usage guide', '使用指南', '使用指南', '使用ガイド', 'Anleitung'],
    ['Tutorial chapters', '教学章节', '教學章節', 'ガイドの章', 'Kapitel der Anleitung'],
    ['Meet Wefto', '认识 Wefto', '認識 Wefto', 'Weftoを知る', 'Wefto kennenlernen'],
    ['Connect a provider', '连接供应商', '連接供應商', 'プロバイダーに接続', 'Anbieter verbinden'],
    ['Set up your app', '配置应用', '設定應用程式', 'アプリを設定', 'App einrichten'],
    ['Choose your local route', '选择本地路线', '選擇本機路線', 'ローカル利用を選ぶ', 'Lokale Nutzung wählen'],
    ['Install and try', '安装与试用', '安裝與試用', 'インストールと試用', 'Installieren und testen'],
    ['Your first request', '第一条请求', '第一筆請求', '最初のリクエスト', 'Die erste Anfrage'],
    ['Close guide', '关闭指南', '關閉指南', 'ガイドを閉じる', 'Anleitung schließen'],
    ['Skip for now', '暂时跳过', '暫時跳過', '今はスキップ', 'Vorerst überspringen'],
    ['Previous step', '上一步', '上一步', '前へ', 'Zurück'],
    ['Next step', '下一步', '下一步', '次へ', 'Weiter'],
    ['Start learning', '开始了解', '開始瞭解', 'ガイドを始める', 'Anleitung beginnen'],
    ['Skip local installation and continue', '跳过本地安装，继续', '略過本機安裝，繼續', 'ローカル導入をスキップして次へ', 'Lokale Installation überspringen'],
    ['Finish reading', '完成阅读', '完成閱讀', '読み終える', 'Lesen abschließen'],
    ['Continue reading', '继续阅读', '繼續閱讀', '続きを読む', 'Weiterlesen'],
    ['Read again', '重新阅读', '重新閱讀', 'もう一度読む', 'Erneut lesen'],
    ['Reading progress {current} / {total}', '阅读进度 {current} / {total}', '閱讀進度 {current} / {total}', '読書の進捗 {current} / {total}', 'Lesefortschritt {current} / {total}'],
    ['Chapter {number}: {name}', '第 {number} 章：{name}', '第 {number} 章：{name}', '第{number}章：{name}', 'Kapitel {number}: {name}'],
    ['Continue from “{chapter}”. Reading progress is separate from configuration.', '从“{chapter}”继续。阅读进度与实际配置分开记录。', '從「{chapter}」繼續。閱讀進度與實際設定分開記錄。', '「{chapter}」から続けます。読書の進捗と設定状況は別です。', 'Bei „{chapter}“ fortsetzen. Lesefortschritt und Konfiguration sind getrennt.'],
    ['Guide read. Check actual settings and request results to confirm readiness.', '指南已读完。是否可用，请以实际设置和请求结果为准。', '指南已讀完。是否可用，請以實際設定和請求結果為準。', 'ガイドを読み終えました。利用可否は実際の設定とリクエスト結果で確認してください。', 'Anleitung gelesen. Betriebsbereitschaft anhand der Einstellungen und Anfrageergebnisse prüfen.'],
    ['Start with your first AI connection', '从第一条 AI 连接开始', '從第一條 AI 連線開始', '最初のAI接続から始める', 'Mit der ersten KI-Verbindung beginnen'],
    ['Connect a cloud provider, bind it to your app, then add a local model if you need one. You can close this guide and continue at any time.', '先连接云端供应商，再为应用绑定模型；需要时再添加本地模型。你可以随时收起指南，稍后继续。', '先連接雲端供應商，再為應用程式綁定模型；需要時再加入本機模型。你可以隨時收起指南，稍後繼續。', 'クラウドプロバイダーに接続し、アプリにモデルを設定します。必要ならローカルモデルを追加します。ガイドはいつでも閉じて再開できます。', 'Cloud-Anbieter verbinden, der App ein Modell zuweisen und bei Bedarf ein lokales Modell ergänzen. Die Anleitung lässt sich jederzeit schließen und fortsetzen.'],
    ['Your app', '你的应用', '你的應用程式', '利用するアプリ', 'Deine App'],
    ['Cloud or local model', '云端或本机模型', '雲端或本機模型', 'クラウドまたはローカルモデル', 'Cloud- oder lokales Modell'],
    ['Request path: your app, Wefto, cloud or local model', '请求路径：你的应用、Wefto、云端或本机模型', '請求路徑：你的應用程式、Wefto、雲端或本機模型', 'リクエスト経路：アプリ、Wefto、クラウドまたはローカルモデル', 'Anfrageweg: App, Wefto, Cloud- oder lokales Modell'],
    ['Connect cloud first', '先接通云端', '先接通雲端', 'まずクラウドに接続', 'Zuerst Cloud verbinden'],
    ['Have your provider API key ready, then choose the cloud model for WorkBuddy or Loomy.', '准备好供应商 API 密钥，再选择 WorkBuddy 或 Loomy 使用的云端模型。', '準備好供應商 API 金鑰，再選擇 WorkBuddy 或 Loomy 使用的雲端模型。', 'プロバイダーのAPIキーを準備し、WorkBuddyまたはLoomyで使うクラウドモデルを選びます。', 'API-Schlüssel des Anbieters bereithalten und ein Cloud-Modell für WorkBuddy oder Loomy wählen.'],
    ['Local models are optional', '本地模型按需添加', '本機模型依需求加入', 'ローカルモデルは任意', 'Lokale Modelle sind optional'],
    ['Ordinary cloud use does not require a model download. Local 1.7B and 4B routing and execution are experimental.', '普通云端使用不需要下载本地模型；本地 1.7B 和 4B 判断与执行属于实验性能力。', '一般雲端使用不需要下載本機模型；本機 1.7B 和 4B 判斷與執行屬於實驗性能力。', '通常のクラウド利用ではモデルのダウンロードは不要です。1.7Bと4Bによる判断と実行は実験的機能です。', 'Für normale Cloud-Nutzung ist kein Modelldownload nötig. Lokales Routing und Ausführung mit 1.7B und 4B sind experimentell.'],
    ['The page connects to local management automatically. If disconnected, check the service and use Refresh now. A management token is different from a provider API key.', '页面会自动连接本机管理。若显示断开，请检查服务并点击“立即刷新”。管理令牌与供应商 API 密钥用途不同。', '頁面會自動連接本機管理。若顯示中斷，請檢查服務並點擊「立即重新整理」。管理權杖與供應商 API 金鑰用途不同。', 'ページはローカル管理に自動接続します。切断時はサービスを確認し「今すぐ更新」を使ってください。管理トークンとプロバイダーのAPIキーは別です。', 'Die Seite verbindet sich automatisch mit der lokalen Verwaltung. Bei Trennung Dienst prüfen und „Jetzt aktualisieren“ nutzen. Verwaltungstoken und Anbieter-API-Schlüssel sind verschieden.'],
    ['Reading does not download models, enable trials or send model requests. Configuration actions remain yours.', '阅读不会下载模型、开启试用或发送模型请求，实际操作由你明确执行。', '閱讀不會下載模型、啟用試用或傳送模型請求，實際操作由你明確執行。', '読むだけではモデルのダウンロード、試用の有効化、モデルへのリクエストは行いません。各操作は自分で実行します。', 'Lesen lädt keine Modelle herunter, aktiviert keine Tests und sendet keine Modellanfragen. Jede Konfigurationsaktion wird ausdrücklich ausgeführt.'],
    ['Add your AI provider', '添加你的 AI 供应商', '新增你的 AI 供應商', 'AIプロバイダーを追加', 'KI-Anbieter hinzufügen'],
    ['A provider supplies the cloud service. In Providers, add the platform you use and save its connection details. Choose the app model in the next chapter.', '供应商提供云端服务。在“供应商”中添加你使用的平台并保存连接信息，下一章再为应用选择模型。', '供應商提供雲端服務。在「供應商」中新增你使用的平台並儲存連線資訊，下一章再為應用程式選擇模型。', 'プロバイダーがクラウドサービスを提供します。「プロバイダー」で利用先を追加して接続情報を保存し、次の章でアプリのモデルを選びます。', 'Der Anbieter stellt den Cloud-Dienst bereit. Unter Anbieter die Plattform hinzufügen und Verbindungsdaten speichern. Das App-Modell folgt im nächsten Kapitel.'],
    ['Provider and platform', '供应商与平台', '供應商與平台', 'プロバイダーとプラットフォーム', 'Anbieter und Plattform'],
    ['Choose your existing provider and confirm the platform or region that issued your key.', '选择你已开通的供应商，并核对密钥所属平台或地区。', '選擇你已開通的供應商，並確認金鑰所屬平台或地區。', '契約済みのプロバイダーを選び、キーが発行されたプラットフォームや地域を確認します。', 'Vorhandenen Anbieter wählen und Plattform oder Region des Schlüssels prüfen.'],
    ['API protocol', 'API 协议', 'API 協定', 'APIプロトコル', 'API-Protokoll'],
    ['Use the protocol supported by that provider. The model name alone does not determine the protocol.', '按供应商支持情况选择协议，不能只根据模型名称判断。', '依供應商支援情況選擇協定，不能只依模型名稱判斷。', 'プロバイダーが対応するプロトコルを選びます。モデル名だけでは判断できません。', 'Ein vom Anbieter unterstütztes Protokoll wählen. Der Modellname allein bestimmt es nicht.'],
    ['API base address', 'API 基础地址', 'API 基礎位址', 'APIベースアドレス', 'API-Basisadresse'],
    ['Copy the base address from the provider console, without /chat/completions or /messages.', '复制供应商控制台给出的基础地址，不附加 /chat/completions 或 /messages。', '複製供應商控制台提供的基礎位址，不附加 /chat/completions 或 /messages。', 'プロバイダーの管理画面からベースアドレスをコピーします。/chat/completionsや/messagesは付けません。', 'Basisadresse aus der Anbieter-Konsole kopieren, ohne /chat/completions oder /messages.'],
    ['Provider API key', '供应商 API 密钥', '供應商 API 金鑰', 'プロバイダーのAPIキー', 'Anbieter-API-Schlüssel'],
    ['Use an API key issued by this provider. Never paste the local management token here.', '填写此供应商签发的 API Key，不要填入本机管理令牌。', '填寫此供應商簽發的 API Key，不要填入本機管理權杖。', 'このプロバイダーが発行したAPIキーを使います。ローカル管理トークンは入力しません。', 'Einen von diesem Anbieter ausgestellten API-Schlüssel verwenden. Hier kein lokales Verwaltungstoken eingeben.'],
    ['Saving a provider stores connection details. It does not prove that your account can use a particular model; verify the selected model when saving an app Profile.', '保存供应商只保存连接信息，不代表账号有某个模型的使用权限；请在保存应用 Profile 时验证所选模型。', '儲存供應商只儲存連線資訊，不代表帳號有某個模型的使用權限；請在儲存應用程式 Profile 時驗證所選模型。', 'プロバイダーの保存は接続情報の保存です。特定モデルの利用権限は保証されないため、アプリのProfile保存時に検証します。', 'Das Speichern hinterlegt Verbindungsdaten. Es bestätigt keine Modellberechtigung. Das gewählte Modell beim Speichern des App-Profils prüfen.'],
    ['Choose a cloud model for your app', '给你的应用选择云端模型', '為你的應用程式選擇雲端模型', 'アプリのクラウドモデルを選ぶ', 'Cloud-Modell für die App wählen'],
    ['In Agents, edit the app Profile and bind your provider and cloud model. Follow the instructions for your app.', '在 Agent 页编辑应用 Profile，绑定供应商与云端模型。请选择你的应用查看接入步骤。', '在 Agent 頁編輯應用程式 Profile，綁定供應商與雲端模型。請選擇你的應用程式查看接入步驟。', 'AgentページでアプリのProfileを編集し、プロバイダーとクラウドモデルを設定します。利用するアプリの手順を確認してください。', 'Unter Agents das App-Profil bearbeiten und Anbieter sowie Cloud-Modell zuweisen. Die Anleitung der eigenen App wählen.'],
    ['Choose app instructions', '选择应用教学', '選擇應用程式教學', 'アプリの説明を選択', 'App-Anleitung wählen'],
    ['Bind a provider and model', '绑定供应商与模型', '綁定供應商與模型', 'プロバイダーとモデルを設定', 'Anbieter und Modell zuweisen'],
    ['Choose your saved provider, then fetch its model list or enter the exact model ID. Published model lists do not verify account access.', '选择已保存的供应商，获取模型列表或填写准确的模型 ID。上游公布的列表不代表账号权限已验证。', '選擇已儲存的供應商，取得模型清單或填寫準確的模型 ID。上游公布的清單不代表帳號權限已驗證。', '保存済みのプロバイダーを選び、モデル一覧を取得するか正確なモデルIDを入力します。公開一覧は利用権限の検証ではありません。', 'Gespeicherten Anbieter wählen und Modelle abrufen oder die genaue Modell-ID eingeben. Eine veröffentlichte Liste bestätigt keine Kontoberechtigung.'],
    ['Verify, save and connect', '验证、保存并接入', '驗證、儲存並接入', '検証・保存・接続', 'Prüfen, speichern und verbinden'],
    ['“Verify and save” sends a small cloud text request and may incur charges. After success, Router saves the Profile and attempts to connect the detected WorkBuddy configuration.', '“验证并保存”会发送少量云端文本，可能产生费用。成功后保存 Profile，并尝试接入检测到的 WorkBuddy 配置。', '「驗證並儲存」會傳送少量雲端文字，可能產生費用。成功後儲存 Profile，並嘗試接入偵測到的 WorkBuddy 設定。', '「検証して保存」は少量のクラウドテキストを送信し、料金が発生する場合があります。成功後にProfileを保存し、検出したWorkBuddy設定への接続を試みます。', '„Prüfen und speichern“ sendet eine kleine Cloud-Textanfrage und kann Kosten verursachen. Danach wird das Profil gespeichert und die erkannte WorkBuddy-Konfiguration verbunden.'],
    ['Use stored credentials first', '先使用已存储凭证', '先使用已儲存憑證', 'まず保存済み認証情報を使う', 'Zunächst gespeicherte Zugangsdaten nutzen'],
    ['Loomy defaults to stored mode: Router uses the provider key you saved. “Verify and save” checks cloud text, saves the Profile and attempts to connect a uniquely detected local Loomy directory.', 'Loomy 默认使用 stored 模式，由 Router 使用已保存的供应商密钥。“验证并保存”会检查云端文本、保存 Profile，并尝试接入唯一可确定的本机 Loomy 目录。', 'Loomy 預設使用 stored 模式，由 Router 使用已儲存的供應商金鑰。「驗證並儲存」會檢查雲端文字、儲存 Profile，並嘗試接入唯一可確定的本機 Loomy 目錄。', 'Loomyの既定値はstoredモードで、Routerが保存したプロバイダーキーを使います。「検証して保存」はクラウドテキストを確認し、Profileを保存して一意に検出できたローカルディレクトリへの接続を試みます。', 'Loomy nutzt standardmäßig stored: Router verwendet den gespeicherten Anbieterschlüssel. „Prüfen und speichern“ prüft Cloud-Text, speichert das Profil und versucht das eindeutig erkannte lokale Loomy-Verzeichnis zu verbinden.'],
    ['Restart the app and start a new session', '重启应用并新建会话', '重新啟動應用程式並建立新會話', 'アプリを再起動し新しいセッションを開始', 'App neu starten und neue Sitzung öffnen'],
    ['Finish existing tasks, restart WorkBuddy and choose the Wefto model. Send your first request in a new session.', '先结束已有任务，再重启 WorkBuddy 并选择 Wefto 对应模型，在新会话中发送第一条请求。', '先結束現有任務，再重新啟動 WorkBuddy 並選擇 Wefto 對應模型，在新會話中傳送第一筆請求。', '実行中の作業を終えてWorkBuddyを再起動し、Weftoのモデルを選びます。新しいセッションから最初のリクエストを送ってください。', 'Laufende Aufgaben beenden, WorkBuddy neu starten und das Wefto-Modell wählen. Die erste Anfrage in einer neuen Sitzung senden.'],
    ['Restart Loomy, choose Wefto and start a new session. Existing sessions and project-level .opencode settings may keep a different model or provider.', '重启 Loomy，选择 Wefto 并新建会话。已有会话及项目级 .opencode 设置可能继续使用其他模型或供应商。', '重新啟動 Loomy，選擇 Wefto 並建立新會話。現有會話及專案層級 .opencode 設定可能繼續使用其他模型或供應商。', 'Loomyを再起動し、Weftoを選んで新しいセッションを開始します。既存セッションやプロジェクトの.opencode設定は別のモデルを使う場合があります。', 'Loomy neu starten, Wefto wählen und eine neue Sitzung öffnen. Bestehende Sitzungen und .opencode-Projekteinstellungen können andere Modelle oder Anbieter verwenden.'],
    ['If automatic connection fails, the saved Profile remains. Close the app and use its setup guide to specify a directory or retry; saving alone is not proof of a host request.', '自动接入失败时 Profile 会保留。请关闭应用后重试，或在接入说明中指定目录；保存成功不等于已验证真实宿主请求。', '自動接入失敗時 Profile 會保留。請關閉應用程式後重試，或在接入說明中指定目錄；儲存成功不等於已驗證真實宿主請求。', '自動接続に失敗してもProfileは保持されます。アプリを終了して再試行するか、接続ガイドでディレクトリを指定します。保存成功だけでは実際のホストリクエストを確認できません。', 'Bei fehlgeschlagener automatischer Verbindung bleibt das Profil erhalten. App schließen und erneut versuchen oder im Einrichtungsleitfaden ein Verzeichnis angeben. Speichern allein bestätigt keine echte Host-Anfrage.'],
    ['Advanced: explicit Loomy forwarding', '进阶：Loomy 显式转发凭证', '進階：Loomy 明確轉送憑證', '詳細：Loomyの明示的な認証転送', 'Erweitert: explizite Loomy-Weiterleitung'],
    ['Only choose forward mode intentionally. It forwards business Authorization supplied by Loomy; stored-key verification and automatic host connection do not apply. Verify from the connected host. The management token must never be used for inference.', '只有明确需要时才选择 forward 模式；它转发 Loomy 提供的业务 Authorization，不适用 stored 密钥验证和自动宿主接入，需从已接入宿主验证。管理令牌不能用于模型推理。', '只有明確需要時才選擇 forward 模式；它轉送 Loomy 提供的業務 Authorization，不適用 stored 金鑰驗證和自動宿主接入，需從已接入宿主驗證。管理權杖不能用於模型推論。', 'forwardモードは意図して選択してください。Loomyの業務Authorizationを転送し、storedキーの検証と自動ホスト接続は適用されません。接続済みホストから検証します。管理トークンは推論に使いません。', 'forward nur bewusst wählen. Dieser Modus leitet Loomys Geschäfts-Authorization weiter; Prüfung gespeicherter Schlüssel und automatische Host-Verbindung gelten nicht. Über den verbundenen Host prüfen. Das Verwaltungstoken darf nicht für Inferenz verwendet werden.'],
    ['Do I need a local model?', '我需要本地模型吗？', '我需要本機模型嗎？', 'ローカルモデルは必要？', 'Brauche ich ein lokales Modell?'],
    ['A local model can judge and answer suitable requests on this computer. Successful local completion may reduce cloud calls; speed, quality and actual savings depend on the device and task.', '本地模型可在本机判断并回答适合的请求。成功在本机完成时，可能减少云端调用；速度、质量与实际节省取决于设备和任务。', '本機模型可在本機判斷並回答適合的請求。成功在本機完成時，可能減少雲端呼叫；速度、品質與實際節省取決於裝置和任務。', 'ローカルモデルは適したリクエストをこの端末で判断・回答できます。ローカルで完了すればクラウド呼び出しを減らせる場合がありますが、速度・品質・節約量は端末と作業次第です。', 'Ein lokales Modell kann passende Anfragen auf diesem Computer beurteilen und beantworten. Erfolgreiche lokale Ausführung kann Cloud-Aufrufe verringern; Tempo, Qualität und Ersparnis hängen von Gerät und Aufgabe ab.'],
    ['Spark-X2.5 · 1.7B / 4B', 'Spark-X2.5 · 1.7B / 4B', 'Spark-X2.5 · 1.7B / 4B', 'Spark-X2.5 · 1.7B / 4B', 'Spark-X2.5 · 1.7B / 4B'],
    ['Both tiers use the same installation and trial flow', '两个档位使用相同的安装与试用流程', '兩個級別使用相同的安裝與試用流程', 'どちらも同じ導入と試用の手順を使います', 'Beide Stufen verwenden denselben Installations- und Testablauf'],
    ['Experimental', '实验性', '實驗性', '実験的', 'Experimentell'],
    ['Choose 1.7B or 4B based on available memory, disk and supported runtime, then confirm with device detection. You can keep using cloud without installing a model.', '按可用内存、磁盘和受支持的运行后端选择 1.7B 或 4B，再用设备检测确认。不安装也可继续使用云端。', '依可用記憶體、磁碟和受支援的執行後端選擇 1.7B 或 4B，再用裝置偵測確認。不安裝也可繼續使用雲端。', '利用可能なメモリ、ディスク、対応ランタイムに応じて1.7Bか4Bを選び、端末検出で確認します。導入せずクラウドを使い続けることもできます。', '1.7B oder 4B nach freiem Speicher, Festplattenplatz und unterstützter Laufzeit wählen und per Geräteprüfung bestätigen. Cloud-Nutzung bleibt ohne Installation möglich.'],
    ['Choose a learning route', '选择学习路线', '選擇學習路線', '学習ルートを選択', 'Lernweg wählen'],
    ['Learn local installation and trial', '了解本地安装与试用', '瞭解本機安裝與試用', 'ローカル導入と試用を学ぶ', 'Lokale Installation und Test kennenlernen'],
    ['Use cloud first', '先使用云端', '先使用雲端', 'まずクラウドを使う', 'Zunächst Cloud nutzen'],
    ['Local trial is not redaction. Redaction requires separate authorization and accepted capability; enabling local routing does not make cloud requests private. Cloud requests still reach your selected provider.', '本地试用不等于脱敏。脱敏需要单独授权及已验收能力；开启本地路由不会让云端请求自动获得隐私保护，云端请求仍发往所选供应商。', '本機試用不等於去識別化。去識別化需要獨立授權及已驗收能力；啟用本機路由不會讓雲端請求自動獲得隱私保護，雲端請求仍傳送至所選供應商。', 'ローカル試用と匿名化は別です。匿名化には別の許可と受け入れ済み機能が必要です。ローカルルーティングを有効にしても、クラウドリクエストは選んだプロバイダーに送られます。', 'Lokaler Test ist keine Anonymisierung. Diese braucht eigene Freigabe und abgenommene Fähigkeiten. Lokales Routing schützt Cloud-Anfragen nicht automatisch; sie gehen weiterhin an den gewählten Anbieter.'],
    ['Understand local trial limits', '了解本地试用限制', '瞭解本機試用限制', 'ローカル試用の制限', 'Grenzen des lokalen Tests verstehen'],
    ['Engineering checks are not quality acceptance. Local answers, cloud-routing decisions and answers after tool calls may fail or time out. Use actual request records to judge results.', '工程检查不等于质量验收。本地回答、转云判断和工具后续回答可能失败或超时；请以真实请求记录确认结果。', '工程檢查不等於品質驗收。本機回答、轉雲判斷和工具後續回答可能失敗或逾時；請以真實請求紀錄確認結果。', '技術的な検査は品質の受け入れ検証ではありません。ローカルの回答、クラウドへの判断、ツール後の回答は失敗やタイムアウトがあり得ます。実際のリクエスト記録で確認してください。', 'Technische Prüfungen sind keine Qualitätsabnahme. Lokale Antworten, Cloud-Entscheidungen und Antworten nach Tool-Aufrufen können fehlschlagen oder auslaufen. Tatsächliche Anfrageprotokolle prüfen.'],
    ['Install, check, then explicitly enable the trial', '先安装、检查，再明确开启试用', '先安裝、檢查，再明確啟用試用', '導入と確認の後に試用を明示的に有効化', 'Installieren, prüfen, dann Test ausdrücklich aktivieren'],
    ['Use Local model to perform these actions in order. Downloaded, loaded, checked and trial-enabled are different states.', '在“本地模型”页按顺序操作。已下载、已加载、已检查和已开启试用是不同状态。', '在「本機模型」頁依序操作。已下載、已載入、已檢查和已啟用試用是不同狀態。', '「ローカルモデル」で順番に操作します。ダウンロード済み、ロード済み、確認済み、試用有効はそれぞれ別の状態です。', 'Unter Lokales Modell diese Schritte nacheinander ausführen. Heruntergeladen, geladen, geprüft und Test aktiviert sind verschiedene Zustände.'],
    ['Detect the device', '检测设备', '偵測裝置', '端末を検出', 'Gerät prüfen'],
    ['Check memory, disk and runtime conditions. A recommendation is not a download or a quality guarantee.', '确认内存、磁盘与运行后端条件。推荐结果不会下载模型，也不是质量保证。', '確認記憶體、磁碟與執行後端條件。建議結果不會下載模型，也不是品質保證。', 'メモリ、ディスク、ランタイム条件を確認します。推奨結果はダウンロードや品質保証ではありません。', 'Speicher, Festplatte und Laufzeit prüfen. Eine Empfehlung lädt nichts herunter und ist keine Qualitätsgarantie.'],
    ['Select 1.7B or 4B', '选择 1.7B 或 4B', '選擇 1.7B 或 4B', '1.7Bか4Bを選択', '1.7B oder 4B auswählen'],
    ['Choose a fixed 1.7B or 4B model. Both use the same download, configuration and capability-check flow.', '选择固定的 1.7B 或 4B 模型。两者使用相同的下载、配置与能力检查流程。', '選擇固定的 1.7B 或 4B 模型。兩者使用相同的下載、設定與能力檢查流程。', '固定の1.7Bか4Bモデルを選びます。どちらも同じダウンロード、設定、機能確認の手順を使います。', 'Ein festes 1.7B- oder 4B-Modell auswählen. Beide verwenden denselben Ablauf für Download, Konfiguration und Fähigkeitsprüfung.'],
    ['Enable the local model', '启用本地模型', '啟用本機模型', 'ローカルモデルを有効化', 'Lokales Modell aktivieren'],
    ['This saves your permission to use local resources; it does not start a download.', '保存允许使用本地资源的意图，不会开始下载。', '儲存允許使用本機資源的意圖，不會開始下載。', 'ローカル資源の利用許可を保存します。ダウンロードは始まりません。', 'Speichert die Erlaubnis zur lokalen Nutzung und startet keinen Download.'],
    ['Download and install the model', '下载并安装模型', '下載並安裝模型', 'モデルをダウンロードして導入', 'Modell herunterladen und installieren'],
    ['Explicitly start the download of the model and required dependencies, and wait for integrity verification.', '明确开始下载模型及必要依赖，等待完整性校验完成。', '明確開始下載模型及必要相依套件，等待完整性驗證完成。', 'モデルと必要な依存関係のダウンロードを開始し、整合性検証の完了を待ちます。', 'Download von Modell und nötigen Abhängigkeiten ausdrücklich starten und Integritätsprüfung abwarten.'],
    ['Load the runtime', '加载运行时', '載入執行階段', 'ランタイムをロード', 'Laufzeit laden'],
    ['Start the installed local runtime and confirm its process status. Installation alone does not load the model.', '启动已安装的本地运行时，确认进程状态。安装完成不等于模型已加载。', '啟動已安裝的本機執行階段，確認程序狀態。安裝完成不等於模型已載入。', '導入済みのローカルランタイムを起動し、プロセス状態を確認します。導入完了だけではモデルはロードされません。', 'Installierte lokale Laufzeit starten und Prozessstatus prüfen. Installation allein lädt das Modell nicht.'],
    ['Run capability checks', '检测能力', '檢查能力', '機能を確認', 'Fähigkeiten prüfen'],
    ['This sends real local check requests. Successful experimental checks make the trial button available. Formal capability may still show unknown: product quality has not been accepted. Inspect judgment and execution results separately, including any errors.', '此操作会发送真实的本地检查请求。实验检查成功后，试用按钮才可点击。正式能力仍可能显示“未知”，表示产品质量尚未验收；请分别查看判断、执行结果及错误。', '此操作會傳送真實的本機檢查請求。實驗檢查成功後，試用按鈕才可點擊。正式能力仍可能顯示「未知」，表示產品品質尚未驗收；請分別查看判斷、執行結果及錯誤。', '実際のローカル検査を送信します。実験用の検査に成功すると試用ボタンが使えます。正式な能力が「不明」でも、品質が未承認という意味です。判断・実行の結果とエラーを別々に確認してください。', 'Sendet echte lokale Prüfanfragen. Erfolgreiche experimentelle Prüfungen geben die Testschaltfläche frei. Die formale Fähigkeit kann weiterhin unbekannt sein: Die Produktqualität ist noch nicht abgenommen. Routing, Ausführung und Fehler getrennt prüfen.'],
    ['Explicitly enable experimental routing and execution', '明确开启实验性判断与执行', '明確啟用實驗性判斷與執行', '実験的な判断と実行を明示的に有効化', 'Experimentelles Routing und Ausführung ausdrücklich aktivieren'],
    ['Only after the required checks pass, click the trial button yourself. You can disable it at any time; neither reading this guide nor installing the model enables it.', '所需检查通过后，由你点击试用按钮开启，也可随时关闭。阅读指南和安装模型均不会自动开启。', '所需檢查通過後，由你點擊試用按鈕啟用，也可隨時關閉。閱讀指南和安裝模型均不會自動啟用。', '必要な検査に合格してから自分で試用ボタンを押します。いつでも無効にできます。読むだけでも導入だけでも有効になりません。', 'Erst nach bestandenen Pflichtprüfungen selbst die Testschaltfläche anklicken. Der Test lässt sich jederzeit deaktivieren. Lesen oder Installation aktiviert ihn nicht.'],
    ['After closing the page or restarting', '关闭页面或重启之后', '關閉頁面或重新啟動之後', 'ページを閉じた後や再起動後', 'Nach Schließen der Seite oder Neustart'],
    ['Closing the page does not stop Router. A service restart does not automatically load the model or rerun capability checks. Saved intent is separate from current effective readiness; check the page again before relying on local use.', '关闭页面不会停止 Router。服务重启不会自动加载模型或重跑能力检查；已保存意图与当前有效状态分开，请再次查看页面确认能否本地使用。', '關閉頁面不會停止 Router。服務重新啟動不會自動載入模型或重跑能力檢查；已儲存意圖與目前有效狀態分開，請再次查看頁面確認能否本機使用。', 'ページを閉じてもRouterは停止しません。サービスの再起動はモデルの自動ロードや検査の再実行を行いません。保存した意図と現在の利用可否は別なので、再度ページで確認してください。', 'Das Schließen der Seite stoppt Router nicht. Ein Dienstneustart lädt das Modell nicht automatisch und wiederholt keine Fähigkeitsprüfung. Gespeicherter Wunsch und aktuelle Bereitschaft sind getrennt; vor lokaler Nutzung erneut prüfen.'],
    ['Skip installation for now', '暂时跳过安装', '暫時略過安裝', '今は導入をスキップ', 'Installation vorerst überspringen'],
    ['Send a request and see where it went', '发一条请求，看看它去了哪里', '傳送一筆請求，看看它去了哪裡', 'リクエストを送り、実行先を確認', 'Anfrage senden und Ziel prüfen'],
    ['After configuration, restart the connected app, choose Wefto and open a new session. Send a simple request there, then return here to inspect actual results.', '完成配置后，重启已接入的应用，选择 Wefto 并新建会话。先在应用中发送一条简单请求，再回来查看实际结果。', '完成設定後，重新啟動已接入的應用程式，選擇 Wefto 並建立新會話。先在應用程式中傳送一筆簡單請求，再回來查看實際結果。', '設定後、接続したアプリを再起動してWeftoを選び、新しいセッションを開きます。簡単なリクエストを送り、ここで実際の結果を確認してください。', 'Nach der Einrichtung die verbundene App neu starten, Wefto wählen und eine neue Sitzung öffnen. Eine einfache Anfrage senden und hier das tatsächliche Ergebnis prüfen.'],
    ['Try: briefly explain what you can help me with.', '可以试试：请用一句话介绍你能帮我做什么。', '可以試試：請用一句話介紹你能幫我做什麼。', '例：何を手伝えるか、一文で教えてください。', 'Beispiel: Erkläre in einem Satz, wobei du mir helfen kannst.'],
    ['Usage → Requests', '用量 → 请求', '用量 → 請求', '使用量 → リクエスト', 'Nutzung → Anfragen'],
    ['Open the request detail for status, actual tokens, latency and internal calls. An error is a result to investigate, not a successful local answer.', '打开请求详情，查看状态、实际 Token、耗时与内部调用。错误需要排查，不能计作成功的本地回答。', '開啟請求詳情，查看狀態、實際 Token、耗時與內部呼叫。錯誤需要排查，不能計作成功的本機回答。', '詳細で状態、実際のトークン、所要時間、内部呼び出しを確認します。エラーは調査対象であり、ローカル回答の成功ではありません。', 'Anfragedetails für Status, tatsächliche Tokens, Dauer und interne Aufrufe öffnen. Ein Fehler muss untersucht werden und ist keine erfolgreiche lokale Antwort.'],
    ['Routing', '路由', '路由', 'ルーティング', 'Routing'],
    ['Confirm the actual cloud or local destination and failure stage. Selecting a route or reading a recommendation does not prove local execution.', '核对实际云端或本地去向以及失败阶段。选择路线或看到推荐结果，都不代表已在本地执行。', '核對實際雲端或本機去向以及失敗階段。選擇路線或看到建議結果，都不代表已在本機執行。', '実際のクラウド・ローカル実行先と失敗段階を確認します。ルートの選択や推奨結果だけではローカル実行の証拠になりません。', 'Tatsächliches Cloud- oder lokales Ziel und Fehlerphase prüfen. Routenwahl oder Empfehlung beweist keine lokale Ausführung.'],
    ['Usage → Overview', '用量 → 概览', '用量 → 概覽', '使用量 → 概要', 'Nutzung → Übersicht'],
    ['Review aggregates within the retained window. Only requests through Wefto are counted. Unreported tokens remain unknown; reference prices and savings are estimates, not bills.', '查看保留窗口内汇总。只统计经过 Wefto 的请求；未报告 Token 为未知，参考价格与节省金额属于估算，不是账单。', '查看保留視窗內彙總。只統計經過 Wefto 的請求；未回報 Token 為未知，參考價格與節省金額屬於估算，不是帳單。', '保持期間内の集計を確認します。Weftoを通ったリクエストだけが対象です。未報告のトークンは不明のままで、参考価格や節約額は請求額ではなく推定です。', 'Summen im Aufbewahrungszeitraum ansehen. Gezählt werden nur Anfragen über Wefto. Nicht gemeldete Tokens bleiben unbekannt; Referenzpreise und Ersparnis sind Schätzungen, keine Rechnungen.'],
    ['Finishing marks this guide as read only. Provider setup, host connection and local trial readiness must be checked in their own pages and real requests.', '完成阅读只标记指南已读。供应商配置、宿主接入与本地试用是否可用，仍需在对应页面及真实请求中验证。', '完成閱讀只標記指南已讀。供應商設定、宿主接入與本機試用是否可用，仍需在對應頁面及真實請求中驗證。', '読み終える操作はガイドの既読だけを記録します。プロバイダー設定、ホスト接続、ローカル試用の可否は各ページと実際のリクエストで確認します。', 'Abschließen markiert nur die Anleitung als gelesen. Anbieterkonfiguration, Host-Verbindung und lokale Testbereitschaft müssen auf ihren Seiten und mit echten Anfragen geprüft werden.'],
    ['Go to Agents', '前往 Agent', '前往 Agent', 'Agentを開く', 'Agents öffnen'],
    ['Go to Providers', '前往供应商', '前往供應商', 'プロバイダーを開く', 'Anbieter öffnen'],
    ['Go to Local model', '前往本地模型', '前往本機模型', 'ローカルモデルを開く', 'Lokales Modell öffnen'],
    ['Go to Requests', '前往请求记录', '前往請求紀錄', 'リクエスト記録を開く', 'Anfrageverlauf öffnen'],
  ]) for (const [language, value] of [['zh', zh], ['zh-TW', tw], ['ja', ja], ['de', de]]) (I18N[language] ||= {})[key] = value;
  translateStatic();

  const key = 'wefto.tutorial.v1';
  const fields = ['version', 'step', 'route', 'app', 'dismissed', 'completed'];
  const defaults = () => ({ version: 1, step: 0, route: 'local', app: 'workbuddy', dismissed: false, completed: false });
  let state = defaults();
  try {
    const saved = JSON.parse(localStorage.getItem(key) ?? localStorage.getItem('api-router.tutorial.v1'));
    if (saved && !Array.isArray(saved) && typeof saved === 'object' &&
        Object.keys(saved).length === fields.length && fields.every((field) => Object.hasOwn(saved, field)) &&
        saved.version === 1 && Number.isInteger(saved.step) && saved.step >= 0 && saved.step <= 5 &&
        ['local', 'cloud'].includes(saved.route) && ['workbuddy', 'loomy'].includes(saved.app) &&
        typeof saved.dismissed === 'boolean' && typeof saved.completed === 'boolean') {
      state = { version: 1, step: saved.step, route: saved.route, app: saved.app, dismissed: saved.dismissed, completed: saved.completed };
      if (state.route === 'cloud' && state.step === 4) state.step = 3;
    }
  } catch {}
  const save = () => { try { localStorage.setItem(key, JSON.stringify(state)); } catch {} };
  const modal = $('#tutorialModal');
  if (!modal) return;
  const dialog = modal.querySelector('.tutorial-dialog');
  const content = $('#tutorialContent');
  const reading = modal.querySelector('.tutorial-reading');
  const names = ['Meet Wefto', 'Connect a provider', 'Set up your app', 'Choose your local route', 'Install and try', 'Your first request'];
  let opened = false;
  let returnFocus = null;
  let isolated = [];
  const visibleSteps = () => state.route === 'cloud' ? [0, 1, 2, 3, 5] : [0, 1, 2, 3, 4, 5];
  const paragraph = (copy, className = 'tutorial-lead') => el('p', className, t(copy));
  const note = (copy, warning = false) => el('div', 'tutorial-note' + (warning ? ' tutorial-warning' : ''), t(copy));
  const focus = (node) => node?.focus({ preventScroll: true });
  function button(copy, className, marker, action) {
    const node = el('button', className, t(copy));
    node.type = 'button';
    if (marker) node.dataset.tutorialFocus = marker;
    node.onclick = action;
    return node;
  }
  function sequence(rows) {
    const list = el('ol', 'tutorial-sequence');
    for (const [index, [title, copy]] of rows.entries()) {
      const item = el('li');
      const number = el('span', 'tutorial-number', String(index + 1));
      number.setAttribute('aria-hidden', 'true');
      const text = el('div');
      text.append(el('strong', '', t(title)), paragraph(copy, 'tutorial-inlinehint'));
      item.append(number, text);
      list.append(item);
    }
    return list;
  }
  function details(title, copy) {
    const node = el('details', 'tutorial-details');
    const summary = el('summary', '', t(title));
    summary.dataset.tutorialFocus = 'details-' + state.step;
    node.append(summary, paragraph(copy, 'tutorial-inlinehint'));
    return node;
  }
  function choices(label, entries, selected, choose) {
    const group = el('div', 'tutorial-choices');
    group.setAttribute('role', 'group');
    group.setAttribute('aria-label', t(label));
    for (const [id, copy] of entries) {
      const choice = button(copy, 'tutorial-choice', id, () => { choose(id); save(); render(); });
      choice.dataset[label === 'Choose app instructions' ? 'app' : 'route'] = id;
      choice.setAttribute('aria-pressed', String(selected === id));
      group.append(choice);
    }
    return group;
  }
  function lesson() {
    const fragment = document.createDocumentFragment();
    if (state.step === 0) {
      fragment.append(paragraph('Connect a cloud provider, bind it to your app, then add a local model if you need one. You can close this guide and continue at any time.'));
      const flow = el('div', 'tutorial-flow');
      flow.setAttribute('role', 'img');
      flow.setAttribute('aria-label', t('Request path: your app, Wefto, cloud or local model'));
      for (const [index, name] of ['Your app', 'Wefto', 'Cloud or local model'].entries()) {
        if (index) { const arrow = el('span', 'tutorial-arrow', '→'); arrow.setAttribute('aria-hidden', 'true'); flow.append(arrow); }
        flow.append(el('div', 'tutorial-node' + (index === 1 ? ' tutorial-node-router' : ''), t(name)));
      }
      fragment.append(flow);
      const features = el('div', 'tutorial-features');
      for (const [title, copy] of [
        ['Connect cloud first', 'Have your provider API key ready, then choose the cloud model for WorkBuddy or Loomy.'],
        ['Local models are optional', 'Ordinary cloud use does not require a model download. Local 1.7B and 4B routing and execution are experimental.'],
      ]) {
        const feature = el('div', 'tutorial-feature');
        feature.append(el('strong', '', t(title)), paragraph(copy, 'tutorial-inlinehint'));
        features.append(feature);
      }
      fragment.append(features, note('The page connects to local management automatically. If disconnected, check the service and use Refresh now. A management token is different from a provider API key.'), note('Reading does not download models, enable trials or send model requests. Configuration actions remain yours.'));
    } else if (state.step === 1) {
      fragment.append(paragraph('A provider supplies the cloud service. In Providers, add the platform you use and save its connection details. Choose the app model in the next chapter.'));
      const fields = el('dl', 'tutorial-fields');
      for (const [title, copy] of [
        ['Provider and platform', 'Choose your existing provider and confirm the platform or region that issued your key.'],
        ['API protocol', 'Use the protocol supported by that provider. The model name alone does not determine the protocol.'],
        ['API base address', 'Copy the base address from the provider console, without /chat/completions or /messages.'],
        ['Provider API key', 'Use an API key issued by this provider. Never paste the local management token here.'],
      ]) {
        const row = el('div', 'tutorial-field');
        row.append(el('dt', 'tutorial-field-name', t(title)), el('dd', 'tutorial-field-value', t(copy)));
        fields.append(row);
      }
      fragment.append(fields, note('Saving a provider stores connection details. It does not prove that your account can use a particular model; verify the selected model when saving an app Profile.'));
    } else if (state.step === 2) {
      fragment.append(paragraph('In Agents, edit the app Profile and bind your provider and cloud model. Follow the instructions for your app.'), choices('Choose app instructions', [['workbuddy', 'WorkBuddy'], ['loomy', 'Loomy']], state.app, (id) => { state.app = id; }));
      fragment.append(sequence([
        ['Bind a provider and model', 'Choose your saved provider, then fetch its model list or enter the exact model ID. Published model lists do not verify account access.'],
        state.app === 'loomy'
          ? ['Use stored credentials first', 'Loomy defaults to stored mode: Router uses the provider key you saved. “Verify and save” checks cloud text, saves the Profile and attempts to connect a uniquely detected local Loomy directory.']
          : ['Verify, save and connect', '“Verify and save” sends a small cloud text request and may incur charges. After success, Router saves the Profile and attempts to connect the detected WorkBuddy configuration.'],
        ['Restart the app and start a new session', state.app === 'loomy'
          ? 'Restart Loomy, choose Wefto and start a new session. Existing sessions and project-level .opencode settings may keep a different model or provider.'
          : 'Finish existing tasks, restart WorkBuddy and choose the Wefto model. Send your first request in a new session.'],
      ]), note('If automatic connection fails, the saved Profile remains. Close the app and use its setup guide to specify a directory or retry; saving alone is not proof of a host request.'));
      if (state.app === 'loomy') fragment.append(note('Verification sends a small text request and may incur charges.'), details('Advanced: explicit Loomy forwarding', 'Only choose forward mode intentionally. It forwards business Authorization supplied by Loomy; stored-key verification and automatic host connection do not apply. Verify from the connected host. The management token must never be used for inference.'));
    } else if (state.step === 3) {
      fragment.append(paragraph('A local model can judge and answer suitable requests on this computer. Successful local completion may reduce cloud calls; speed, quality and actual savings depend on the device and task.'));
      const model = el('div', 'tutorial-model');
      const label = el('div', 'tutorial-model-label');
      label.append(el('strong', '', t('Spark-X2.5 · 1.7B / 4B')), paragraph('Both tiers use the same installation and trial flow', 'tutorial-inlinehint'));
      model.append(label, el('span', 'tutorial-tag', t('Experimental')));
      fragment.append(model, paragraph('Choose 1.7B or 4B based on available memory, disk and supported runtime, then confirm with device detection. You can keep using cloud without installing a model.', 'tutorial-inlinehint'), choices('Choose a learning route', [['local', 'Learn local installation and trial'], ['cloud', 'Use cloud first']], state.route, (id) => { state.route = id; }), note('Local trial is not redaction. Redaction requires separate authorization and accepted capability; enabling local routing does not make cloud requests private. Cloud requests still reach your selected provider.', true), details('Understand local trial limits', 'Engineering checks are not quality acceptance. Local answers, cloud-routing decisions and answers after tool calls may fail or time out. Use actual request records to judge results.'));
    } else if (state.step === 4) {
      fragment.append(paragraph('Use Local model to perform these actions in order. Downloaded, loaded, checked and trial-enabled are different states.'), sequence([
        ['Detect the device', 'Check memory, disk and runtime conditions. A recommendation is not a download or a quality guarantee.'],
        ['Select 1.7B or 4B', 'Choose a fixed 1.7B or 4B model. Both use the same download, configuration and capability-check flow.'],
        ['Enable the local model', 'This saves your permission to use local resources; it does not start a download.'],
        ['Download and install the model', 'Explicitly start the download of the model and required dependencies, and wait for integrity verification.'],
        ['Load the runtime', 'Start the installed local runtime and confirm its process status. Installation alone does not load the model.'],
        ['Run capability checks', 'This sends real local check requests. Successful experimental checks make the trial button available. Formal capability may still show unknown: product quality has not been accepted. Inspect judgment and execution results separately, including any errors.'],
        ['Explicitly enable experimental routing and execution', 'Only after the required checks pass, click the trial button yourself. You can disable it at any time; neither reading this guide nor installing the model enables it.'],
      ]), details('After closing the page or restarting', 'Closing the page does not stop Router. A service restart does not automatically load the model or rerun capability checks. Saved intent is separate from current effective readiness; check the page again before relying on local use.'), button('Skip installation for now', 'text tutorial-skip', 'skip-install', () => { state.route = 'cloud'; setStep(5); }));
    } else {
      fragment.append(paragraph('After configuration, restart the connected app, choose Wefto and open a new session. Send a simple request there, then return here to inspect actual results.'), el('blockquote', 'tutorial-quote', t('Try: briefly explain what you can help me with.')));
      const rows = el('div', 'tutorial-resultrows');
      for (const [title, copy] of [
        ['Usage → Requests', 'Open the request detail for status, actual tokens, latency and internal calls. An error is a result to investigate, not a successful local answer.'],
        ['Routing', 'Confirm the actual cloud or local destination and failure stage. Selecting a route or reading a recommendation does not prove local execution.'],
        ['Usage → Overview', 'Review aggregates within the retained window. Only requests through Wefto are counted. Unreported tokens remain unknown; reference prices and savings are estimates, not bills.'],
      ]) {
        const row = el('div', 'tutorial-resultrow');
        row.append(el('strong', '', t(title)), paragraph(copy, 'tutorial-inlinehint'));
        rows.append(row);
      }
      fragment.append(rows, note('Finishing marks this guide as read only. Provider setup, host connection and local trial readiness must be checked in their own pages and real requests.'));
    }
    return fragment;
  }
  const titles = ['Start with your first AI connection', 'Add your AI provider', 'Choose a cloud model for your app', 'Do I need a local model?', 'Install, check, then explicitly enable the trial', 'Send a request and see where it went'];
  function render() {
    const active = document.activeElement;
    const marker = modal.contains(active) ? active.dataset.tutorialFocus : null;
    const scroll = reading.scrollTop;
    $('#tutorialTitle').textContent = t(titles[state.step]);
    $('#tutorialChapter').textContent = t(names[state.step]);
    $('#tutorialSteps').setAttribute('aria-label', t('Tutorial chapters'));
    $('#tutorialSteps').replaceChildren(...visibleSteps().map((step) => {
      const node = button(names[step], 'tutorial-step', 'step-' + step, () => setStep(step));
      node.dataset.step = String(step);
      node.replaceChildren(el('span', 'tutorial-count', String(step + 1)), el('span', 'tutorial-step-label', t(names[step])));
      node.setAttribute('aria-label', t('Chapter {number}: {name}', { number: step + 1, name: t(names[step]) }));
      if (step === state.step) node.setAttribute('aria-current', 'step');
      return node;
    }));
    content.replaceChildren(lesson());
    reading.scrollTop = scroll;
    const steps = visibleSteps();
    $('#tutorialProgress').textContent = t('Reading progress {current} / {total}', { current: steps.indexOf(state.step) + 1, total: steps.length });
    $('#tutorialClose').setAttribute('aria-label', t('Close guide'));
    $('#tutorialClose').setAttribute('title', t('Close guide'));
    $('#tutorialDismiss').textContent = t('Skip for now');
    $('#tutorialBack').textContent = t('Previous step');
    $('#tutorialBack').disabled = state.step === 0;
    $('#tutorialNext').textContent = t(state.step === 0 ? 'Start learning' : state.step === 5 ? 'Finish reading' : state.step === 3 && state.route === 'cloud' ? 'Skip local installation and continue' : 'Next step');
    $('#tutorialGo').textContent = t(state.step === 1 ? 'Go to Providers' : state.step === 3 || state.step === 4 ? 'Go to Local model' : state.step === 5 ? 'Go to Requests' : 'Go to Agents');
    for (const id of ['tutorialOpen', 'tutorialSettings']) {
      const node = $('#' + id);
      if (node) { node.textContent = t('Usage guide'); node.setAttribute('aria-label', t('Usage guide')); }
    }
    $('#tutorialResume').hidden = state.completed;
    $('#tutorialContinue').textContent = t(state.completed ? 'Read again' : 'Continue reading');
    $('#tutorialSummary').textContent = state.completed ? t('Guide read. Check actual settings and request results to confirm readiness.') : t('Continue from “{chapter}”. Reading progress is separate from configuration.', { chapter: t(names[state.step]) });
    if (opened && marker) {
      const replacement = [...modal.querySelectorAll('[data-tutorial-focus]')].find((node) => node.dataset.tutorialFocus === marker);
      focus(replacement || $('#tutorialNext'));
    } else if (opened && modal.contains(active) && (!active.isConnected || active.disabled)) focus($('#tutorialNext'));
  }
  function setStep(step) {
    state.step = step;
    save();
    render();
    reading.scrollTop = 0;
  }
  function isolate() {
    isolated = [...document.querySelectorAll('body > header, body > aside, body > main, body > footer, .modal')]
      .filter((node) => node !== modal && !node.contains(modal) && !modal.contains(node))
      .map((node) => ({ node, inert: node.inert, attribute: node.hasAttribute('inert') }));
    for (const { node } of isolated) node.inert = true;
  }
  function restore() {
    for (const { node, inert, attribute } of isolated) {
      node.inert = inert;
      if (attribute) node.setAttribute('inert', '');
      else if (!inert) node.removeAttribute('inert');
    }
    isolated = [];
  }
  function open(event) {
    if (opened) return;
    // WebKit does not always focus a button when it is clicked. Remember
    // the actual entry button so closing the guide restores keyboard focus.
    returnFocus = event?.currentTarget instanceof HTMLElement ? event.currentTarget : document.activeElement;
    opened = true;
    // Remember the first display immediately so a refresh never forces it open again.
    state.dismissed = true;
    save();
    modal.hidden = false;
    isolate();
    render();
    focus($('#tutorialNext'));
  }
  function close() {
    if (!opened) return;
    state.dismissed = true;
    save();
    opened = false;
    modal.hidden = true;
    restore();
    render();
    if (returnFocus?.isConnected && !returnFocus.closest('[inert]') && returnFocus.getClientRects().length) focus(returnFocus);
    else focus($('#tutorialOpen'));
    returnFocus = null;
  }
  for (const id of ['tutorialOpen', 'tutorialSettings', 'tutorialContinue']) $('#' + id)?.addEventListener('click', open);
  $('#tutorialClose').addEventListener('click', close);
  $('#tutorialDismiss').addEventListener('click', close);
  $('#tutorialBack').addEventListener('click', () => { const steps = visibleSteps(); setStep(steps[Math.max(0, steps.indexOf(state.step) - 1)]); });
  $('#tutorialNext').addEventListener('click', () => {
    if (state.step === 5) { state.completed = true; close(); return; }
    const steps = visibleSteps();
    setStep(steps[steps.indexOf(state.step) + 1]);
  });
  $('#tutorialGo').addEventListener('click', () => {
    const step = state.step;
    close();
    // A guide link is navigation only. Opening the local-model page from the
    // guide must not start its first-run hardware probe as a side effect.
    if (step === 3 || step === 4) window.routerLocalSkipDetectionOnce = true;
    show(step === 1 ? 'providers' : step === 3 || step === 4 ? 'local-model' : step === 5 ? 'usage' : 'agents');
    if (step === 5) $('#usageTab-requests')?.click();
  });
  document.addEventListener('keydown', (event) => {
    if (!opened) return;
    if (event.key === 'Escape') { event.preventDefault(); event.stopImmediatePropagation(); close(); return; }
    if (event.key !== 'Tab') return;
    const targets = [...dialog.querySelectorAll('button:not(:disabled), a[href], input:not(:disabled), textarea:not(:disabled), summary, [tabindex]:not([tabindex="-1"])')]
      .filter((node) => !node.closest('[hidden], [inert]') && node.getClientRects().length && getComputedStyle(node).visibility !== 'hidden');
    if (!targets.length) { event.preventDefault(); focus(dialog); return; }
    // Safari's keyboard settings can skip buttons in its default Tab order.
    // Choose every modal target explicitly; keyboard focus may scroll the
    // reading pane normally, while entry and exit keep preventScroll.
    const current = targets.indexOf(document.activeElement);
    const next = current < 0 ? (event.shiftKey ? targets.length - 1 : 0)
      : (current + (event.shiftKey ? -1 : 1) + targets.length) % targets.length;
    event.preventDefault();
    targets[next].focus();
  }, true);
  window.routerTutorialOpen = open;
  window.routerTutorialRender = render;
  render();
  if (!state.dismissed && !state.completed) open();
})();
