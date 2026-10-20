// Routing Group Editor & Model Picker Popover for API-Router.
(function () {
  var rg = window.__apiRouterRoutingGroups;
  if (!rg) return;

  var EFFORT_CYCLE = ['', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
  var THINKING_LEVELS = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max'];

  var draft = null;
  var pickerState = {
    open: false,
    filterProvider: 'all',
    query: '',
    anchorEl: null
  };
  var dragFromIndex = -1;

  function slugify(text) {
    var base = String(text || '').trim().toLowerCase()
      .replace(/[^a-z0-9._-]+/g, '-')
      .replace(/^-+|-+$/g, '');
    return base || ('group-' + Date.now().toString(36).slice(-4));
  }

  function setEditorError(msg) {
    var el = document.getElementById('rgError');
    if (el) el.textContent = msg || '';
  }

  function parseContextDisplay(ctx) {
    if (!ctx || ctx === 'max' || ctx === 'min') return '';
    var n = Number(ctx);
    if (!isFinite(n) || n <= 0) return String(ctx);
    if (n % 1000000 === 0) return (n / 1000000) + 'm';
    if (n % 1000 === 0) return (n / 1000) + 'k';
    return String(n);
  }

  function parseContextValue(raw) {
    var s = String(raw || '').trim().toLowerCase();
    if (!s) return 'max';
    if (s === 'max' || s === 'min') return s;
    var m = s.match(/^(\d+(?:\.\d+)?)\s*([km])?$/);
    if (!m) return 'max';
    var num = parseFloat(m[1]);
    if (m[2] === 'm') num *= 1000000;
    else if (m[2] === 'k') num *= 1000;
    return Math.round(num) || 'max';
  }

  function createDraft(group, forceCustom) {
    var g = group || {};
    var isNew = !g.id || !!forceCustom;
    var id = isNew ? (g.id ? g.id.replace(/^auto-/, '') : '') : g.id;
    var rawPatterns = Array.isArray(g.match) ? g.match : (Array.isArray(g.patterns) ? g.patterns : []);
    var ctxMode = 'max';
    if (g.context === -1 || g.context === 'min') ctxMode = 'min';
    else if (Number(g.context) > 0) ctxMode = Number(g.context);
    return {
      isNew: isNew,
      id: id,
      name: g.name || '',
      alias: g.family || g.alias || '',
      enabled: g.enabled !== false && !g.disabled,
      members: Array.isArray(g.members) ? g.members.slice() : [],
      patterns: rawPatterns.slice(),
      hideMembers: !!(g.unlists || g.hideMembers),
      routing: rg.normalizeStrategy(g.routing || 'smart_hybrid'),
      active: g.pick || g.active || '',
      weights: Object.assign({}, g.weights || {}),
      memberEfforts: Object.assign({}, g.memberEfforts || {}),
      cooldownMove: !!(g.sink || g.cooldownMove),
      firstToken: Number(g.firstToken) > 0 ? Number(g.firstToken) : 0,
      patience: Number(g.patience) > 0 ? Number(g.patience) : 0,
      context: ctxMode,
      affinity: g.affinity != null ? String(g.affinity) : '',
      stickiness: Number(g.stickiness) > 0 ? Number(g.stickiness) : 30,
      rules: Array.isArray(g.rules) ? g.rules.map(function (r) {
        return {
          when: r.when || 'prompt',
          pattern: r.pattern || '',
          use: r.use || r.target || '',
          effort: r.effort || '',
          fast: !!r.fast
        };
      }) : [],
      effort: g.effort || '',
      levels: Array.isArray(g.levels) ? g.levels.slice() : []
    };
  }

  function updateHeaderBadge() {
    if (!draft) return;
    var effectiveId = draft.id || slugify(draft.name || 'new-group');
    var badge = document.getElementById('rgCallBadge');
    if (badge) {
      badge.textContent = 'group/' + effectiveId;
      badge.hidden = false;
    }
    var hint = document.getElementById('rgAliasHint');
    var suffix = rg.tr('· routing group');
    if (hint) {
      var preview = draft.alias
        ? ('“' + draft.alias + '”')
        : (draft.name ? ('“' + draft.name + ' ' + suffix + '”') : ('“' + suffix + '”'));
      hint.textContent = preview + ' · ' + rg.tr('Agent call ID: ') + 'group/' + effectiveId;
    }
  }

  var PLUS_SVG = '<svg width="11" height="11" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round"><path d="M6 2v8M2 6h8"/></svg>';

  function renderMembersSection() {
    var listEl = document.getElementById('rgMembersList');
    if (!listEl || !draft) return;
    var isWeighted = draft.routing === 'weighted';
    var isManual = draft.routing === 'manual';

    if (!draft.members.length) {
      listEl.innerHTML = '';
      return;
    }

    listEl.innerHTML = draft.members.map(function (ref, idx) {
      var info = rg.describeMember(ref);
      var isActive = isManual && ((draft.active && draft.active === ref) || (!draft.active && idx === 0));
      var w = draft.weights[ref] != null ? draft.weights[ref] : 50;
      var eff = draft.memberEfforts[ref] || '';
      var effLabel = eff || rg.tr('Default');
      return '<div class="fbrow' + (isActive ? ' active-use' : '') + '" draggable="true" data-member-idx="' + idx + '" data-member-ref="' + rg.esc(ref) + '">' +
        '<button type="button" class="rt-mhandle ag-handle" title="' + rg.esc(rg.tr('Drag to reorder')) + '">' + (idx + 1) + '</button>' +
        (isManual
          ? '<button type="button" class="use-btn' + (isActive ? ' on' : '') + '" data-member-active="' + rg.esc(ref) + '" title="' + rg.esc(rg.tr(isActive ? 'Active' : 'Set active')) + '">' + (isActive ? '✓' : '') + '</button>'
          : '') +
        '<img class="ic" src="' + rg.esc(info.icon) + '" alt="" loading="lazy" style="width:15px;height:15px;flex:none;border-radius:3px;object-fit:contain">' +
        '<span class="n" title="' + rg.esc(info.modelName + ' · ' + info.providerName) + '"><span>' + rg.esc(info.modelName) + '</span><small>' + rg.esc(info.providerName) + '</small></span>' +
        '<span class="grow"></span>' +
        (isWeighted
          ? '<span class="rt-weight"><input class="rt-weight-in" type="number" min="1" max="100" step="1" data-member-weight="' + rg.esc(ref) + '" value="' + rg.esc(w) + '"> %</span>'
          : '') +
        '<button type="button" class="pill rt-fixed' + (eff ? '' : ' muted') + '" data-member-effort="' + rg.esc(ref) + '">' + rg.esc(effLabel) + '</button>' +
        '<button type="button" class="text danger" data-member-remove="' + rg.esc(ref) + '" aria-label="' + rg.esc(rg.tr('Remove')) + '">×</button>' +
      '</div>';
    }).join('');
  }

  function renderEditorBody() {
    var body = document.getElementById('rgEditorBody');
    if (!body || !draft) return;

    var stratMeta = rg.strategyMeta(draft.routing);
    var stratButtons = rg.STRATEGIES.map(function (s) {
      var on = draft.routing === s.id;
      return '<button type="button" class="opt' + (on ? ' on' : '') + '" data-rg-strategy="' + rg.esc(s.id) + '">' + rg.esc(rg.tr(s.label)) + '</button>';
    }).join('');

    var patternsHtml = draft.patterns.map(function (pat, idx) {
      return '<div class="fbrow">' +
        '<span class="i">' + (idx + 1) + '</span>' +
        '<input type="text" data-pattern-idx="' + idx + '" value="' + rg.esc(pat) + '" placeholder="' + rg.esc(rg.tr('e.g. openrouter/*:free or re:^claude-')) + '" autocomplete="off" style="flex:1;min-width:0">' +
        '<button type="button" class="text danger" data-pattern-remove="' + idx + '">×</button>' +
      '</div>';
    }).join('');

    var ftPreset = [0, 30, 60, 120].indexOf(draft.firstToken) >= 0 ? draft.firstToken : -1;
    var ftCustomVal = ftPreset < 0 && draft.firstToken > 0 ? draft.firstToken : '';

    var patPreset = [0, 60, 120, 240].indexOf(draft.patience) >= 0 ? draft.patience : -1;
    var patCustomMin = patPreset < 0 && draft.patience > 0 ? Math.max(1, Math.round(draft.patience / 60)) : '';

    var isCtxMax = !draft.context || draft.context === 'max';
    var isCtxMin = draft.context === 'min';
    var ctxCustomStr = (!isCtxMax && !isCtxMin) ? parseContextDisplay(draft.context) : '';

    var aff = draft.affinity || '';
    var affHint = 'Stays on the first chosen model to keep prompt cache warm across turns.';
    if (aff === 'session') affHint = 'Stays on the same model for the entire conversation session.';
    else if (aff === 'turn') affHint = 'Stays on the same model within a single agent turn (tool-call loop).';
    else if (aff === 'off') affHint = 'Picks freshly on every request without session stickiness.';
    else if (aff === 'pin') affHint = 'Pins the session strictly to the first chosen model; does not fail over.';

    var rulesHtml = draft.rules.map(function (r, idx) {
      var memberOpts = draft.members.map(function (m) {
        var info = rg.describeMember(m);
        var sel = r.use === m ? ' selected' : '';
        return '<option value="' + rg.esc(m) + '"' + sel + '>' + rg.esc(info.modelName + ' · ' + info.providerName) + '</option>';
      }).join('');
      if (r.use && draft.members.indexOf(r.use) < 0) {
        memberOpts = '<option value="' + rg.esc(r.use) + '" selected>' + rg.esc(r.use) + '</option>' + memberOpts;
      }
      var hidePattern = r.when === 'tools' || r.when === 'vision';
      return '<div class="rt-rule" data-rule-idx="' + idx + '">' +
        '<span class="rt-rule-txt">' + rg.esc(rg.tr('When')) + '</span>' +
        '<select class="rt-rule-when" data-rule-field="when" data-rule-idx="' + idx + '">' +
          '<option value="prompt"' + (r.when === 'prompt' ? ' selected' : '') + '>' + rg.esc(rg.tr('Prompt matches')) + '</option>' +
          '<option value="system"' + (r.when === 'system' ? ' selected' : '') + '>' + rg.esc(rg.tr('System matches')) + '</option>' +
          '<option value="model"' + (r.when === 'model' ? ' selected' : '') + '>' + rg.esc(rg.tr('Model name')) + '</option>' +
          '<option value="tools"' + (r.when === 'tools' ? ' selected' : '') + '>' + rg.esc(rg.tr('Has tools')) + '</option>' +
          '<option value="vision"' + (r.when === 'vision' ? ' selected' : '') + '>' + rg.esc(rg.tr('Has vision')) + '</option>' +
          '<option value="tokens"' + (r.when === 'tokens' ? ' selected' : '') + '>' + rg.esc(rg.tr('Context >')) + '</option>' +
        '</select>' +
        (hidePattern ? '' : '<input type="text" class="rt-rule-val" data-rule-field="pattern" data-rule-idx="' + idx + '" value="' + rg.esc(r.pattern || '') + '" placeholder="' + rg.esc(rg.tr('regex or keyword')) + '">') +
        '<span class="rt-rule-txt">' + rg.esc(rg.tr('route to')) + '</span>' +
        '<select class="rt-rule-use" data-rule-field="use" data-rule-idx="' + idx + '">' + memberOpts + '</select>' +
        '<button type="button" class="text danger" data-rule-remove="' + idx + '" aria-label="' + rg.esc(rg.tr('Remove')) + '">×</button>' +
      '</div>';
    }).join('');

    var effortOptions = [
      { id: '', label: rg.tr('As requested') },
      { id: 'auto', label: rg.tr('Per turn') },
      { id: 'low', label: 'low' },
      { id: 'medium', label: 'medium' },
      { id: 'high', label: 'high' },
      { id: 'xhigh', label: 'xhigh' },
      { id: 'max', label: 'max' }
    ];
    var effortHint = !draft.effort
      ? rg.tr('Keeps whatever reasoning effort the client request asked for.')
      : (draft.effort === 'auto'
        ? rg.tr('Adjusts reasoning effort per turn based on prompt complexity.')
        : (rg.tr('Forces reasoning effort to ') + draft.effort + '.'));

    var isLevelsIntersect = !draft.levels || draft.levels.length === 0;

    body.innerHTML =
      // 1. Name
      '<label for="rgName">' + rg.esc(rg.tr('Name')) + '</label>' +
      '<input id="rgName" name="name" type="text" required maxlength="120" autocomplete="off" placeholder="' + rg.esc(rg.tr('e.g. Opus fallback')) + '" value="' + rg.esc(draft.name) + '">' +

      // 2. Name in Agents
      '<label for="rgAlias">' + rg.esc(rg.tr('Name in Agents')) + '</label>' +
      '<div class="gsuffix">' +
        '<input id="rgAlias" name="alias" type="text" maxlength="120" autocomplete="off" placeholder="' + rg.esc((draft.name ? draft.name + ' ' : '') + rg.tr('· routing group')) + '" value="' + rg.esc(draft.alias) + '" style="flex:1;min-width:180px">' +
        '<span class="hint" id="rgAliasHint"></span>' +
      '</div>' +

      // 3. Models
      '<label>' + rg.esc(rg.tr('Models')) + '</label>' +
      '<div class="fallback">' +
        '<div class="fbl" id="rgMembersList"></div>' +
        '<button type="button" class="rt-gadd" id="rgAddModelBtn">' + PLUS_SVG + '<span>' + rg.esc(rg.tr('Add model')) + '</span></button>' +
        '<div class="hint">' + rg.esc(rg.tr('First model defines the group\'s capabilities; tried top-to-bottom in In-order mode.')) + '</div>' +
      '</div>' +

      // 4. Patterns
      '<label>' + rg.esc(rg.tr('Patterns')) + '</label>' +
      '<div class="fallback rt-pats">' +
        '<div class="fbl" id="rgPatternsList">' + patternsHtml + '</div>' +
        '<div class="rt-patadd">' +
          '<input type="text" id="rgPatternNewInput" placeholder="' + rg.esc(rg.tr('e.g. openrouter/*:free or re:^claude-')) + '" autocomplete="off">' +
          '<button type="button" class="rt-gadd" id="rgAddPatternBtn">' + PLUS_SVG + '<span>' + rg.esc(rg.tr('Add pattern')) + '</span></button>' +
        '</div>' +
        '<div class="hint">' + rg.esc(rg.tr('e.g. openrouter/*:free or re:…. Newly discovered matching models auto-join after manual picks.')) + '</div>' +
      '</div>' +

      // 5. Model list
      '<label>' + rg.esc(rg.tr('Model list')) + '</label>' +
      '<div>' +
        '<div class="segs">' +
          '<button type="button" class="opt' + (!draft.hideMembers ? ' on' : '') + '" data-rg-hide-members="0">' + rg.esc(rg.tr('Keep members listed')) + '</button>' +
          '<button type="button" class="opt' + (draft.hideMembers ? ' on' : '') + '" data-rg-hide-members="1">' + rg.esc(rg.tr('Group only')) + '</button>' +
        '</div>' +
        '<div class="hint">' + rg.esc(rg.tr(draft.hideMembers ? 'Member models are hidden from model pickers; only this group is listed.' : 'Member models still appear individually in agents\' model pickers.')) + '</div>' +
      '</div>' +

      // 6. Routing
      '<label>' + rg.esc(rg.tr('Routing')) + '</label>' +
      '<div>' +
        '<div class="segs">' + stratButtons + '</div>' +
        '<div class="hint">' + rg.esc(rg.tr(stratMeta.desc)) + '</div>' +
      '</div>' +

      // 7. On limit (shown when routing === 'order')
      (draft.routing === 'order'
        ? '<label>' + rg.esc(rg.tr('On limit')) + '</label>' +
          '<div>' +
            '<div class="segs">' +
              '<button type="button" class="opt' + (!draft.cooldownMove ? ' on' : '') + '" data-rg-cooldown-move="0">' + rg.esc(rg.tr('Keep position')) + '</button>' +
              '<button type="button" class="opt' + (draft.cooldownMove ? ' on' : '') + '" data-rg-cooldown-move="1">' + rg.esc(rg.tr('Move to end')) + '</button>' +
            '</div>' +
            '<div class="hint">' + rg.esc(rg.tr(draft.cooldownMove ? 'Rate-limited model rotates to the bottom so later requests skip the wait.' : 'Next round tries the first model again once its cooldown ends.')) + '</div>' +
          '</div>'
        : '') +

      // 8. Slow stream
      '<label>' + rg.esc(rg.tr('Slow stream')) + '</label>' +
      '<div class="ctx-wrap">' +
        '<div class="segs">' +
          '<button type="button" class="opt' + (ftPreset === 0 ? ' on' : '') + '" data-rg-ft="0">' + rg.esc(rg.tr('Wait')) + '</button>' +
          '<button type="button" class="opt' + (ftPreset === 30 ? ' on' : '') + '" data-rg-ft="30">' + rg.esc(rg.tr('30s')) + '</button>' +
          '<button type="button" class="opt' + (ftPreset === 60 ? ' on' : '') + '" data-rg-ft="60">' + rg.esc(rg.tr('1 min')) + '</button>' +
          '<button type="button" class="opt' + (ftPreset === 120 ? ' on' : '') + '" data-rg-ft="120">' + rg.esc(rg.tr('2 min')) + '</button>' +
        '</div>' +
        '<input type="number" class="first-in" id="rgFirstTokenInput" min="1" max="600" placeholder="45" value="' + rg.esc(ftCustomVal) + '">' +
        '<span class="first-unit">' + rg.esc(rg.tr('s')) + '</span>' +
        '<div class="hint">' + rg.esc(rg.tr('If upstream connects but emits no token within this time — or stalls mid-stream — retry the next member.')) + '</div>' +
      '</div>' +

      // 9. When all busy
      '<label>' + rg.esc(rg.tr('When all busy')) + '</label>' +
      '<div class="ctx-wrap">' +
        '<div class="segs">' +
          '<button type="button" class="opt' + (patPreset === 0 ? ' on' : '') + '" data-rg-pat="0">' + rg.esc(rg.tr('Off')) + '</button>' +
          '<button type="button" class="opt' + (patPreset === 60 ? ' on' : '') + '" data-rg-pat="60">' + rg.esc(rg.tr('1 min')) + '</button>' +
          '<button type="button" class="opt' + (patPreset === 120 ? ' on' : '') + '" data-rg-pat="120">' + rg.esc(rg.tr('2 min')) + '</button>' +
          '<button type="button" class="opt' + (patPreset === 240 ? ' on' : '') + '" data-rg-pat="240">' + rg.esc(rg.tr('4 min')) + '</button>' +
        '</div>' +
        '<input type="number" class="first-in" id="rgPatienceInput" min="1" max="60" placeholder="3" value="' + rg.esc(patCustomMin) + '">' +
        '<span class="first-unit">' + rg.esc(rg.tr('min')) + '</span>' +
        '<div class="hint">' + rg.esc(rg.tr('When every member is cooling down, wait for the soonest one and keep retrying within this window before failing.')) + '</div>' +
      '</div>' +

      // 10. Context
      '<label>' + rg.esc(rg.tr('Context')) + '</label>' +
      '<div class="ctx-wrap">' +
        '<div class="segs">' +
          '<button type="button" class="opt' + (isCtxMax ? ' on' : '') + '" data-rg-ctx="max">' + rg.esc(rg.tr('Largest member')) + '</button>' +
          '<button type="button" class="opt' + (isCtxMin ? ' on' : '') + '" data-rg-ctx="min">' + rg.esc(rg.tr('Smallest member')) + '</button>' +
        '</div>' +
        '<input type="text" class="ctx-in" id="rgContextInput" placeholder="200k" value="' + rg.esc(ctxCustomStr) + '">' +
        '<span class="first-unit">' + rg.esc(rg.tr('tokens')) + '</span>' +
        '<div class="hint">' + rg.esc(rg.tr(isCtxMax ? 'Advertises the largest member\'s context window so long chats aren\'t compacted early.' : (isCtxMin ? 'Advertises the smallest member\'s context window so every fallback fits.' : 'Advertises this custom context window to agents.'))) + '</div>' +
      '</div>' +

      // 11. Session stays on
      '<label>' + rg.esc(rg.tr('Session stays on')) + '</label>' +
      '<div class="ctx-wrap">' +
        '<div class="segs">' +
          '<button type="button" class="opt' + (aff === '' ? ' on' : '') + '" data-rg-aff="">' + rg.esc(rg.tr('Auto')) + '</button>' +
          '<button type="button" class="opt' + (aff === 'session' ? ' on' : '') + '" data-rg-aff="session">' + rg.esc(rg.tr('Whole session')) + '</button>' +
          '<button type="button" class="opt' + (aff === 'turn' ? ' on' : '') + '" data-rg-aff="turn">' + rg.esc(rg.tr('Within turn')) + '</button>' +
          '<button type="button" class="opt' + (aff === 'off' ? ' on' : '') + '" data-rg-aff="off">' + rg.esc(rg.tr('Off')) + '</button>' +
          '<button type="button" class="opt' + (aff === 'pin' ? ' on' : '') + '" data-rg-aff="pin">' + rg.esc(rg.tr('Pin')) + '</button>' +
        '</div>' +
        (aff === 'session'
          ? '<input type="number" class="first-in" id="rgStickinessInput" min="1" max="1440" value="' + rg.esc(draft.stickiness || 30) + '">' +
            '<span class="first-unit">' + rg.esc(rg.tr('min idle')) + '</span>'
          : '') +
        '<div class="hint">' + rg.esc(rg.tr(affHint)) + '</div>' +
      '</div>' +

      // 12. Rules
      '<label>' + rg.esc(rg.tr('Rules')) + '</label>' +
      '<div class="fallback rt-rules">' +
        '<div class="fbl" id="rgRulesList">' + rulesHtml + '</div>' +
        '<div class="rt-gadds">' +
          '<button type="button" class="rt-gadd" id="rgAddRuleBtn">' + PLUS_SVG + '<span>' + rg.esc(rg.tr('Add rule')) + '</span></button>' +
        '</div>' +
        '<div class="hint">' + rg.esc(rg.tr('Matched top-to-bottom; the first matching rule routes to its target model, otherwise the group\'s default routing applies.')) + '</div>' +
      '</div>' +

      // 13. Reasoning
      '<label>' + rg.esc(rg.tr('Reasoning')) + '</label>' +
      '<div>' +
        '<div class="segs">' +
          effortOptions.map(function (opt) {
            return '<button type="button" class="opt' + ((draft.effort || '') === opt.id ? ' on' : '') + '" data-rg-effort="' + rg.esc(opt.id) + '">' + rg.esc(opt.label) + '</button>';
          }).join('') +
        '</div>' +
        '<div class="hint">' + rg.esc(effortHint) + '</div>' +
      '</div>' +

      // 14. Thinking levels
      '<label>' + rg.esc(rg.tr('Thinking levels')) + '</label>' +
      '<div>' +
        '<div class="segs">' +
          '<button type="button" class="opt' + (isLevelsIntersect ? ' on' : '') + '" data-rg-level-reset="1">' + rg.esc(rg.tr('Intersection')) + '</button>' +
          THINKING_LEVELS.map(function (lv) {
            var on = !isLevelsIntersect && draft.levels.indexOf(lv) >= 0;
            return '<button type="button" class="opt' + (on ? ' on' : '') + '" data-rg-level="' + rg.esc(lv) + '">' + rg.esc(lv) + '</button>';
          }).join('') +
        '</div>' +
        '<div class="hint">' + rg.esc(rg.tr('Exposes thinking levels supported by all members (excess levels auto-downgrade on fallback).')) + '</div>' +
      '</div>';

    renderMembersSection();
    updateHeaderBadge();
  }

  // Build candidate sections for the Model Picker Popover.
  function collectPickerSections() {
    var sections = [];
    var currentEditId = draft ? draft.id : '';

    // 1. Other Routing Groups
    var groupItems = [];
    (rg.state.groups || []).forEach(function (g) {
      if (!g.id || g.id === currentEditId) return;
      var ref = 'group/' + g.id;
      var strat = rg.strategyMeta(g.routing);
      groupItems.push({
        ref: ref,
        providerKey: 'group',
        modelName: g.name || g.id,
        icon: '/icons/favicon.svg',
        ctx: '',
        note: (g.members ? g.members.length : 0) + ' ' + rg.tr('models') + ' · ' + rg.tr(strat.label)
      });
    });
    if (groupItems.length) {
      sections.push({ key: 'group', title: rg.tr('Routing groups'), items: groupItems });
    }

    // 2. Local runtime models
    var localItems = [];
    (rg.state.localModels || []).forEach(function (m) {
      var id = m && (m.id || m.name);
      if (!id) return;
      var ref = 'local/' + id;
      var info = rg.describeMember(ref);
      var usedIn = rg.groupsContainingMember(ref, currentEditId);
      localItems.push({
        ref: ref,
        providerKey: 'local',
        modelName: id,
        icon: info.icon,
        ctx: info.contextBadge || '',
        note: rg.tr('Local on-device') + (usedIn.length ? (' · ' + rg.tr('in ') + usedIn.join(', ')) : '')
      });
    });
    if (localItems.length) {
      sections.push({ key: 'local', title: rg.tr('Local runtime'), items: localItems });
    }

    // 3. Cloud providers
    (rg.state.providers || []).forEach(function (p) {
      var pKey = p.id || p.name || 'cloud';
      var pName = p.name || p.id || rg.tr('Cloud');
      var models = (Array.isArray(p.discoveredModels) && p.discoveredModels.length)
        ? p.discoveredModels
        : (Array.isArray(p.effectiveModels) && p.effectiveModels.length)
          ? p.effectiveModels
          : (Array.isArray(p.models) ? p.models : []);
      var items = [];
      models.forEach(function (m) {
        if (!m) return;
        var ref = pKey + '/' + m;
        var info = rg.describeMember(ref);
        var usedIn = rg.groupsContainingMember(ref, currentEditId);
        items.push({
          ref: ref,
          providerKey: pKey,
          modelName: m,
          icon: info.icon,
          ctx: info.contextBadge || '',
          note: pName + (usedIn.length ? (' · ' + rg.tr('in ') + usedIn.join(', ')) : '')
        });
      });
      if (items.length) {
        sections.push({
          key: pKey,
          title: pName.toUpperCase(),
          providerName: pName,
          icon: rg.providerIcon(p),
          items: items
        });
      }
    });

    return sections;
  }

  function positionModelPicker() {
    var pop = document.getElementById('rgModelPicker');
    var anchor = pickerState.anchorEl || document.getElementById('rgAddModelBtn');
    if (!pop || !anchor || pop.hidden) return;
    var rect = anchor.getBoundingClientRect();
    var popW = Math.min(440, window.innerWidth - 24);
    var left = Math.min(Math.max(12, rect.left), Math.max(12, window.innerWidth - popW - 12));
    var top = rect.bottom + 6;
    var maxH = 380;
    if (top + maxH > window.innerHeight - 12 && rect.top > maxH + 12) {
      top = Math.max(12, rect.top - maxH - 6);
    }
    pop.style.left = left + 'px';
    pop.style.top = top + 'px';
  }

  var STAR_SVG = '<svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"><path d="M8 2.1l1.76 3.57 3.94.57-2.85 2.78.67 3.92L8 11.09l-3.52 1.85.67-3.92L2.3 6.24l3.94-.57L8 2.1z"/></svg>';
  var CHECK_SVG = '<svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M3.5 8.5l3 3 6-6.5"/></svg>';

  function renderModelPickerList() {
    var listEl = document.getElementById('rgPickerList');
    var railEl = document.getElementById('rgPickerRail');
    if (!listEl || !draft) return;

    if (railEl) {
      railEl.querySelectorAll('button[data-rail]').forEach(function (btn) {
        btn.classList.toggle('on', btn.getAttribute('data-rail') === pickerState.filterProvider);
      });
    }

    var sections = collectPickerSections();
    var q = String(pickerState.query || '').trim().toLowerCase();
    var selectedSet = {};
    draft.members.forEach(function (m) { selectedSet[m] = true; });

    var html = '';
    sections.forEach(function (sec) {
      if (pickerState.filterProvider !== 'all' && pickerState.filterProvider !== 'fav' && sec.key !== pickerState.filterProvider) {
        return;
      }
      var filtered = sec.items.filter(function (item) {
        if (pickerState.filterProvider === 'fav' && !rg.state.favorites[item.ref]) return false;
        if (!q) return true;
        return item.modelName.toLowerCase().indexOf(q) >= 0 ||
          item.note.toLowerCase().indexOf(q) >= 0 ||
          item.ref.toLowerCase().indexOf(q) >= 0;
      });
      if (!filtered.length) return;
      html += '<li class="group">' + rg.esc(sec.title) + '</li>';
      filtered.forEach(function (item) {
        var isSel = !!selectedSet[item.ref];
        var isFav = !!rg.state.favorites[item.ref];
        html += '<li data-picker-ref="' + rg.esc(item.ref) + '" class="' + (isSel ? 'cur' : '') + '">' +
          '<img class="ic" src="' + rg.esc(item.icon) + '" alt="" loading="lazy">' +
          '<span class="option-words">' +
            '<span class="v">' + rg.esc(item.modelName) + '</span>' +
            (item.ctx ? '<span class="badge ctx">' + rg.esc(item.ctx) + '</span>' : '') +
            '<span class="n">' + rg.esc(item.note) + '</span>' +
          '</span>' +
          '<button type="button" class="favorite' + (isFav ? ' on' : '') + '" data-picker-fav="' + rg.esc(item.ref) + '" aria-label="Favorite">' + STAR_SVG + '</button>' +
          '<span class="check">' + CHECK_SVG + '</span>' +
        '</li>';
      });
    });

    if (!html) {
      if (q && q.indexOf('/') > 0) {
        var customInfo = rg.describeMember(pickerState.query.trim());
        var isSelCustom = !!selectedSet[customInfo.ref];
        html = '<li data-picker-ref="' + rg.esc(customInfo.ref) + '" class="' + (isSelCustom ? 'cur' : '') + '">' +
          '<img class="ic" src="' + rg.esc(customInfo.icon) + '" alt="">' +
          '<span class="option-words">' +
            '<span class="v">' + rg.esc(customInfo.modelName) + '</span>' +
            '<span class="n">' + rg.esc(customInfo.providerName) + '</span>' +
          '</span>' +
          '<span class="check">' + CHECK_SVG + '</span>' +
        '</li>';
      } else {
        html = '<li class="empty">' + rg.esc(rg.tr('No matching models')) + '</li>';
      }
    }

    listEl.innerHTML = html;
  }

  function openModelPicker(anchor) {
    var pop = document.getElementById('rgModelPicker');
    if (!pop || !draft) return;
    pickerState.open = true;
    pickerState.anchorEl = anchor;
    pickerState.query = '';
    pickerState.filterProvider = 'all';

    var sections = collectPickerSections();
    var railButtons =
      '<button type="button" class="rail-item on" data-rail="all" title="' + rg.esc(rg.tr('All models')) + '">' +
        '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><rect x="2" y="2" width="5" height="5" rx="1"/><rect x="9" y="2" width="5" height="5" rx="1"/><rect x="2" y="9" width="5" height="5" rx="1"/><rect x="9" y="9" width="5" height="5" rx="1"/></svg>' +
      '</button>' +
      '<button type="button" class="rail-item" data-rail="fav" title="' + rg.esc(rg.tr('Favorites')) + '">' +
        '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.4"><path d="M8 2.2l1.8 3.6 4 .6-2.9 2.8.7 4-3.6-1.9-3.6 1.9.7-4L2.2 6.4l4-.6L8 2.2z"/></svg>' +
      '</button>';

    sections.forEach(function (sec) {
      if (sec.key === 'group') {
        railButtons += '<button type="button" class="rail-item" data-rail="group" title="' + rg.esc(rg.tr('Routing groups')) + '">' +
          '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M8 2L2 5.5 8 9l6-3.5L8 2zM2 10.5L8 14l6-3.5"/></svg>' +
        '</button>';
      } else if (sec.key === 'local') {
        railButtons += '<button type="button" class="rail-item" data-rail="local" title="' + rg.esc(rg.tr('Local runtime')) + '">' +
          '<svg width="16" height="16" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><rect x="4" y="4" width="8" height="8" rx="1.5"/><path d="M6 1.5v2M10 1.5v2M6 12.5v2M10 12.5v2M1.5 6h2M1.5 10h2M12.5 6h2M12.5 10h2"/></svg>' +
        '</button>';
      } else {
        railButtons += '<button type="button" class="rail-item" data-rail="' + rg.esc(sec.key) + '" title="' + rg.esc(sec.providerName || sec.title) + '">' +
          '<img class="ic" src="' + rg.esc(sec.icon || '/icons/favicon.svg') + '" alt="">' +
        '</button>';
      }
    });

    pop.innerHTML =
      '<div class="search">' +
        '<svg width="13" height="13" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5"><circle cx="7" cy="7" r="4.5"/><path d="M10.5 10.5L14 14" stroke-linecap="round"/></svg>' +
        '<input type="text" id="rgPickerSearch" autocomplete="off" placeholder="' + rg.esc(rg.tr('Filter…')) + '">' +
      '</div>' +
      '<div class="picker-body">' +
        '<div id="rgPickerRail">' + railButtons + '</div>' +
        '<ul id="rgPickerList"></ul>' +
      '</div>';

    pop.hidden = false;
    renderModelPickerList();
    positionModelPicker();

    var searchInput = document.getElementById('rgPickerSearch');
    if (searchInput) searchInput.focus();
  }

  function closeModelPicker() {
    pickerState.open = false;
    var pop = document.getElementById('rgModelPicker');
    if (pop) pop.hidden = true;
  }

  function openEditor(group, forceCustom) {
    draft = createDraft(group, forceCustom);
    rg.state.isNew = draft.isNew;
    rg.state.editingId = draft.id;

    var modal = document.getElementById('routingGroupModal');
    var titleEl = document.getElementById('routingGroupModalTitle');
    var delBtn = document.getElementById('rgDeleteBtn');
    if (!modal) return;

    if (titleEl) {
      titleEl.textContent = draft.isNew ? rg.tr('New group') : rg.tr('Edit routing group');
    }
    if (delBtn) {
      delBtn.hidden = draft.isNew;
    }

    setEditorError('');
    closeModelPicker();
    renderEditorBody();
    modal.hidden = false;

    var nameInput = document.getElementById('rgName');
    if (nameInput) nameInput.focus();
  }

  function closeEditor() {
    closeModelPicker();
    var modal = document.getElementById('routingGroupModal');
    if (modal) modal.hidden = true;
    setEditorError('');
  }

  async function saveGroup(ev) {
    if (ev) ev.preventDefault();
    if (!draft) return;

    var nameInput = document.getElementById('rgName');
    var aliasInput = document.getElementById('rgAlias');
    if (nameInput) draft.name = nameInput.value.trim();
    if (aliasInput) draft.alias = aliasInput.value.trim();

    if (!draft.name) {
      setEditorError(rg.tr('Group name is required.'));
      return;
    }
    var pendingPatEl = document.getElementById('rgPatternNewInput');
    var pendingPat = pendingPatEl ? pendingPatEl.value.trim() : '';
    var allPatterns = (draft.patterns || []).slice();
    if (pendingPat) allPatterns.push(pendingPat);
    var cleanPatterns = allPatterns.map(function (p) { return String(p || '').trim(); }).filter(Boolean);
    if (!draft.members.length && !cleanPatterns.length) {
      setEditorError(rg.tr('Please add at least one member model or pattern.'));
      return;
    }

    var effectiveId = draft.id || slugify(draft.name);
    var weightsOut = {};
    if (draft.routing === 'weighted') {
      draft.members.forEach(function (m) {
        var w = parseInt(draft.weights[m], 10);
        weightsOut[m] = (w >= 1 && w <= 100) ? w : 50;
      });
    }

    var cleanRules = (draft.rules || []).filter(function (r) {
      if (!r.use) return false;
      if (r.when !== 'tools' && r.when !== 'vision' && !String(r.pattern || '').trim()) return false;
      return true;
    }).map(function (r) {
      return {
        when: r.when || 'prompt',
        pattern: String(r.pattern || '').trim(),
        use: String(r.use || '').trim(),
        effort: String(r.effort || '').trim(),
        fast: !!r.fast
      };
    });

    var ctxInt = 0;
    if (draft.context === 'min' || draft.context === -1) ctxInt = -1;
    else if (Number(draft.context) > 0) ctxInt = Math.round(Number(draft.context));

    var activeModel = draft.routing === 'manual' ? (draft.active || draft.members[0] || '') : (draft.active || '');

    var payload = {
      id: effectiveId,
      name: draft.name,
      family: draft.alias || '',
      alias: draft.alias || '',
      enabled: draft.enabled !== false,
      routing: draft.routing,
      members: draft.members.slice(),
      match: cleanPatterns,
      patterns: cleanPatterns,
      unlists: !!draft.hideMembers,
      hideMembers: !!draft.hideMembers,
      pick: activeModel,
      active: activeModel,
      weights: weightsOut,
      memberEfforts: draft.memberEfforts || {},
      sink: !!draft.cooldownMove,
      cooldownMove: !!draft.cooldownMove,
      firstToken: draft.firstToken || 0,
      patience: draft.patience || 0,
      context: ctxInt,
      affinity: draft.affinity || '',
      stickiness: draft.affinity === 'session' ? (draft.stickiness || 30) : 0,
      rules: cleanRules,
      effort: draft.effort || '',
      levels: draft.levels || []
    };

    try {
      await rg.apiRequest('/api/routing-groups', { method: 'POST', body: payload });
      closeEditor();
      await rg.loadRoutingGroups(true);
      rg.showStatus(rg.tr('Routing group saved.'), false);
    } catch (err) {
      setEditorError(rg.tr(err && err.message ? err.message : String(err)));
    }
  }

  async function deleteGroup(id) {
    var target = (rg.state.groups || []).find(function (g) { return g.id === id; });
    var label = target ? (target.name || target.id) : id;
    var msg = rg.tr('Delete routing group "{1}"?').replace('{1}', label);
    if (!window.confirm(msg)) return;
    try {
      await rg.apiRequest('/api/routing-groups?id=' + encodeURIComponent(id), { method: 'DELETE' });
      closeEditor();
      await rg.loadRoutingGroups(true);
      rg.showStatus(rg.tr('Routing group deleted.'), false);
    } catch (err) {
      rg.showStatus(rg.tr(err && err.message ? err.message : String(err)), true);
    }
  }

  function bindEvents() {
    var refreshBtn = document.getElementById('refreshRoutingGroupsBtn');
    if (refreshBtn) refreshBtn.addEventListener('click', function () { rg.loadRoutingGroups(false); });

    var newBtn = document.getElementById('newRoutingGroupBtn');
    if (newBtn) newBtn.addEventListener('click', function () { openEditor(null, true); });

    document.querySelectorAll('button[data-view="routing"]').forEach(function (btn) {
      btn.addEventListener('click', function () { rg.loadRoutingGroups(true); });
    });

    var cancelBtn = document.getElementById('rgCancelBtn');
    if (cancelBtn) cancelBtn.addEventListener('click', closeEditor);

    var delBtn = document.getElementById('rgDeleteBtn');
    if (delBtn) delBtn.addEventListener('click', function () {
      if (draft && draft.id) deleteGroup(draft.id);
    });

    var form = document.getElementById('routingGroupForm');
    if (form) form.addEventListener('submit', saveGroup);

    var body = document.getElementById('rgEditorBody');
    if (body) {
      body.addEventListener('input', function (ev) {
        if (!draft) return;
        var t = ev.target;
        if (t.id === 'rgName') {
          draft.name = t.value;
          if (draft.isNew) draft.id = slugify(draft.name);
          var aliasEl = document.getElementById('rgAlias');
          if (aliasEl) aliasEl.placeholder = (draft.name ? draft.name + ' ' : '') + rg.tr('· routing group');
          updateHeaderBadge();
          return;
        }
        if (t.id === 'rgAlias') {
          draft.alias = t.value;
          updateHeaderBadge();
          return;
        }
        if (t.hasAttribute('data-member-weight')) {
          var mRef = t.getAttribute('data-member-weight');
          draft.weights[mRef] = Math.max(1, Math.min(100, parseInt(t.value, 10) || 50));
          return;
        }
        if (t.hasAttribute('data-pattern-idx')) {
          var pIdx = parseInt(t.getAttribute('data-pattern-idx'), 10);
          draft.patterns[pIdx] = t.value;
          return;
        }
        if (t.id === 'rgFirstTokenInput') {
          var vFt = parseInt(t.value, 10);
          draft.firstToken = vFt > 0 ? vFt : 0;
          body.querySelectorAll('[data-rg-ft]').forEach(function (b) { b.classList.remove('on'); });
          return;
        }
        if (t.id === 'rgPatienceInput') {
          var vPat = parseInt(t.value, 10);
          draft.patience = vPat > 0 ? vPat * 60 : 0;
          body.querySelectorAll('[data-rg-pat]').forEach(function (b) { b.classList.remove('on'); });
          return;
        }
        if (t.id === 'rgContextInput') {
          draft.context = parseContextValue(t.value);
          body.querySelectorAll('[data-rg-ctx]').forEach(function (b) { b.classList.remove('on'); });
          return;
        }
        if (t.id === 'rgStickinessInput') {
          draft.stickiness = Math.max(1, parseInt(t.value, 10) || 30);
          return;
        }
        if (t.hasAttribute('data-rule-field')) {
          var rIdx = parseInt(t.getAttribute('data-rule-idx'), 10);
          var field = t.getAttribute('data-rule-field');
          if (draft.rules[rIdx]) {
            draft.rules[rIdx][field] = t.value;
            if (field === 'when') renderEditorBody();
          }
        }
      });

      body.addEventListener('click', function (ev) {
        if (!draft) return;
        var addModelBtn = ev.target.closest('#rgAddModelBtn');
        if (addModelBtn) {
          if (pickerState.open) closeModelPicker();
          else openModelPicker(addModelBtn);
          return;
        }

        var stratBtn = ev.target.closest('[data-rg-strategy]');
        if (stratBtn) {
          draft.routing = stratBtn.getAttribute('data-rg-strategy');
          renderEditorBody();
          return;
        }

        var removeMemBtn = ev.target.closest('[data-member-remove]');
        if (removeMemBtn) {
          var remRef = removeMemBtn.getAttribute('data-member-remove');
          draft.members = draft.members.filter(function (m) { return m !== remRef; });
          renderMembersSection();
          if (pickerState.open) renderModelPickerList();
          return;
        }

        var activeMemBtn = ev.target.closest('[data-member-active]');
        if (activeMemBtn) {
          draft.active = activeMemBtn.getAttribute('data-member-active');
          renderMembersSection();
          return;
        }

        var effMemBtn = ev.target.closest('[data-member-effort]');
        if (effMemBtn) {
          var effRef = effMemBtn.getAttribute('data-member-effort');
          var curEff = draft.memberEfforts[effRef] || '';
          var nextIdx = (EFFORT_CYCLE.indexOf(curEff) + 1) % EFFORT_CYCLE.length;
          var nextEff = EFFORT_CYCLE[nextIdx];
          if (nextEff) draft.memberEfforts[effRef] = nextEff;
          else delete draft.memberEfforts[effRef];
          renderMembersSection();
          return;
        }

        var addPatBtn = ev.target.closest('#rgAddPatternBtn');
        if (addPatBtn) {
          var newPatIn = document.getElementById('rgPatternNewInput');
          var val = newPatIn ? newPatIn.value.trim() : '';
          draft.patterns.push(val);
          renderEditorBody();
          return;
        }

        var remPatBtn = ev.target.closest('[data-pattern-remove]');
        if (remPatBtn) {
          var remIdx = parseInt(remPatBtn.getAttribute('data-pattern-remove'), 10);
          draft.patterns.splice(remIdx, 1);
          renderEditorBody();
          return;
        }

        var hideMemBtn = ev.target.closest('[data-rg-hide-members]');
        if (hideMemBtn) {
          draft.hideMembers = hideMemBtn.getAttribute('data-rg-hide-members') === '1';
          renderEditorBody();
          return;
        }

        var cdMoveBtn = ev.target.closest('[data-rg-cooldown-move]');
        if (cdMoveBtn) {
          draft.cooldownMove = cdMoveBtn.getAttribute('data-rg-cooldown-move') === '1';
          renderEditorBody();
          return;
        }

        var ftBtn = ev.target.closest('[data-rg-ft]');
        if (ftBtn) {
          draft.firstToken = parseInt(ftBtn.getAttribute('data-rg-ft'), 10) || 0;
          renderEditorBody();
          return;
        }

        var patBtn = ev.target.closest('[data-rg-pat]');
        if (patBtn) {
          draft.patience = parseInt(patBtn.getAttribute('data-rg-pat'), 10) || 0;
          renderEditorBody();
          return;
        }

        var ctxBtn = ev.target.closest('[data-rg-ctx]');
        if (ctxBtn) {
          draft.context = ctxBtn.getAttribute('data-rg-ctx');
          renderEditorBody();
          return;
        }

        var affBtn = ev.target.closest('[data-rg-aff]');
        if (affBtn) {
          draft.affinity = affBtn.getAttribute('data-rg-aff');
          renderEditorBody();
          return;
        }

        var addRuleBtn = ev.target.closest('#rgAddRuleBtn');
        if (addRuleBtn) {
          draft.rules.push({
            when: 'prompt',
            pattern: '',
            use: draft.members[0] || ''
          });
          renderEditorBody();
          return;
        }

        var remRuleBtn = ev.target.closest('[data-rule-remove]');
        if (remRuleBtn) {
          var rIdx = parseInt(remRuleBtn.getAttribute('data-rule-remove'), 10);
          draft.rules.splice(rIdx, 1);
          renderEditorBody();
          return;
        }

        var effBtn = ev.target.closest('[data-rg-effort]');
        if (effBtn) {
          draft.effort = effBtn.getAttribute('data-rg-effort');
          renderEditorBody();
          return;
        }

        var lvResetBtn = ev.target.closest('[data-rg-level-reset]');
        if (lvResetBtn) {
          draft.levels = [];
          renderEditorBody();
          return;
        }

        var lvBtn = ev.target.closest('[data-rg-level]');
        if (lvBtn) {
          var lv = lvBtn.getAttribute('data-rg-level');
          var cur = Array.isArray(draft.levels) ? draft.levels.slice() : [];
          var pos = cur.indexOf(lv);
          if (pos >= 0) cur.splice(pos, 1);
          else cur.push(lv);
          draft.levels = cur;
          renderEditorBody();
        }
      });

      // Drag and drop reordering on .fbl
      body.addEventListener('dragstart', function (ev) {
        var row = ev.target.closest('.fbl > .fbrow[data-member-idx]');
        if (!row) return;
        dragFromIndex = parseInt(row.getAttribute('data-member-idx'), 10);
        row.classList.add('dragging');
      });
      body.addEventListener('dragend', function (ev) {
        var row = ev.target.closest('.fbl > .fbrow');
        if (row) row.classList.remove('dragging');
        dragFromIndex = -1;
      });
      body.addEventListener('dragover', function (ev) {
        var row = ev.target.closest('.fbl > .fbrow[data-member-idx]');
        if (!row || dragFromIndex < 0) return;
        ev.preventDefault();
      });
      body.addEventListener('drop', function (ev) {
        var row = ev.target.closest('.fbl > .fbrow[data-member-idx]');
        if (!row || dragFromIndex < 0 || !draft) return;
        ev.preventDefault();
        var toIndex = parseInt(row.getAttribute('data-member-idx'), 10);
        if (toIndex !== dragFromIndex && toIndex >= 0 && toIndex < draft.members.length) {
          var moved = draft.members.splice(dragFromIndex, 1)[0];
          draft.members.splice(toIndex, 0, moved);
          renderMembersSection();
        }
        dragFromIndex = -1;
      });
    }

    // Model picker popover events
    var pickerEl = document.getElementById('rgModelPicker');
    if (pickerEl) {
      pickerEl.addEventListener('input', function (ev) {
        if (ev.target && ev.target.id === 'rgPickerSearch') {
          pickerState.query = ev.target.value;
          renderModelPickerList();
        }
      });

      pickerEl.addEventListener('click', function (ev) {
        if (!draft) return;
        var railBtn = ev.target.closest('button[data-rail]');
        if (railBtn) {
          pickerState.filterProvider = railBtn.getAttribute('data-rail') || 'all';
          renderModelPickerList();
          return;
        }

        var favBtn = ev.target.closest('button[data-picker-fav]');
        if (favBtn) {
          ev.stopPropagation();
          rg.toggleFavorite(favBtn.getAttribute('data-picker-fav'));
          renderModelPickerList();
          return;
        }

        var itemLi = ev.target.closest('li[data-picker-ref]');
        if (itemLi) {
          var ref = itemLi.getAttribute('data-picker-ref');
          var idx = draft.members.indexOf(ref);
          if (idx >= 0) draft.members.splice(idx, 1);
          else draft.members.push(ref);
          renderMembersSection();
          renderModelPickerList();
        }
      });
    }

    // Close model picker when clicking outside
    document.addEventListener('mousedown', function (ev) {
      if (!pickerState.open) return;
      var pop = document.getElementById('rgModelPicker');
      var addBtn = document.getElementById('rgAddModelBtn');
      if (pop && pop.contains(ev.target)) return;
      if (addBtn && addBtn.contains(ev.target)) return;
      closeModelPicker();
    });

    // Routing groups list card actions
    var container = document.getElementById('routingGroups');
    if (container) {
      container.addEventListener('click', async function (ev) {
        var copyBadge = ev.target.closest('[data-copy-call]');
        if (copyBadge) {
          var text = copyBadge.getAttribute('data-copy-call');
          if (navigator.clipboard && navigator.clipboard.writeText) {
            navigator.clipboard.writeText(text).catch(function () {});
          }
          rg.showStatus(rg.tr('Copied ') + text, false);
          return;
        }

        var toggleBtn = ev.target.closest('[data-rg-toggle]');
        if (toggleBtn) {
          var id = toggleBtn.getAttribute('data-rg-toggle');
          var nextOn = !toggleBtn.classList.contains('on');
          try {
            await rg.apiRequest('/api/routing-groups/switch', { method: 'POST', body: { id: id, enabled: nextOn } });
            await rg.loadRoutingGroups(true);
          } catch (err) {
            rg.showStatus(err && err.message ? err.message : String(err), true);
          }
          return;
        }

        var editBtn = ev.target.closest('[data-rg-edit]');
        if (editBtn) {
          var editId = editBtn.getAttribute('data-rg-edit');
          var g = (rg.state.groups || []).find(function (item) { return item.id === editId; });
          if (g) openEditor(g, false);
          return;
        }

        var pinBtn = ev.target.closest('[data-rg-pin]');
        if (pinBtn) {
          var pinId = pinBtn.getAttribute('data-rg-pin');
          var gPin = (rg.state.groups || []).find(function (item) { return item.id === pinId; });
          if (gPin) openEditor(gPin, true);
          return;
        }

        var delCardBtn = ev.target.closest('[data-rg-delete]');
        if (delCardBtn) {
          deleteGroup(delCardBtn.getAttribute('data-rg-delete'));
          return;
        }

        var chip = ev.target.closest('.rt-member-chip[data-member][data-group-id]');
        if (chip) {
          var gId = chip.getAttribute('data-group-id');
          var member = chip.getAttribute('data-member');
          var group = (rg.state.groups || []).find(function (item) { return item.id === gId; });
          if (group && rg.normalizeStrategy(group.routing) === 'manual') {
            try {
              await rg.apiRequest('/api/routing-groups', {
                method: 'POST',
                body: Object.assign({}, group, { id: group.id.replace(/^auto-/, ''), pick: member, active: member })
              });
              await rg.loadRoutingGroups(true);
              var info = rg.describeMember(member);
              rg.showStatus(rg.tr('Active model switched to ') + info.modelName, false);
            } catch (err) {
              rg.showStatus(err && err.message ? err.message : String(err), true);
            }
          }
        }
      });
    }
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () {
      bindEvents();
      rg.loadRoutingGroups(true);
    });
  } else {
    bindEvents();
    rg.loadRoutingGroups(true);
  }
})();
