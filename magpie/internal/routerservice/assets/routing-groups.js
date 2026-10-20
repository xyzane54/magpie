// Routing Groups view, i18n registration, member metadata resolution, and list rendering for API-Router.
(function () {
  var RG_I18N = {
    zh: {
      'Routing': '路由',
      'Refresh': '刷新',
      '＋ New routing group': '＋ 新建路由组',
      'New group': '新建组',
      'Edit group': '编辑组',
      'New routing group': '新建路由组',
      'Edit routing group': '编辑路由组',
      'Orchestrate local on-device models and cloud provider models into unified group/<id> virtual models for external agents and clients.': '将本地端侧模型与云端服务商模型编排为统一的 group/<id> 虚拟模型，供外部 Agent 与客户端调用。',
      'Custom routing groups': '自定义路由组',
      'Auto-discovered same-name groups': '自动发现的同名路由组',
      'Models shared across multiple providers or local runtime are automatically grouped. Pin or edit to customize strategy.': '跨多个服务商或本地运行时共有的同名模型会自动归组。点击固定或编辑即可自定义路由策略。',
      'No custom routing groups yet. Click "＋ New routing group" to create one.': '暂无自定义路由组。点击右上角“＋ 新建路由组”即可创建。',
      'Auto same-name': '自动同名组',
      'Local': '本地端侧',
      'Local on-device': '本地端侧',
      'Local runtime': '本地端侧',
      'Group': '路由组',
      'Routing group': '路由组',
      'Routing groups': '路由组',
      'Cloud': '云端',
      'Pin as custom': '固定为自定义',
      'Edit': '编辑',
      'Delete': '删除',
      'Cancel': '取消',
      'Save': '保存',
      'Copied ': '已复制 ',
      'Routing group saved.': '路由组已保存。',
      'Routing group deleted.': '路由组已删除。',
      'Delete routing group "{1}"?': '确定删除路由组“{1}”吗？',
      'Group name is required.': '请填写路由组名称。',
      'Please add at least one member model or pattern.': '请至少添加一个成员模型或匹配模式。',
      'Active model switched to ': '当前生效模型已切换为 ',
      'Click to set as active model in manual mode': '手动模式下点击切换为当前生效模型',
      'Click to copy model ID': '点击复制调用标识',
      'Name': '名称',
      'e.g. Opus fallback': '例如：Opus 多家备份',
      'Name in Agents': 'Agent 列表里的名称',
      '· routing group': '· 路由组',
      'Agent call ID: ': 'Agent 调用标识：',
      'Models': '模型',
      'Add model': '添加模型',
      '＋ Add model': '＋ 添加模型',
      'First model defines the group\'s capabilities; tried top-to-bottom in In-order mode.': '排第一的模型决定本组对外的能力；按顺序模式下从上往下尝试。',
      'Patterns': '模式',
      'Add pattern': '添加模式',
      'e.g. openrouter/*:free or re:^claude-': '例如 openrouter/*:free 或 re:^claude-',
      'e.g. openrouter/*:free or re:…. Newly discovered matching models auto-join after manual picks.': '例如 openrouter/*:free 或 re:…。新出现的匹配模型会自动加入，排在手动选择的模型后面。',
      'Model list': '模型列表',
      'Keep members listed': '也列出成员模型',
      'Group only': '只列出本组',
      'Member models still appear individually in agents\' model pickers.': '成员模型仍各自出现在 Agent 的模型列表中。',
      'Member models are hidden from model pickers; only this group is listed.': '不在模型列表里单独列出这些成员模型，只展示本路由组。',
      'Smart': '智能',
      'Smart Hybrid': '智能 · 本地+云端',
      'In order': '按顺序',
      'Ordered': '按顺序',
      'Take turns': '轮流',
      'Least used': '最少使用',
      'Drain reset': '重置前用完',
      'Weighted': '按权重',
      'Manual': '手动',
      'Picks the healthiest, fastest, best-fit model for each request, prioritizing local on-device models when suitable.': '综合速度、健康度、配额与任务复杂度自动挑选最合适的模型（简单任务优先走本地端侧，复杂任务走云端）。',
      'Always tries the first model; falls through on error.': '总是先用排第一的模型，失败或限流时按顺序往下兜底。',
      'Rotates requests evenly across healthy models.': '在健康的成员模型之间轮流分配请求。',
      'Sends the next request to whichever model has served the fewest.': '优先把下一个请求发给近期调用次数最少的成员模型。',
      'Spends down the member whose quota window resets soonest, so paid quota doesn\'t expire unused.': '优先消耗配额重置时间最早的成员，避免已付费额度过期浪费。',
      'Splits traffic by the weights set on each model.': '按照每个成员模型设置的权重比例分配流量。',
      'Uses only the model you\'ve ticked above; switch anytime with one click.': '始终使用上方勾选的那个模型；随时可一键切换。',
      'On limit': '被限流时',
      'Keep position': '留在原位',
      'Move to end': '排到最后',
      'Next round tries the first model again once its cooldown ends.': '休息结束后，下一轮继续先试排第一的模型。',
      'Rate-limited model rotates to the bottom so later requests skip the wait.': '被限流的模型移到队尾，后续请求直接跳过它。',
      'Slow stream': '迟迟不出字',
      'Wait': '一直等',
      '30s': '30 秒',
      '1 min': '1 分钟',
      '2 min': '2 分钟',
      's': '秒',
      'If upstream connects but emits no token within this time — or stalls mid-stream — retry the next member.': '上游已接通但超过该时间仍未吐出第一个字，或流式输出中途卡住超过该时间，则换下一个成员重试。',
      'When all busy': '成员全都忙',
      'Off': '关闭',
      '4 min': '4 分钟',
      'min': '分钟',
      'When every member is cooling down, wait for the soonest one and keep retrying within this window before failing.': '所有成员都在休息时，先等最快恢复的那个，并在这段时间内持续重试，超时仍无可用才报错。',
      'Context': '上下文',
      'Largest member': '最大模型的',
      'Smallest member': '最小模型的',
      'tokens': 'tokens',
      'Advertises the largest member\'s context window so long chats aren\'t compacted early.': '按本组上下文最大的成员对外报告，避免长对话过早被压缩。',
      'Advertises the smallest member\'s context window so every fallback fits.': '按本组上下文最小的成员对外报告，确保切换到任意兜底模型都不会超长。',
      'Advertises this custom context window to agents.': '对外统一报告该自定义上下文窗口大小。',
      'Session stays on': '会话保持',
      'Auto': '自动',
      'Whole session': '整段会话',
      'Within turn': '一轮之内',
      'Pin': '固定',
      'Stays on the first chosen model to keep prompt cache warm across turns.': '保持在首次选中的模型上，保证多轮对话的提示词缓存命中。',
      'Stays on the same model for the entire conversation session.': '整段对话期间固定使用同一个成员模型，直到会话结束。',
      'Stays on the same model within a single agent turn (tool-call loop).': '在同一轮 Agent 工具调用循环内保持使用同一个模型。',
      'Picks freshly on every request without session stickiness.': '每次请求都重新根据路由策略挑选模型，不做会话保持。',
      'Pins the session strictly to the first chosen model; does not fail over.': '严格固定在首次选中的模型上。',
      'min idle': '分钟空闲释放',
      'Rules': '规则',
      'Add rule': '添加规则',
      'Matched top-to-bottom; the first matching rule routes to its target model, otherwise the group\'s default routing applies.': '按顺序从上往下匹配，命中第一条后走它指定的模型；都不命中时走本组默认路由。',
      'When': '当',
      'Prompt matches': '提示词匹配',
      'System matches': '系统提示匹配',
      'Model name': '模型名称',
      'Has tools': '包含工具调用',
      'Has vision': '包含图片输入',
      'Context >': '上下文大于',
      'regex or keyword': '正则或关键词',
      'route to': '走',
      'Remove': '移除',
      'Reasoning': '推理强度',
      'As requested': '按请求',
      'Per turn': '每轮判断',
      'Keeps whatever reasoning effort the client request asked for.': '保留客户端请求里自带的推理强度设置。',
      'Adjusts reasoning effort per turn based on prompt complexity.': '根据每轮提示词的复杂程度自动调整推理强度。',
      'Forces reasoning effort to ': '强制将推理强度设为 ',
      'Thinking levels': '推理档位',
      'Intersection': '成员共有',
      'Exposes thinking levels supported by all members (excess levels auto-downgrade on fallback).': '对外暴露所有成员都支持的推理档位（兜底到某个成员时，超出的档位会自动降级为该成员最高档）。',
      'Filter…': '筛选…',
      'All models': '全部模型',
      'Favorites': '收藏',
      'No matching models': '没有匹配的模型',
      'in ': '已在 ',
      'models': '个模型',
      'Active': '当前生效',
      'Set active': '设为生效',
      'Drag to reorder': '拖拽调整顺序',
      'Move up': '上移',
      'Move down': '下移',
      'Default': '默认',
      'invalid routing group payload': '路由组数据格式无效',
      'group name is required': '请输入路由组名称',
      'unsupported routing strategy': '不支持的路由策略',
      'invalid timeout or patience setting': '超时等待设置无效',
      'group weight must be between 1 and 100': '成员权重必须在 1 到 100 之间',
      'routing rule target is required': '请选择路由规则的目标模型',
      'routing group cycle detected': '检测到路由组循环引用',
      'routing group not found': '未找到该路由组'
    }
  };

  if (typeof I18N !== 'undefined') {
    Object.keys(RG_I18N).forEach(function (lang) {
      if (!I18N[lang]) I18N[lang] = {};
      Object.assign(I18N[lang], RG_I18N[lang]);
    });
    if (typeof setLocale === 'function') {
      setLocale(typeof locale === 'string' && locale ? locale : 'zh');
    }
  }

  function tr(key) {
    if (typeof t === 'function') return t(key);
    var lang = typeof locale === 'string' ? locale : 'zh';
    return (RG_I18N[lang] && RG_I18N[lang][key]) || key;
  }

  function esc(val) {
    if (typeof escapeHTML === 'function') return escapeHTML(String(val == null ? '' : val));
    return String(val == null ? '' : val).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  var VENDOR_ICONS = [
    { test: /claude|anthropic/i, icon: 'claude-color.svg' },
    { test: /deepseek/i, icon: 'deepseek-color.svg' },
    { test: /qwen|tongyi|dashscope|bailian|alibaba/i, icon: 'qwen-color.svg' },
    { test: /gemini|google|vertex/i, icon: 'gemini-color.svg' },
    { test: /gpt-|o1|o3|o4|chatgpt|codex|openai/i, icon: 'openai.svg' },
    { test: /glm|zhipu|bigmodel|chatglm/i, icon: 'glm-color.svg' },
    { test: /kimi|moonshot/i, icon: 'kimi-color.svg' },
    { test: /minimax|abab/i, icon: 'minimax-color.svg' },
    { test: /doubao|volces|ark|bytedance/i, icon: 'doubao-color.svg' },
    { test: /grok|xai/i, icon: 'grok.svg' },
    { test: /mistral|codestral|pixtral/i, icon: 'mistral-color.svg' },
    { test: /llama|meta/i, icon: 'meta-color.svg' },
    { test: /gemma/i, icon: 'gemma-color.svg' },
    { test: /hunyuan|tencent/i, icon: 'hunyuan-color.svg' },
    { test: /spark|iflytek|xinghuo/i, icon: 'spark-color.svg' },
    { test: /ernie|baidu|qianfan/i, icon: 'wenxin-color.svg' },
    { test: /step|stepfun/i, icon: 'stepfun-color.svg' },
    { test: /yi-|lingyi/i, icon: 'yi-color.svg' },
    { test: /baichuan/i, icon: 'baichuan-color.svg' },
    { test: /internlm/i, icon: 'internlm-color.svg' },
    { test: /cohere|command-r/i, icon: 'cohere-color.svg' },
    { test: /perplexity|sonar/i, icon: 'perplexity-color.svg' },
    { test: /openrouter/i, icon: 'openrouter.svg' },
    { test: /silicon|siliconflow/i, icon: 'siliconcloud-color.svg' },
    { test: /together/i, icon: 'together-color.svg' },
    { test: /fireworks/i, icon: 'fireworks-color.svg' },
    { test: /groq/i, icon: 'groq.svg' },
    { test: /cerebras/i, icon: 'cerebras-color.svg' },
    { test: /ollama/i, icon: 'ollama.svg' },
    { test: /vllm/i, icon: 'vllm-color.svg' },
    { test: /lmstudio/i, icon: 'lmstudio.svg' },
    { test: /nvidia|nim/i, icon: 'nvidia-color.svg' },
    { test: /azure/i, icon: 'azureai-color.svg' },
    { test: /bedrock|aws/i, icon: 'bedrock-color.svg' },
    { test: /huggingface/i, icon: 'huggingface-color.svg' },
    { test: /modelscope/i, icon: 'modelscope-color.svg' },
    { test: /github|copilot/i, icon: 'githubcopilot.svg' }
  ];

  function resolveIcon(text) {
    var s = String(text || '');
    for (var i = 0; i < VENDOR_ICONS.length; i++) {
      if (VENDOR_ICONS[i].test.test(s)) return '/icons/' + VENDOR_ICONS[i].icon;
    }
    return '/icons/favicon.svg';
  }

  var STRATEGIES = [
    { id: 'smart_hybrid', label: 'Smart', cardLabel: 'Smart Hybrid', desc: 'Picks the healthiest, fastest, best-fit model for each request, prioritizing local on-device models when suitable.' },
    { id: 'order', label: 'In order', cardLabel: 'In order', desc: 'Always tries the first model; falls through on error.' },
    { id: 'round-robin', label: 'Take turns', cardLabel: 'Take turns', desc: 'Rotates requests evenly across healthy models.' },
    { id: 'least-used', label: 'Least used', cardLabel: 'Least used', desc: 'Sends the next request to whichever model has served the fewest.' },
    { id: 'drain-reset', label: 'Drain reset', cardLabel: 'Drain reset', desc: 'Spends down the member whose quota window resets soonest, so paid quota doesn\'t expire unused.' },
    { id: 'weighted', label: 'Weighted', cardLabel: 'Weighted', desc: 'Splits traffic by the weights set on each model.' },
    { id: 'manual', label: 'Manual', cardLabel: 'Manual', desc: 'Uses only the model you\'ve ticked above; switch anytime with one click.' }
  ];

  function normalizeStrategy(raw) {
    var s = String(raw || 'smart_hybrid').trim().toLowerCase();
    if (s === 'ordered') return 'order';
    if (s === 'weight') return 'weighted';
    if (s === 'smart' || s === 'smart_split') return 'smart_hybrid';
    if (s === 'rotate' || s === 'round_robin') return 'round-robin';
    if (s === 'usage' || s === 'least' || s === 'least_used') return 'least-used';
    if (s === 'pace' || s === 'drain_reset') return 'drain-reset';
    return s;
  }

  function strategyMeta(id) {
    var norm = normalizeStrategy(id);
    for (var i = 0; i < STRATEGIES.length; i++) {
      if (STRATEGIES[i].id === norm) return STRATEGIES[i];
    }
    return STRATEGIES[0];
  }

  var state = {
    groups: [],
    providers: [],
    localModels: [],
    favorites: {},
    editingId: '',
    isNew: true
  };

  try {
    var savedFavs = localStorage.getItem('apiRouter.modelFavorites');
    if (savedFavs) state.favorites = JSON.parse(savedFavs) || {};
  } catch (_) {}

  function toggleFavorite(ref) {
    if (state.favorites[ref]) delete state.favorites[ref];
    else state.favorites[ref] = true;
    try {
      localStorage.setItem('apiRouter.modelFavorites', JSON.stringify(state.favorites));
    } catch (_) {}
  }

  function findProvider(idOrName) {
    var key = String(idOrName || '').trim();
    if (!key) return null;
    for (var i = 0; i < state.providers.length; i++) {
      var p = state.providers[i];
      if (p.id === key) return p;
    }
    var lower = key.toLowerCase();
    for (var j = 0; j < state.providers.length; j++) {
      var p2 = state.providers[j];
      if (String(p2.name || '').trim().toLowerCase() === lower || String(p2.preset || '').trim().toLowerCase() === lower) {
        return p2;
      }
    }
    return null;
  }

  function providerIcon(p) {
    if (!p) return '/icons/favicon.svg';
    return resolveIcon([p.name, p.preset, p.baseURL, (p.discoveredModels || [])[0] || ''].join(' '));
  }

  function describeMember(ref) {
    var raw = String(ref || '').trim();
    var slash = raw.indexOf('/');
    var prefix = slash > 0 ? raw.slice(0, slash) : 'cloud';
    var rest = slash > 0 ? raw.slice(slash + 1) : raw;

    if (prefix === 'local') {
      return {
        ref: raw,
        kind: 'local',
        providerId: 'local',
        providerName: tr('Local on-device'),
        modelName: rest,
        icon: resolveIcon(rest || 'ollama'),
        contextBadge: ''
      };
    }
    if (prefix === 'group') {
      var gMatch = null;
      for (var i = 0; i < state.groups.length; i++) {
        if (state.groups[i].id === rest) { gMatch = state.groups[i]; break; }
      }
      return {
        ref: raw,
        kind: 'group',
        providerId: 'group',
        providerName: tr('Routing group'),
        modelName: (gMatch && gMatch.name) ? gMatch.name : rest,
        groupObj: gMatch,
        icon: '/icons/favicon.svg',
        contextBadge: ''
      };
    }
    var p = findProvider(prefix);
    var pName = p ? (p.name || p.id) : prefix.replace(/^p_[0-9a-f]{8,}$/i, tr('Cloud'));
    var icon = resolveIcon(rest + ' ' + (p ? (p.name + ' ' + (p.preset || '') + ' ' + (p.baseURL || '')) : prefix));
    var ctx = '';
    if (/1m|1000k|gemini-2\.5|gemini-1\.5|claude-opus-4|claude-sonnet-4|qwen3\.5-plus|glm-5/i.test(rest)) ctx = '1M';
    else if (/200k|claude/i.test(rest)) ctx = '200K';
    else if (/128k|deepseek|gpt-4|qwen|kimi/i.test(rest)) ctx = '128K';
    return {
      ref: raw,
      kind: 'cloud',
      providerId: p ? p.id : prefix,
      providerName: pName,
      modelName: rest,
      icon: icon,
      contextBadge: ctx
    };
  }

  function groupsContainingMember(ref, excludeGroupId) {
    var names = [];
    for (var i = 0; i < state.groups.length; i++) {
      var g = state.groups[i];
      if (excludeGroupId && g.id === excludeGroupId) continue;
      if (Array.isArray(g.members) && g.members.indexOf(ref) >= 0) {
        names.push(g.name || g.id);
      }
    }
    return names;
  }

  function showStatus(msg, isError) {
    var el = document.getElementById('routingGroupStatus');
    if (!el) return;
    if (!msg) {
      el.hidden = true;
      el.textContent = '';
      el.classList.remove('is-error');
      return;
    }
    el.hidden = false;
    el.textContent = msg;
    el.classList.toggle('is-error', !!isError);
  }

  async function apiRequest(path, opts) {
    opts = opts || {};
    var headers = Object.assign({}, opts.headers || {});
    var body = opts.body;
    if (body && typeof body !== 'string') {
      body = JSON.stringify(body);
    }
    if (body && !headers['Content-Type']) {
      headers['Content-Type'] = 'application/json';
    }
    var fetchFn = typeof managementFetch === 'function' ? managementFetch : fetch;
    var res = await fetchFn(path, {
      method: opts.method || 'GET',
      headers: headers,
      body: body
    });
    var data = await res.json().catch(function () { return {}; });
    if (!res.ok || data.ok === false) {
      throw new Error(data.error || ('HTTP ' + res.status));
    }
    return data;
  }

  async function loadRoutingGroups(silent) {
    try {
      var res = await Promise.all([
        apiRequest('/api/routing-groups'),
        apiRequest('/api/cloud-providers').catch(function () { return { providers: [], localModels: [] }; })
      ]);
      state.groups = Array.isArray(res[0] && res[0].groups) ? res[0].groups : [];
      state.providers = Array.isArray(res[1] && res[1].providers) ? res[1].providers : [];
      state.localModels = Array.isArray(res[1] && res[1].localModels) ? res[1].localModels : [];
      renderGroups();
      if (!silent) showStatus('', false);
    } catch (err) {
      showStatus(err && err.message ? err.message : String(err), true);
    }
  }

  function renderMemberChip(group, member, idx) {
    var info = describeMember(member);
    var isLocal = info.kind === 'local';
    var isGroup = info.kind === 'group';
    var normRouting = normalizeStrategy(group.routing);
    var isManual = normRouting === 'manual';
    var activeRef = group.pick || group.active || '';
    var isActive = isManual && ((activeRef && activeRef === member) || (!activeRef && idx === 0));
    var w = group.weights && group.weights[member];
    var showWeight = normRouting === 'weighted' && w > 0;
    var cls = 'rt-member-chip' + (isLocal ? ' is-local' : '') + (isGroup ? ' is-group' : '') + (isActive ? ' is-active' : '');
    var title = isManual ? tr('Click to set as active model in manual mode') : (info.providerName + ' / ' + info.modelName);
    return '<span class="' + cls + '" data-member="' + esc(member) + '" data-group-id="' + esc(group.id) + '" title="' + esc(title) + '"' + (isManual ? ' style="cursor:pointer"' : '') + '>' +
      '<img class="ic" src="' + esc(info.icon) + '" alt="" loading="lazy">' +
      '<span>' + esc(info.modelName) + '</span>' +
      '<span class="rt-member-kind">· ' + esc(info.providerName) + '</span>' +
      (showWeight ? '<span class="rt-member-weight">' + esc(w) + '%</span>' : '') +
      '</span>';
  }

  function renderGroupCard(g) {
    var meta = strategyMeta(g.routing);
    var enabled = g.enabled !== false && !g.disabled;
    var membersHtml = (g.members || []).map(function (m, idx) { return renderMemberChip(g, m, idx); }).join('');
    var callId = 'group/' + g.id;
    return '<div class="rt-group-card' + (enabled ? '' : ' is-disabled') + '" data-group-id="' + esc(g.id) + '">' +
      '<div class="rt-group-top">' +
        '<button type="button" class="lib-switch' + (enabled ? ' on' : '') + '" data-rg-toggle="' + esc(g.id) + '" role="switch" aria-checked="' + (enabled ? 'true' : 'false') + '"><i></i></button>' +
        '<span class="rt-group-title">' + esc(g.name || g.id) + '</span>' +
        '<code class="rt-call-badge" data-copy-call="' + esc(callId) + '" title="' + esc(tr('Click to copy model ID')) + '">' + esc(callId) + '</code>' +
        '<span class="rt-strategy-tag">' + esc(tr(meta.cardLabel || meta.label)) + '</span>' +
        (g.auto ? '<span class="rt-auto-tag">' + esc(tr('Auto same-name')) + '</span>' : '') +
        '<span class="grow"></span>' +
        (g.auto ? '<button type="button" class="text" data-rg-pin="' + esc(g.id) + '">' + esc(tr('Pin as custom')) + '</button>' : '') +
        '<button type="button" class="text" data-rg-edit="' + esc(g.id) + '">' + esc(tr('Edit')) + '</button>' +
        (!g.auto || g.overridden ? '<button type="button" class="text danger" data-rg-delete="' + esc(g.id) + '">' + esc(tr('Delete')) + '</button>' : '') +
      '</div>' +
      '<div class="rt-group-members">' + membersHtml + '</div>' +
    '</div>';
  }

  function renderGroups() {
    var customEl = document.getElementById('customRoutingGroupsList');
    var autoSec = document.getElementById('autoRoutingGroupsSection');
    var autoEl = document.getElementById('autoRoutingGroupsList');
    if (!customEl || !autoEl || !autoSec) return;

    var custom = state.groups.filter(function (g) { return !g.auto; });
    var auto = state.groups.filter(function (g) { return !!g.auto; });

    if (!custom.length) {
      customEl.innerHTML = '<div class="row"><div class="who"><div class="sub">' + esc(tr('No custom routing groups yet. Click "＋ New routing group" to create one.')) + '</div></div></div>';
    } else {
      customEl.innerHTML = custom.map(renderGroupCard).join('');
    }

    autoSec.hidden = auto.length === 0;
    autoEl.innerHTML = auto.map(renderGroupCard).join('');
  }

  window.__apiRouterRoutingGroups = {
    STRATEGIES: STRATEGIES,
    normalizeStrategy: normalizeStrategy,
    strategyMeta: strategyMeta,
    state: state,
    tr: tr,
    esc: esc,
    resolveIcon: resolveIcon,
    providerIcon: providerIcon,
    describeMember: describeMember,
    groupsContainingMember: groupsContainingMember,
    toggleFavorite: toggleFavorite,
    showStatus: showStatus,
    apiRequest: apiRequest,
    loadRoutingGroups: loadRoutingGroups,
    renderGroups: renderGroups
  };
})();
