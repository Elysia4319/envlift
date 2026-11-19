/* envlift GUI 前端
 *
 * 硬约束（D14 / ③ 步 C2）：
 *   - 纯 vanilla JS，无框架、无构建、无 npm、无任何外链资源；
 *   - token 只存在于内存，不落任何浏览器持久存储，用完即从地址栏抹掉；
 *   - 不拼命令字符串、不实现业务逻辑：命令、层级、匹配全部来自 /api/v1；
 *   - 错误按稳定 code 分支，不做自然语言匹配。
 */
(function () {
  'use strict';

  /* ---------------- 常量 ---------------- */

  var TOKEN_HEADER = 'X-Envlift-Token';

  // 只允许使用已存在的路由（C2：不为页面方便新增「前端专用」接口）。
  var R = {
    status: '/api/v1/status',
    scan: '/api/v1/scan',
    scanCommit: '/api/v1/scan/commit',
    configCurrent: '/api/v1/config/current',
    plan: '/api/v1/plan',
    apply: '/api/v1/apply',
    task: function (id) { return '/api/v1/task/' + encodeURIComponent(id); },
    taskCancel: function (id) { return '/api/v1/task/' + encodeURIComponent(id) + '/cancel'; },
    reports: '/api/v1/reports',
    report: function (name, fmt) {
      return '/api/v1/reports/' + encodeURIComponent(name) + '?format=' + fmt;
    },
    reportDiff: function (a, b) {
      return '/api/v1/reports/diff?a=' + encodeURIComponent(a) + '&b=' + encodeURIComponent(b);
    },
    decisions: '/api/v1/decisions',
    rollback: '/api/v1/rollback',
    quit: '/api/quit'
  };

  // 稳定错误码 → 给人看的一句话。分支只认 code，不认 message。
  var CODE_HINT = {
    unauthorized: '当前 token 错误/失效/未生成。请点击启动时终端里的完整地址重新开启GUI。',
    task_busy: '已有任务在跑。等它结束，或先取消它再提交。',
    plan_stale: '配置或环境在预览之后变了。重新生成一次计划，别用旧哈希执行。',
    plan_stale_network: '网络状态和生成计划时不一致。重新走一次预演再决定。',
    invalid_mode: 'apply 必须显式声明 mode（all / skip_admin）。',
    admin_confirmation_missing: '有计划项需要管理员运行，但没逐项确认。',
    skipped_admin_ack_missing: '选择了「只跑无需管理员运行的部分」，但没有确认知道需要管理员运行的项会被跳过。',
    reboot_ack_required: '有计划项执行后会重启，必须先确认。',
    config_schema_too_new: '配置的 schema 版本比本程序新。升级 envlift，别降级读。',
    path_forbidden: '写入路径不在白名单内（只允许配置目录、reports、journal 目录）。',
    not_found: '找不到目标：文件或任务不存在。',
    bad_request: '请求内容不合法。',
    invalid_decisions: '决策不合法：下面逐条说明了哪一条、为什么。',
    decision_target_not_found: '决策引用的条目在计划里找不到（配置或环境变了）。撤销这几条决策后重新生成计划。',
    invalid_decision_params: '决策的参数不合法（例如路径格式不对、安装包不在候选里）。',
    unknown_target: '引用的条目不存在。',
    internal: '程序内部出错了。'
  };

  var TASK_TERMINAL = ['succeeded', 'failed', 'cancelled', 'awaiting_reboot'];
  var TASK_STATE_TEXT = {
    queued: '排队中',
    running: '执行中',
    succeeded: '已完成',
    failed: '失败',
    cancelled: '已取消',
    awaiting_reboot: '等待重启'
  };

  var state = {
    token: '',
    status: null,
    busy: false,
    lastRetry: null,
    scan: null,
    scanTaskId: '',
    scanLogSeq: 0,
    autoScroll: true,
    // excluded：下标 → 被排除。勾选框表示「纳入」，默认全部纳入（忠实快照）。
    excluded: {},
    commitOut: 'envlift.yaml',
    limitUnrecognized: 20,
    limitRecognized: 20,
    plan: null,
    planHash: '',
    // ④ 决策：本机选择，随每次 plan 请求一起提交（不入配置、不进浏览器存储）。
    decisions: [],
    // 二次确认面板的选择（D15：mode 必须显式，默认 all 但需要用户真的点执行）。
    confirm: { mode: 'all', ackSkipped: false, rebootAck: false, admin: {} },
    applying: false,
    journals: []
  };

  /* ---------------- 小工具 ---------------- */

  /* TODO(ui)：本文件的渲染函数里仍有 48 处内联 style="..."（主要是
     margin / 局部 flex 微调）。推右与常规按钮行已收口到 .push-right /
     .row-actions；其余内联值后续做深色主题或再调间距时会压过 CSS 变量，
     必须优先改成 class。改动时请一并搜索：
       grep -n 'style="' internal/webui/static/app.js
     重点位置：renderPlan / confirmSection / renderScanResult / listHtml。 */

  function $(id) { return document.getElementById(id); }

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function sleep(ms) { return new Promise(function (r) { setTimeout(r, ms); }); }

  function hhmmss(rfc3339) {
    var m = /T(\d\d:\d\d:\d\d)/.exec(rfc3339 || '');
    return m ? m[1] : '';
  }

  function setText(el, text) { if (el) { el.textContent = text; } }

  function show(el, on) { if (el) { el.hidden = !on; } }

  // 数值速览：{[标签]: [数值, 小字说明]} 的数组 → 一行账（三处共用，别各写一份）。
  function tallyHtml(items) {
    return '<div class="tally">' + items.map(function (it) {
      var small = it[2] ? '<small> ' + esc(it[2]) + '</small>' : '';
      return '<div><dl><dt>' + esc(it[0]) + '</dt><dd>' + esc(it[1]) + small + '</dd></dl></div>';
    }).join('') + '</div>';
  }

  // 注册表值类型：给人看的名字，别把数字 2 直接甩到界面上。
  var REG_KIND = { 1: 'REG_SZ', 2: 'REG_EXPAND_SZ' };
  function regKindName(k) {
    return REG_KIND[k] || ('类型 ' + k);
  }

  /* ---------------- token ---------------- */

  function initToken() {
    var q = new URLSearchParams(location.search);
    var t = q.get('token') || '';
    if (t) {
      state.token = t;
      // 立刻从地址栏抹掉：不留在历史记录里，也不进任何后续跳转。
      q.delete('token');
      var qs = q.toString();
      history.replaceState(null, '', location.pathname + (qs ? '?' + qs : '') + location.hash);
      return true;
    }
    return false;
  }

  /* ---------------- HTTP ---------------- */

  function api(path, opts) {
    opts = opts || {};
    var init = { method: opts.method || 'GET', headers: {} };
    if (state.token) { init.headers[TOKEN_HEADER] = state.token; }
    if (opts.body !== undefined && opts.body !== null) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(opts.body);
    }
    return fetch(path, init).then(function (res) {
      return res.text().then(function (text) {
        // raw：正文不是 JSON（md 原文），原样返回字符串。
        if (opts.raw && res.ok) { return text; }
        var data = null;
        if (text) { try { data = JSON.parse(text); } catch (e) { data = null; } }
        if (res.ok) { return data; }
        var err = (data && data.error) || {};
        var e = new Error(err.message || text || ('HTTP ' + res.status));
        e.code = err.code || ('http_' + res.status);
        e.details = err.details;
        e.status = res.status;
        throw e;
      });
    });
  }

  /* ---------------- 错误条 ---------------- */

  function showError(err, retry) {
    state.lastRetry = typeof retry === 'function' ? retry : null;
    setText($('errbar-code'), err.code || 'error');
    setText($('errbar-msg'), err.message || '未知错误');
    var hint = CODE_HINT[err.code] || '';
    if (!hint && err.status >= 500) { hint = CODE_HINT.internal; }
    if (err.code === 'task_busy' && err.details && err.details.task_id) {
      hint = '正在跑的任务是 ' + err.details.task_id + '。' + hint;
    }
    setText($('errbar-hint'), hint);
    show($('errbar-retry'), !!state.lastRetry);
    show($('errbar'), true);
  }

  function clearError() {
    state.lastRetry = null;
    show($('errbar'), false);
  }

  /* ---------------- 任务轮询（增量） ---------------- */

  function pollTask(id, onUpdate, isCancelled) {
    var since = 0;
    function step() {
      if (isCancelled && isCancelled()) {
        return Promise.resolve({ id: id, state: 'cancelled', logs: [] });
      }
      return api(R.task(id) + '?since=' + since).then(function (snap) {
        // since 的语义是「已收到的最大 seq」（后端按 Seq > since 过滤），
        // 不是「下一个 seq」——用 next_seq 直接当 since 会永久漏掉第一条日志。
        var last = since;
        (snap.logs || []).forEach(function (l) { if (l.seq > last) { last = l.seq; } });
        if (snap.next_seq) { last = Math.max(last, snap.next_seq - 1); }
        since = last;
        if (onUpdate) { onUpdate(snap); }
        if (TASK_TERMINAL.indexOf(snap.state) >= 0) { return snap; }
        return sleep(350).then(step);
      });
    }
    return step();
  }

  function startTask(path, body, onUpdate, isCancelled) {
    return api(path, { method: 'POST', body: body || {} }).then(function (r) {
      if (onUpdate) { onUpdate({ id: r.task_id, state: 'queued' }); }
      return pollTask(r.task_id, onUpdate, isCancelled);
    });
  }

  /* ---------------- 顶部状态条 ---------------- */

  function renderStatus(s) {
    state.status = s;
    var facts = [
      ['系统', (s.os || '?') + ' · build ' + (s.build || '?')],
      ['架构', s.arch || '?'],
      ['用户', (s.username || '?') + '@' + (s.hostname || '?')],
      ['winget', s.winget_ok ? (s.winget_version || '可用') : '不可用'],
      ['网络', s.network_ok ? '可达' : '不可达'],
      ['配置', s.config_path || '?']
    ];
    var html = facts.map(function (f) {
      return '<div class="fact"><dt>' + esc(f[0]) + '</dt><dd title="' + esc(f[1]) + '">' + esc(f[1]) + '</dd></div>';
    }).join('');
    $('facts').innerHTML = html;

    var elev = $('badge-elev');
    if (s.elevated) {
      elev.setAttribute('data-tone', 'ok');
      elev.textContent = '管理员运行';
      elev.title = '系统级改动（HKLM PATH）可以执行，但仍会逐项询问';
    } else {
      elev.setAttribute('data-tone', 'warn');
      elev.textContent = '未以管理员运行';
      elev.title = '需要管理员运行的项会被阻止。要改系统级 PATH 请以管理员身份重新运行 envlift';
    }

    if (s.product_name && s.os && s.product_name.indexOf(s.os) < 0) {
      elev.title += '（注册表 ProductName=' + s.product_name + '，与 build 判定不一致，仅作参考）';
    }
  }

  function loadStatus() {
    return api(R.status).then(renderStatus).catch(function (e) {
      setText($('fact-boot'), '未连接');
      showError(e, loadStatus);
    });
  }

  /* ---------------- 标签页 ---------------- */

  var TABS = ['tab-scan', 'tab-restore', 'tab-path', 'tab-report'];
  var PANELS = {
    'tab-scan': 'panel-scan', 'tab-restore': 'panel-restore',
    'tab-path': 'panel-path', 'tab-report': 'panel-report'
  };

  // 桌面是左侧竖排导航，窄屏（≤820px）降级为顶部横排。
  // 方向键必须跟着方向走，否则竖排里按上下键没反应。
  var NAV_NARROW = window.matchMedia('(max-width: 820px)');

  function syncNavOrientation() {
    var nav = document.querySelector('.tabs');
    if (nav) { nav.setAttribute('aria-orientation', NAV_NARROW.matches ? 'horizontal' : 'vertical'); }
  }

  function selectTab(id, focus) {
    TABS.forEach(function (t) {
      var on = t === id;
      $(t).setAttribute('aria-selected', on ? 'true' : 'false');
      $(t).tabIndex = on ? 0 : -1;
      show($(PANELS[t]), on);
    });
    if (focus) { $(id).focus(); }
    if (id === 'tab-path') { renderPathPanel(); }
    if (id === 'tab-report') {
      // 进页先拉一次列表；已有详情/对比时不要覆盖它（用户可能正在看）。
      if (!rep.detail && !rep.diff) { loadReports(); } else { renderReports(); }
    }
  }

  function initTabs() {
    syncNavOrientation();
    if (NAV_NARROW.addEventListener) {
      NAV_NARROW.addEventListener('change', syncNavOrientation);
    } else if (NAV_NARROW.addListener) {
      NAV_NARROW.addListener(syncNavOrientation);
    }
    TABS.forEach(function (id, i) {
      $(id).addEventListener('click', function () { selectTab(id); });
      $(id).addEventListener('keydown', function (ev) {
        var next = null;
        var horizontal = NAV_NARROW.matches;
        var fwd = horizontal ? 'ArrowRight' : 'ArrowDown';
        var back = horizontal ? 'ArrowLeft' : 'ArrowUp';
        if (ev.key === fwd) { next = TABS[(i + 1) % TABS.length]; }
        if (ev.key === back) { next = TABS[(i - 1 + TABS.length) % TABS.length]; }
        if (ev.key === 'Home') { next = TABS[0]; }
        if (ev.key === 'End') { next = TABS[TABS.length - 1]; }
        if (next) { ev.preventDefault(); selectTab(next, true); }
      });
    });
  }

  /* ---------------- 扫描 ---------------- */

  var scanPhases = [];

  function resetScanRun() {
    scanPhases = [];
    $('scan-rail').innerHTML = '';
    $('scan-log').textContent = '';
    $('scan-bar').style.width = '0%';
    setText($('scan-progress-text'), '');
    setText($('scan-state-note'), '');
  }

  function updatePhase(snap) {
    if (snap.phase) {
      if (scanPhases.indexOf(snap.phase) < 0) { scanPhases.push(snap.phase); }
    }
    var cur = snap.phase || (scanPhases.length ? scanPhases[scanPhases.length - 1] : '');
    $('scan-rail').innerHTML = scanPhases.map(function (p) {
      var st = p === cur ? (TASK_TERMINAL.indexOf(snap.state) >= 0 ? 'done' : 'active') : 'done';
      return '<li data-state="' + st + '">' + esc(p) + '</li>';
    }).join('');
    if (snap.total > 0) {
      var pct = Math.round((snap.done / snap.total) * 100);
      $('scan-bar').style.width = Math.max(4, Math.min(100, pct)) + '%';
      setText($('scan-progress-text'), snap.done + '/' + snap.total + ' · ' + pct + '%');
    }
    if (snap.state) { setText($('scan-state-note'), TASK_STATE_TEXT[snap.state] || snap.state); }
  }

  function appendLogs(snap) {
    var box = $('scan-log');
    (snap.logs || []).forEach(function (l) {
      var line = document.createElement('div');
      line.className = 'l-' + (l.level || 'info');
      line.innerHTML = '<span class="l-time">' + esc(hhmmss(l.time)) + '</span>  ' + esc(l.message);
      box.appendChild(line);
    });
    if (state.autoScroll) { box.scrollTop = box.scrollHeight; }
  }

  function setBusy(on) {
    state.busy = on;
    $('btn-scan').disabled = on || !state.token;
    $('btn-plan').disabled = on || !state.token;
    $('btn-config-current').disabled = on || !state.token;
    // 写盘也是一次提交：扫描/计划在跑时不许并发提交，否则两个请求互相踩。
    var cr = $('btn-commit-rename'), co = $('btn-commit-overwrite');
    if (cr) { cr.disabled = on || !state.token; }
    if (co) { co.disabled = on || !state.token; }
    show($('btn-scan-cancel'), on);
  }

  function doScan() {
    clearError();
    resetScanRun();
    show($('scan-run'), true);
    show($('scan-idle'), false);
    setBusy(true);

    startTask(R.scan, {
      raw: $('scan-raw').checked,
      keep_pip_bootstrap: $('scan-keep-pip-bootstrap').checked
    }, function (snap) {
      if (snap.id) { state.scanTaskId = snap.id; }
      updatePhase(snap);
      appendLogs(snap);
    }).then(function (snap) {
      setBusy(false);
      if (snap.state === 'succeeded' && snap.result) {
        renderScanResult(snap.result);
      } else if (snap.state === 'failed') {
        showError({ code: 'task_failed', message: snap.error || '扫描失败' }, doScan);
      } else if (snap.state === 'cancelled') {
        setText($('scan-state-note'), '已取消');
      }
    }).catch(function (e) {
      setBusy(false);
      showError(e, doScan);
    });
  }

  function markChip(v) {
    return '<span class="mark" data-mark="' + esc(v) + '">' + esc(v) + '</span>';
  }

  function renderScanResult(payload) {
    state.scan = payload;
    var cfg = payload.config || {};
    var stats = payload.stats || {};
    var rec = payload.recognition || null;
    var out = [];
    var sm = cfg.source_machine || {};

    // —— 配方覆盖概览（未识别区排在最前面：这是要人注意的部分）——
    // 覆盖情况由内核算（match.Summarize）；拿不到就明说，绝不显示成「0 条全没配方」。
    var recByIndex = {};
    if (rec) {
      (rec.entries || []).forEach(function (e) { recByIndex[e.index] = e; });
    }

    var sw = cfg.software || [];
    // 稳定标识对照表（内核算好的）；被排除的条目靠它翻译成 target_id。
    var idByIndex = {};
    (payload.targets || []).forEach(function (t) { idByIndex[t.index] = t.target_id; });
    var unrec = [], recog = [];
    sw.forEach(function (s, i) {
      var info = recByIndex[i] || { basis: 'none' };
      var item = { sw: s, i: i, info: info, targetId: idByIndex[i] || '' };
      if (info.recipe && !(info.ambiguous && info.ambiguous.length)) { recog.push(item); }
      else { unrec.push(item); }
    });

    // 滤除原因是个 map（按过滤规则计数），不是数字；把分解如实列出来。
    var filteredBreakdown = '';
    if (stats.filtered && typeof stats.filtered === 'object') {
      filteredBreakdown = Object.keys(stats.filtered).filter(function (k) {
        return stats.filtered[k] > 0;
      }).map(function (k) {
        return k + ' ' + stats.filtered[k];
      }).join(' / ');
    }

    var tally = [
      ['软件条目', String(sw.length)],
      ['原始', String(stats.raw || 0), '滤除 ' + Object.keys(stats.filtered || {}).reduce(function (a, k) {
        return a + (stats.filtered[k] || 0);
      }, 0) + ' 条']
    ];
    if (rec) {
      tally.push(['配方已识别', String(rec.recognized || 0)]);
      tally.push(['未识别', String(rec.unrecognized || 0),
        '其中 ' + (rec.unrecognized_with_winget || 0) + ' 条有 winget id']);
      tally.push(['配方表', String(rec.recipes || 0), '条']);
    }
    out.push('<div class="section"><div class="section-head"><h2>本次扫描</h2></div>'
      + '<div class="section-body">' + tallyHtml(tally)
      + (filteredBreakdown ? '<p class="hint" style="margin:10px 0 0">滤除原因：' + esc(filteredBreakdown) + '</p>' : '')
      + '</div></div>');

    if (!rec) {
      out.push('<div class="section"><div class="section-body"><div class="guard">'
        + '<strong>配方覆盖情况不可用。</strong>'
        + esc(payload.recognition_error || '内核没有返回覆盖统计')
        + ' 下面的「未识别 / 已识别」分区因此不可信，请先修好配方表再看这份扫描。'
        + '</div></div></div>');
    }

    // —— 系统信息 ——
    var os = sm.os || {};
    // 注册表 ProductName 不可靠（实测 build 26300 却报 Windows 10 Home China），
    // 与 build 判定冲突时把话说清楚，别让两个值并列摆着让人自己猜信哪个。
    var pn = os.product_name || '—';
    if (os.product_name && os.version && os.product_name.indexOf(os.version) < 0) {
      pn += '（与 build 判定不一致，仅作参考）';
    }
    out.push('<div class="section"><div class="section-head"><h2>系统信息</h2></div><div class="section-body tight"><div class="table-wrap"><table class="ledger"><tbody>'
      + row2('操作系统', (os.version || '?') + '（build ' + (os.build || '?') + '）')
      + row2('注册表名', pn + (os.display_version ? ' · ' + os.display_version : ''))
      + row2('架构', sm.arch || '?')
      + row2('用户名', sm.username || '（已脱敏）')
      + row2('主机名', sm.hostname || '（已脱敏）')
      + row2('生成时间', cfg.generated_at || '?')
      + '</tbody></table></div></div></div>');

    // —— PATH ——
    var path = cfg.path || {};
    ['user', 'machine'].forEach(function (sec) {
      var list = path[sec] || [];
      var label = sec === 'user' ? 'PATH · 用户级' : 'PATH · 系统级';
      var rows = list.map(function (e, i) {
        return '<tr><td class="num">' + (i + 1) + '</td><td class="data">' + esc(e.value) + '</td>'
          + '<td>' + (e.expand ? '<span class="chip" data-tone="skip">REG_EXPAND_SZ</span>' : '<span class="chip" data-tone="skip">REG_SZ</span>') + '</td></tr>';
      }).join('');
      if (!rows) { rows = '<tr><td colspan="3" class="hint">（无）</td></tr>'; }
      out.push('<div class="section"><div class="section-head"><h2>' + esc(label) + '</h2>'
        + '<span class="count">' + list.length + ' 条</span></div>'
        + '<div class="section-body tight"><div class="table-wrap"><table class="ledger path-table">'
        + '<thead><tr><th>#</th><th>值（注册表原值，未展开 %VAR%）</th><th>值类型</th></tr></thead>'
        + '<tbody>' + rows + '</tbody></table></div></div>'
        + '<div class="section-foot"><span class="hint">只采持久值，不采当前会话 PATH（D2）——会话里常混着临时注入与僵尸路径。</span></div></div>');
    });

    // —— 运行时 ——
    var rts = cfg.runtimes || [];
    var rtRows = rts.map(function (r) {
      var from = r.resolved_from ? r.resolved_from + (r.resolved_index != null ? '#' + r.resolved_index : '') : '—';
      return '<tr><td class="name">' + esc(r.name) + '</td><td class="data">' + esc(r.command || '') + '</td>'
        + '<td class="code">' + esc(r.version || 'unknown') + '</td>'
        + '<td>' + (r.manager ? '<span class="chip" data-tone="warn">' + esc(r.manager) + '</span>' : '<span class="hint">—</span>') + '</td>'
        + '<td class="data">' + esc(r.executable || '—') + '</td><td class="code">' + esc(from) + '</td></tr>';
    }).join('');
    if (!rtRows) { rtRows = '<tr><td colspan="6" class="hint">（无）</td></tr>'; }
    out.push('<div class="section"><div class="section-head"><h2>语言运行时</h2><span class="count">' + rts.length + ' 个</span></div>'
      + '<div class="section-body tight"><div class="table-wrap"><table class="ledger">'
      + '<thead><tr><th>名称</th><th>命令</th><th>版本</th><th>托管</th><th>可执行文件</th><th>解析来源</th></tr></thead>'
      + '<tbody>' + rtRows + '</tbody></table></div></div>'
      + '<div class="section-foot"><span class="hint">版本一律取自实际执行 --version，不信目录名（实测目录名会骗人）。</span></div></div>');

    // —— conda 环境 ——
    var cenvs = cfg.conda_envs || [];
    var mirrorNames = Object.keys(cfg.mirrors || {});
    var condaRows = cenvs.map(function (e, i) {
      var isBase = !!e.base;
      var condaOn = isBase ? false : (e.restore_conda !== false);
      var pipOn = isBase ? false : (e.restore_pip !== false);
      var mirrorOpts = ['<option value="">默认源</option>'].concat(mirrorNames.map(function (name) {
        return '<option value="' + esc(name) + '"' + (e.mirror === name ? ' selected' : '') + '>' + esc(name) + '</option>';
      }));
      return '<tr>'
        + '<td class="name">' + esc(e.name) + (isBase ? ' <span class="chip" data-tone="manual">base 只读</span>' : '') + '</td>'
        + '<td class="code">' + esc(e.python_version || '—') + '</td>'
        + '<td class="data">' + esc(e.prefix || '—') + '</td>'
        + '<td class="num">' + ((e.packages || []).length) + '</td>'
        + '<td class="num">' + ((e.pip_packages || []).length) + '</td>'
        + '<td><input type="checkbox" class="conda-conda" data-ci="' + i + '"' + (condaOn ? ' checked' : '') + (isBase ? ' disabled' : '') + ' aria-label="恢复 conda 包"></td>'
        + '<td><input type="checkbox" class="conda-pip" data-ci="' + i + '"' + (pipOn ? ' checked' : '') + (isBase ? ' disabled' : '') + ' aria-label="恢复 pip 包"></td>'
        + '<td><select class="conda-mirror" data-ci="' + i + '"' + (isBase ? ' disabled' : '') + '>' + mirrorOpts.join('') + '</select></td>'
        + '</tr>';
    }).join('');
    if (!condaRows) { condaRows = '<tr><td colspan="8" class="hint">（未发现 conda 环境）</td></tr>'; }
    out.push('<div class="section"><div class="section-head"><h2>conda 环境</h2><span class="count">' + cenvs.length + ' 个</span></div>'
      + '<div class="section-body tight"><div class="table-wrap"><table class="ledger">'
      + '<thead><tr><th>环境</th><th>Python</th><th>前缀</th><th>conda 包</th><th>pip 包</th><th>恢复 conda</th><th>恢复 pip</th><th>镜像</th></tr></thead>'
      + '<tbody>' + condaRows + '</tbody></table></div></div>'
      + '<div class="section-foot"><span class="hint">base 默认只读；命名环境可分别关闭 conda/pip。镜像 profile 需先在 <code>mirrors:</code> 中定义，不会硬编码默认清华源。</span></div></div>');

    // —— 未识别（排在已识别之前）——
    out.push(softwareSection('未识别', unrec, 'limitUnrecognized',
      '这些条目没有配方覆盖，还原时会落到 manual。带 winget id 的那些属于「配方缺口」——补一条配方就能自动装。',
      true));

    // —— 已识别 ——
    out.push(softwareSection('已识别', recog, 'limitRecognized',
      '配方靠四级锚点命中：winget_ids → aliases → match_display → name，命中即停（M2）。', false));

    // —— 写入配置 ——
    // 勾选状态是给 ④ 的决策回传用的，**不参与本次写盘**——写盘一律全量。
    // 所以文案必须说清楚，否则「已勾选 N 条」会被读成「只写了 N 条」。
    out.push('<div class="section"><div class="section-head"><h2>写入配置</h2>'
      + '<span class="count" id="commit-state"></span></div>'
      + '<div class="section-body"><div style="display:flex; gap:10px; align-items:center; flex-wrap:wrap">'
      + '<label class="field"><span class="hint">文件名</span><input type="text" id="commit-out" value="' + esc(state.commitOut) + '" spellcheck="false"></label>'
      + '<button class="btn" id="btn-commit-rename" type="button">另存为新文件名</button>'
      + '<button class="btn btn-ghost" id="btn-commit-overwrite" type="button">备份后覆盖</button>'
      + '</div><p class="hint" style="margin:10px 0 0">默认不覆盖已有文件。覆盖会先把原文件备份成 <code>.bak</code>。</p>'
      + '<p class="hint" style="margin:6px 0 0">取消勾选的条目不会删除，而是原样写进配置顶层 <code>x-excluded:</code> 区——不参与还原，但信息可恢复。</p>'
      + '<p class="hint" id="commit-result" style="margin:8px 0 0"></p></div></div>');

    var box = $('scan-result');
    box.innerHTML = out.join('');
    show(box, true);

    $('btn-commit-rename').addEventListener('click', function () { commitScan('rename'); });
    $('btn-commit-overwrite').addEventListener('click', function () { commitScan('backup_overwrite'); });
    $('commit-out').addEventListener('input', function () {
      state.commitOut = $('commit-out').value;
    });
    bindSoftwareControls(box);
    bindCondaControls(box);
    updateCommitCount();
    setBusy(state.busy); // 重渲染后按钮的禁用态要跟着当前忙碌状态走
  }

  function row2(k, v) {
    return '<tr><th scope="row" style="width:22%">' + esc(k) + '</th><td class="data">' + esc(v) + '</td></tr>';
  }

  function softwareSection(title, items, limitKey, note, isUnrec) {
    var limit = state[limitKey];
    var shown = items.slice(0, limit);
    var rows = shown.map(function (it) {
      var s = it.sw, info = it.info;
      var recipeCell;
      if (isUnrec) {
        if (info.ambiguous && info.ambiguous.length) {
          recipeCell = '<span class="chip" data-tone="warn">多配方</span> <span class="hint">' + esc(info.ambiguous.join(' / ')) + '</span>';
        } else if (s.winget_id) {
          recipeCell = '<span class="chip" data-tone="bad">配方缺口</span> <span class="code">' + esc(s.winget_id) + '</span>';
        } else {
          recipeCell = '<span class="chip" data-tone="manual">无映射</span>';
        }
      } else {
        recipeCell = '<span class="chip" data-tone="local">' + esc(info.recipe) + '</span> <span class="hint">' + esc(info.basis || '') + '</span>';
      }
      var srcs = (s.sources || []).map(function (x) {
        return '<span class="chip" data-tone="skip">' + esc(x) + '</span>';
      }).join(' ');
      var checked = state.excluded[it.i] ? '' : ' checked';
      return '<tr data-idx="' + it.i + '">'
        + '<td><input type="checkbox" class="pick" data-idx="' + it.i + '" data-target="' + esc(it.targetId || '') + '" aria-label="纳入 ' + esc(s.name) + '"' + checked + '></td>'
        + '<td class="name">' + esc(s.name) + '</td>'
        + '<td class="code">' + esc(s.version || '—') + '</td>'
        + '<td>' + (srcs || '<span class="hint">—</span>') + '</td>'
        + '<td>' + recipeCell + '</td></tr>';
    }).join('');
    if (!rows) { rows = '<tr><td colspan="5" class="hint">（无）</td></tr>'; }

    var more = '';
    if (items.length > limit) {
      more = '<button class="btn btn-ghost btn-sm more" data-limit-key="' + limitKey + '" type="button">加载更多（还有 '
        + (items.length - limit) + ' 条）</button>';
    }

    return '<div class="section"><div class="section-head">'
      + '<h2>' + esc(title) + '</h2><span class="count">' + items.length + ' 条</span>'
      + (more ? '<div class="push-right">' + more + '</div>' : '')
      + '</div><div class="section-body tight"><div class="table-wrap"><table class="ledger">'
      + '<thead><tr><th style="width:34px">纳入</th><th>名称（DisplayName 原值）</th><th>版本</th><th>来源</th><th>配方</th></tr></thead>'
      + '<tbody>' + rows + '</tbody></table></div></div>'
      + '<div class="section-foot"><span class="hint">' + esc(note) + '</span></div></div>';
  }

  function bindSoftwareControls(scope) {
    Array.prototype.forEach.call(scope.querySelectorAll('.pick'), function (cb) {
      cb.addEventListener('change', function () {
        var idx = cb.getAttribute('data-idx');
        // 勾 = 纳入；取消勾 = 排除（写入时进 x-excluded，不参与还原）。
        if (cb.checked) { delete state.excluded[idx]; } else { state.excluded[idx] = true; }
        updateCommitCount();
      });
    });
    Array.prototype.forEach.call(scope.querySelectorAll('.more'), function (btn) {
      btn.addEventListener('click', function () {
        var key = btn.getAttribute('data-limit-key');
        state[key] += 20;
        renderScanResult(state.scan);
      });
    });
  }

  function bindCondaControls(scope) {
    Array.prototype.forEach.call(scope.querySelectorAll('.conda-conda'), function (cb) {
      cb.addEventListener('change', function () {
        var i = parseInt(cb.getAttribute('data-ci'), 10);
        var list = (state.scan && state.scan.config && state.scan.config.conda_envs) || [];
        if (list[i]) { list[i].restore_conda = cb.checked; }
      });
    });
    Array.prototype.forEach.call(scope.querySelectorAll('.conda-pip'), function (cb) {
      cb.addEventListener('change', function () {
        var i = parseInt(cb.getAttribute('data-ci'), 10);
        var list = (state.scan && state.scan.config && state.scan.config.conda_envs) || [];
        if (list[i]) { list[i].restore_pip = cb.checked; }
      });
    });
    Array.prototype.forEach.call(scope.querySelectorAll('.conda-mirror'), function (sel) {
      sel.addEventListener('change', function () {
        var i = parseInt(sel.getAttribute('data-ci'), 10);
        var list = (state.scan && state.scan.config && state.scan.config.conda_envs) || [];
        if (list[i]) { list[i].mirror = sel.value; }
      });
    });
  }

  function updateCommitCount() {
    var n = Object.keys(state.excluded).length;
    var text = n === 0
      ? '全部纳入（忠实快照）'
      : '已排除 ' + n + ' 条 → 写入配置顶层 x-excluded 区，不参与还原';
    setText($('commit-state'), text);
  }

  // excludeIDs 把被排除的下标翻译成稳定 target_id（前端不自己算哈希）。
  function excludeIDs() {
    var byIndex = {};
    ((state.scan && state.scan.targets) || []).forEach(function (t) { byIndex[t.index] = t.target_id; });
    var out = [];
    Object.keys(state.excluded).forEach(function (idx) {
      var id = byIndex[idx];
      if (id) { out.push(id); }
    });
    return out;
  }

  function commitScan(onExist) {
    clearError();
    var out = (state.commitOut || 'envlift.yaml').trim() || 'envlift.yaml';
    setText($('commit-result'), '正在写入…');
    setBusy(true);
    var condaSettings = (((state.scan && state.scan.config) || {}).conda_envs || []).map(function (e) {
      return {
        name: e.name,
        manager: e.manager || '',
        restore_conda: e.base ? false : (e.restore_conda !== false),
        restore_pip: e.base ? false : (e.restore_pip !== false),
        mirror: e.mirror || ''
      };
    });
    api(R.scanCommit, {
      method: 'POST',
      body: { task_id: state.scanTaskId, out: out, on_exist: onExist, exclude: excludeIDs(), conda: condaSettings }
    }).then(function (r) {
      setBusy(false);
      var cfg = (state.scan && state.scan.config) || {};
      var pathCount = ((cfg.path || {}).user || []).length + ((cfg.path || {}).machine || []).length;
      setText($('commit-result'),
        '已写入 ' + r.written
        + ' · 采到 ' + r.total + ' 条，其中已排除 ' + r.excluded + ' 条（在 x-excluded 区）'
        + ' · PATH ' + pathCount + ' 条');
    }).catch(function (e) {
      setBusy(false);
      setText($('commit-result'), '');
      showError(e, function () { commitScan(onExist); });
    });
  }

  /* ---------------- 还原：配置与计划 ---------------- */

  function loadCurrentConfig() {
    clearError();
    api(R.configCurrent).then(function (cfg) {
      var box = $('plan-load-note');
      box.innerHTML = tallyHtml([
        ['schema', 'v' + (cfg.schema || '?')],
        ['软件条目', String((cfg.software || []).length)],
        ['conda 环境', String((cfg.conda_envs || []).length)],
        ['PATH 用户级', String(((cfg.path || {}).user || []).length)],
        ['PATH 系统级', String(((cfg.path || {}).machine || []).length)],
        ['生成时间', String(cfg.generated_at || '?')]
      ]);
    }).catch(function (e) {
      setText($('plan-load-note'), '');
      showError(e, loadCurrentConfig);
    });
  }

  function doPlan(keepError) {
    if (!keepError) { clearError(); }
    setBusy(true);
    var cfgPath = ($('plan-config').value || 'envlift.yaml').trim();
    setText($('plan-load-note'), '生成计划中…');
    // D1：plan 同步返回；决策不合法会当场 400 并带 details.issues。
    return api(R.plan, { method: 'POST', body: { config: cfgPath, decisions: state.decisions } }).then(function (res) {
      setBusy(false);
      state.planHash = res.plan_hash || '';
      state.plan = res.plan || null;
      // 计划内容变了，之前勾的提权确认一律作废（D1）。
      resetConfirm();
      // 生成过程中这里滚动显示 phase 文本；结束后必须换掉，
      // 否则页面上会一直挂着一句「生成计划中：生成计划」。
      setText($('plan-load-note'),
        state.plan
          ? '计划已生成：' + ((state.plan.items || []).length) + ' 项 · 目标 '
            + ((state.plan.target || {}).os || '?') + ' / ' + ((state.plan.target || {}).arch || '?')
          : '');
      renderPlan();
      return true;
    }).catch(function (e) {
      setBusy(false);
      showError(e, doPlan);
      planFailure(e);
      return false;
    });
  }

  // 计划生成被决策挡下时，页面不能只剩一条错误条：用户需要一条出路。
  // 决策可能来自 envlift.decisions.yaml（例如配置换了、id 失效），所以给「清空」，
  // 而不是让用户去翻文件删。
  function planFailure(e) {
    var code = e && e.code;
    var decisionCodes = ['invalid_decisions', 'decision_target_not_found', 'invalid_decision_params'];
    if (decisionCodes.indexOf(code) < 0) { return; }
    var issues = (e.details && e.details.issues) || [];
    var box = $('plan-result');
    box.innerHTML = '<div class="section"><div class="section-head"><h2>决策无法应用</h2></div>'
      + '<div class="section-body"><div class="guard"><strong>' + esc(code) + '</strong> '
      + esc(e.message || '') + '</div>'
      + (issues.length
        ? '<ul style="margin:10px 0 0; padding-left:20px">' + issues.map(function (is) {
          return '<li>第 ' + esc(String(is.index || '?')) + ' 条'
            + (is.target_id ? '（<code>' + esc(is.target_id) + '</code>）' : '')
            + '：' + esc(is.reason || '') + '</li>';
        }).join('') + '</ul>'
        : '')
      + '<div style="margin-top:12px; display:flex; gap:10px; flex-wrap:wrap">'
      + '<button class="btn" id="btn-clear-decisions" type="button">清空全部决策并重新生成</button>'
      + '</div></div></div>';
    show(box, true);
    $('btn-clear-decisions').addEventListener('click', function () {
      state.decisions = [];
      doPlan();
    });
  }

  // 改决策 → 重算。后端拒绝时必须把 state 回滚，否则界面显示「已决策」而计划里没有它。
  function changeDecisions(next, prev) {
    state.decisions = next;
    return doPlan().then(function (ok) {
      if (ok) { return; }
      state.decisions = prev;
      return doPlan(true);
    });
  }

  function tierChip(tier) {
    var t = tier === 'winget' || tier === 'local' || tier === 'conda' || tier === 'manual' ? tier : 'skip';
    return '<span class="chip" data-tone="' + t + '">' + esc(tier || '—') + '</span>';
  }

  function statusChip(it) {
    if (it.status === 'planned') { return ''; }
    var tone = it.status === 'manual' ? 'manual'
      : (it.status === 'needs-disambiguation' || it.status === 'blocked-needs-decision') ? 'warn'
        : 'skip';
    return ' <span class="chip" data-tone="' + tone + '">' + esc(it.status) + '</span>';
  }

  function renderPlan() {
    var p = state.plan;
    if (!p) { show($('plan-result'), false); return; }
    var s = p.summary || {};
    var out = [];

    // 待确认清单先算出来：上面的速览与下面的分区必须是同一个数。
    // 曾经出现过「待确认 0」配「3 项」——速览取的是 summary.ambiguous（只数歧义），
    // 分区数的是 manual + covered + 失效 PATH，两套口径摆在同一个页面上。
    var pending = (p.items || []).filter(function (it) {
      return it.status === 'needs-disambiguation' || it.status === 'manual' || it.status === 'covered';
    });
    var pendingPath = (p.path_changes || []).filter(function (c) { return c.mark === '?' || c.mark === '!'; });
    var pendingCount = pending.length + pendingPath.length;
    var ambiguousCount = pending.filter(function (it) { return it.status === 'needs-disambiguation'; }).length;

    out.push('<div class="section"><div class="section-head"><h2>计划概览</h2>'
      + '<div class="copy-row push-right">'
      + '<span class="hint">plan_hash</span>'
      + '<code class="hash" id="plan-hash">' + esc(state.planHash) + '</code>'
      + '<button class="btn btn-ghost btn-sm" id="btn-copy-hash" type="button">复制</button></div></div>'
      + '<div class="section-body">'
      + (p.blocked ? '<div class="guard" style="margin-bottom:12px"><strong>计划被阻止：</strong>' + esc(p.blocked) + '</div>' : '')
      + tallyHtml([
        ['条目', String(s.total || 0)],
        ['winget', String(s.winget || 0)],
        ['本地包', String(s.local || 0)],
        ['conda', String(s.conda || 0)],
        ['manual', String(s.manual || 0)],
        ['跳过', String(s.skipped || 0)],
        ['未决策跳过', String(s.blocked_needs_decision || 0)],
        ['按决策处理', String(s.by_decision || 0)]
      ])
      + '<p class="hint" style="margin:10px 0 0">目标机 '
      + esc((p.target && p.target.os) || '?') + ' / ' + esc((p.target && p.target.arch) || '?')
      + ' · winget ' + ((p.target && p.target.winget_ok) ? '可用' : '不可用')
      + ' · 网络 ' + ((p.target && p.target.network_ok) ? '可达' : '不可达')
      + ' · 生成于 ' + esc(p.generated_at || '?') + '</p>'
      + '<p class="hint" style="margin:6px 0 0">预检：安装包 ' + esc(String((p.preflight || {}).installer_files || 0))
      + ' 个 / 命中配方 ' + esc(String((p.preflight || {}).installer_matched || 0))
      + ' 个 · 配方表 ' + esc(String((p.preflight || {}).recipes || 0)) + ' 条</p>'
      + ((p.warnings || []).length
        ? '<div class="guard" style="margin-top:12px"><div><strong>计划警告（' + p.warnings.length + '）</strong><ul style="margin:6px 0 0; padding-left:20px">'
          + p.warnings.map(function (w) { return '<li>' + esc(w) + '</li>'; }).join('')
          + '</ul></div></div>'
        : '')
      + '</div></div>');

    // 执行顺序表
    var rows = (p.items || []).map(function (it, i) {
      var exp = it.expected_version || '—';
      var cur = it.current_version || '—';
      var condaNote = '';
      if (it.conda) {
        condaNote = '<br><span class="hint">conda ' + ((it.conda.packages || []).length)
          + ' 包 · pip ' + ((it.conda.pip_packages || []).length) + ' 包';
        if ((it.conda_missing || []).length) {
          condaNote += ' · 缺 conda ' + it.conda_missing.length + '：'
            + esc(it.conda_missing.slice(0, 4).join(', '))
            + (it.conda_missing.length > 4 ? ' …' : '');
        }
        if ((it.pip_missing || []).length) {
          condaNote += ' · 缺 pip ' + it.pip_missing.length + '：'
            + esc(it.pip_missing.slice(0, 4).join(', '))
            + (it.pip_missing.length > 4 ? ' …' : '');
        }
        condaNote += '</span>';
      }
      return '<tr>'
        + '<td class="num">' + (i + 1) + '</td>'
        + '<td class="name">' + esc(it.name) + statusChip(it)
        + condaNote
        + (it.manual_hint ? '<br><span class="hint">' + esc(it.manual_hint) + '</span>' : '') + '</td>'
        + '<td class="code">' + esc(exp) + '<br><span class="hint">当前 ' + esc(cur) + '</span></td>'
        + '<td>' + tierChip(it.tier) + '</td>'
        + '<td class="code">' + (it.command ? esc(it.command) : '<span class="hint">—</span>') + '</td>'
        + '<td>' + (it.needs_admin ? '<span class="chip" data-tone="warn">需管理员运行</span>' : '<span class="hint">—</span>') + '</td>'
        + '<td>' + (it.expect_reboot ? '<span class="chip" data-tone="warn">会重启</span>' : '<span class="hint">—</span>') + '</td>'
        + '<td class="code">' + (it.verify_command
          ? esc(it.verify_command) + (it.verify_expect_contains
            ? '<br><span class="hint">输出需含 ' + esc(it.verify_expect_contains) + '</span>' : '')
          : '<span class="hint">全局三级校验</span>') + '</td>'
        + '<td class="code">' + (it.fallback ? esc(it.fallback) : '<span class="hint">—</span>') + '</td>'
        + '</tr>';
    }).join('');
    if (!rows) { rows = '<tr><td colspan="9" class="hint">（计划里没有需要执行的条目）</td></tr>'; }

    out.push('<div class="section"><div class="section-head"><h2>执行顺序</h2>'
      + '<span class="count">' + (p.items || []).length + ' 项</span></div>'
      + '<div class="section-body tight"><div class="table-wrap">'
      + '<table class="ledger plan-table">'
      + '<colgroup>'
      + '<col class="c-idx"><col class="c-target"><col class="c-version"><col class="c-tier">'
      + '<col class="c-cmd"><col class="c-admin"><col class="c-reboot"><col class="c-verify"><col class="c-fallback">'
      + '</colgroup>'
      + '<thead><tr><th>#</th><th>目标</th><th>期望 / 当前</th><th>层级</th><th>将执行的最终命令</th>'
      + '<th>管理员运行</th><th>重启</th><th>校验</th><th>失败降级</th></tr></thead>'
      + '<tbody>' + rows + '</tbody></table></div></div>'
      + '<div class="section-foot"><span class="hint">命令里的变量已代入目标机真实值；这里显示的就是将要执行的那一条，不存在第二套拼接逻辑。</span></div></div>');

    // ④ 待确认与决策：每条给出可用的动作，已决策的显示徽章与撤销。
    out.push(decisionsSection(p));
    // ④ 二次确认与执行（E3/E4/E5）
    out.push(confirmSection(p));

    var box = $('plan-result');
    box.innerHTML = out.join('');
    show(box, true);
    bindPlanControls(box);
    bindDecisionControls(box);
    bindConfirmControls(box);
    if (state.plan) { renderPathPanel(); }
  }

  /* ---------------- ④ 决策 ---------------- */

  // 需要人决定的条目：未决策的、被同配方去重覆盖的（可改用）、已经下过决策的。
  function needsDecision(it) {
    if (it.status === 'blocked-needs-decision') { return true; }
    if (it.decision) { return true; }
    if (it.status === 'covered' && it.recipe) { return true; }
    if (it.status === 'planned' && it.install_dir_supported &&
        (it.tier === 'local' || it.tier === 'winget')) { return true; }
    return false;
  }

  function decisionOf(id) {
    for (var i = 0; i < state.decisions.length; i++) {
      if (state.decisions[i].target_id === id) { return state.decisions[i]; }
    }
    return null;
  }

  // 写一条决策并重算计划：决策会改变 plan_hash，确认态必须作废。
  function putDecision(d) {
    var prev = state.decisions;
    var prevDec = decisionOf(d.target_id);
    if (prevDec && prevDec.action === 'select' && d.action === 'set_location') {
      if (!window.confirm('将替换版本决策（select），继续？')) { return; }
    }
    var out = [];
    var replaced = false;
    prev.forEach(function (x) {
      if (x.target_id === d.target_id) { out.push(d); replaced = true; } else { out.push(x); }
    });
    if (!replaced) { out.push(d); }
    changeDecisions(out, prev);
  }

  function dropDecision(id) {
    var prev = state.decisions;
    changeDecisions(prev.filter(function (x) { return x.target_id !== id; }), prev);
  }

  // 决策文件：只在用户点「保存」时写盘（D3）。
  function saveDecisionsFile() {
    return api(R.decisions, { method: 'POST', body: { decisions: state.decisions } }).then(function (r) {
      setText($('plan-load-note'), '决策已保存：' + (r.saved || 0) + ' 条（envlift.decisions.yaml，本机侧、不进 git）');
    }).catch(function (e) { showError(e, null); });
  }

  function decisionBadge(it) {
    if (!it.decision) { return ''; }
    return ' <span class="chip" data-tone="ok">决策 · ' + esc(it.decision) + '</span>'
      + ' <button class="btn btn-ghost btn-sm dec-undo" data-undo="' + esc(it.target_id) + '" type="button">撤销</button>';
  }

  function decisionControl(it) {
    var cands = it.candidates || [];
    var html = '<div class="dec-ctl" data-id="' + esc(it.target_id) + '" data-kind="' + esc(it.kind) + '">';
    if (it.status === 'covered') {
      html += '<button class="btn btn-ghost btn-sm dec-act" data-action="override_representative" type="button">改用此项</button>';
    }
    if (cands.length && (it.candidate_kind === 'installer' || it.candidate_kind === 'version' || it.candidate_kind === 'recipe')) {
      var label = it.candidate_kind === 'installer' ? '选安装包'
        : it.candidate_kind === 'recipe' ? '选配方' : '选版本';
      html += '<label class="field"><span class="hint">' + label + '</span>'
        + '<select class="dec-cand" aria-label="' + label + '">'
        + '<option value="">—</option>'
        + cands.map(function (c) {
          return '<option value="' + esc(c) + '">' + esc(c) + '</option>';
        }).join('')
        + '</select></label>';
    }

    var forceOk = it.tier !== 'winget' ||
      (state.plan && state.plan.target && state.plan.target.winget_force_ok === true);
    var dirOk = (it.tier === 'local' || it.tier === 'winget') && it.install_dir_supported && forceOk;
    html += '<label class="field"><span class="hint">安装目录</span>'
      + '<input class="dec-installdir" type="text" spellcheck="false" placeholder="安装目录，如 D:\\Apps\\Git"'
      + (dirOk ? '' : ' disabled') + ' aria-label="安装目录"></label>'
      + '<button class="btn btn-ghost btn-sm dec-apply-dir" type="button"'
      + (dirOk ? '' : ' disabled') + '>指定目录</button>'
      + (dirOk ? '' : '<span class="hint">'
        + (it.kind === 'conda_env' ? 'conda 环境不支持指定安装目录'
          : it.decision === 'select' ? '当前版本不支持与选包/选配方组合'
          : (it.tier === 'winget' && !forceOk ? '本机 winget 不支持 --force，不能指定目录'
            : '该配方不支持指定安装目录'))
        + '</span>');

    html += '<button class="btn btn-ghost btn-sm dec-act" data-action="skip" type="button">跳过</button>'
      + '<button class="btn btn-ghost btn-sm dec-act" data-action="mark_manual" type="button">转人工</button>'
      + '</div>';
    return html;
  }

  function decisionsSection(p) {
    var items = (p.items || []).filter(needsDecision);
    var paths = (p.path_changes || []).filter(function (c) {
      return c.status === 'blocked-needs-decision' || c.decision;
    });
    var blocked = (p.summary || {}).blocked_needs_decision || 0;
    var out = [];

    var cards = items.map(function (it) {
      return '<div class="pending-card"><h4>' + esc(it.name) + statusChip(it) + decisionBadge(it) + '</h4>'
        + '<p class="why">' + esc(it.reason || '')
        + (it.manual_hint ? ' · 人工步骤：' + esc(it.manual_hint) : '') + '</p>'
        + decisionControl(it) + '</div>';
    }).concat(paths.map(function (c) {
      var dec = decisionOf(c.target_id);
      var orig = (dec && dec.value && dec.value !== c.value)
        ? ' <span class="hint">原值 ' + esc(dec.value) + '</span>' : '';
      var head = '<h4>PATH[' + esc(c.section) + '] <code>' + esc(c.value) + '</code> '
        + markChip(c.mark) + orig
        + (c.decision ? ' <span class="chip" data-tone="ok">决策 · ' + esc(c.decision) + '</span>' : '') + '</h4>';
      var undo = c.decision
        ? '<button class="btn btn-ghost btn-sm dec-undo" data-undo="' + esc(c.target_id) + '" type="button">撤销</button>'
        : '';
      var ctl = '<div class="dec-ctl" data-id="' + esc(c.target_id) + '" data-kind="path">'
        + '<label class="field"><span class="hint">处置</span><select class="dec-act-path" aria-label="PATH 处置">'
        + '<option value="">—</option>'
        + '<option value="skip">不写入</option>'
        + '<option value="rewrite">改写落点</option>'
        + '<option value="add">按原值直接写入</option>'
        + '<option value="mark_invalid">标记失效</option>'
        + '</select></label>'
        + '<input class="dec-value" type="text" spellcheck="false" placeholder="新路径" aria-label="新路径" hidden>'
        + '<button class="btn btn-ghost btn-sm dec-apply-path" type="button">应用</button>'
        + undo + '</div>';
      return '<div class="pending-card">' + head + '<p class="why">' + esc(c.reason || '') + '</p>' + ctl + '</div>';
    })).join('');

    out.push('<div class="section"><div class="section-head"><h2>待确认与决策</h2>'
      + '<span class="count">' + (items.length + paths.length) + ' 项</span></div>'
      + '<div class="section-body">'
      + (blocked > 0
        ? '<div class="guard" style="margin-bottom:12px"><strong>' + blocked + ' 项因未决策将被跳过（C2）。</strong>'
          + '它们不会阻断其它条目；先执行也可以，回头再补决策。</div>'
        : '')
      + (cards || '<span class="hint">没有需要人工决定的项目。</span>')
      + '<div style="margin-top:12px"><button class="btn btn-ghost btn-sm" id="btn-save-decisions" type="button"'
      + (state.decisions.length ? '' : ' disabled') + '>把当前决策保存到 envlift.decisions.yaml</button></div>'
      + '</div></div>');
    return out.join('');
  }

  function bindDecisionControls(scope) {
    Array.prototype.forEach.call(scope.querySelectorAll('.dec-undo'), function (btn) {
      btn.addEventListener('click', function () { dropDecision(btn.getAttribute('data-undo')); });
    });
    var save = scope.querySelector('#btn-save-decisions');
    if (save) { save.addEventListener('click', saveDecisionsFile); }

    Array.prototype.forEach.call(scope.querySelectorAll('.dec-ctl'), function (ctl) {
      var id = ctl.getAttribute('data-id');
      var kind = ctl.getAttribute('data-kind');
      var itemOf = function () {
        return (state.plan.items || []).filter(function (x) { return x.target_id === id; })[0];
      };
      Array.prototype.forEach.call(ctl.querySelectorAll('.dec-act'), function (btn) {
        btn.addEventListener('click', function () {
          var it = itemOf();
          var d = { target_id: id, kind: kind, action: btn.getAttribute('data-action') };
          if (it) { d.name = it.name; d.version = it.expected_version || ''; }
          putDecision(d);
        });
      });
      var pick = ctl.querySelector('.dec-cand');
      if (pick) {
        pick.addEventListener('change', function () {
          if (!pick.value) { return; }
          var it = itemOf();
          var params = {};
          if (it && it.candidate_kind === 'installer') { params.installer = pick.value; }
          else if (it && it.candidate_kind === 'recipe') { params.recipe = pick.value; }
          else { params.version = pick.value; }
          var d = { target_id: id, kind: kind, action: 'select', params: params };
          if (it) { d.name = it.name; d.version = it.expected_version || ''; }
          putDecision(d);
        });
      }

      var dirInput = ctl.querySelector('.dec-installdir');
      var dirApply = ctl.querySelector('.dec-apply-dir');
      if (dirApply) {
        dirApply.addEventListener('click', function () {
          var v = (dirInput && dirInput.value || '').trim();
          if (!v) {
            showError({ code: 'invalid_decision_params', message: '安装目录不能为空' }, null);
            return;
          }
          var it = itemOf();
          var d = { target_id: id, kind: kind, action: 'set_location', params: { install_dir: v } };
          if (it) { d.name = it.name; d.version = it.expected_version || ''; }
          putDecision(d);
        });
      }

      if (kind === 'path') {
        var act = ctl.querySelector('.dec-act-path');
        var val = ctl.querySelector('.dec-value');
        var apply = ctl.querySelector('.dec-apply-path');
        var ch = (state.plan.path_changes || []).filter(function (x) { return x.target_id === id; })[0];
        if (act) {
          var sync = function () { if (val) { val.hidden = act.value !== 'rewrite'; } };
          act.addEventListener('change', sync);
          sync();
        }
        if (apply) {
          apply.addEventListener('click', function () {
            var action = act ? act.value : '';
            if (!action) { showError({ code: 'bad_request', message: '先选一个处置方式' }, null); return; }
            var d = { target_id: id, kind: 'path', action: action };
            if (ch) { d.value = ch.value; }
            if (action === 'rewrite') {
              var v = (val && val.value || '').trim();
              if (!v) { showError({ code: 'invalid_decision_params', message: '改写落点必须填新路径（如 D:\Tools）' }, null); return; }
              d.params = { new_value: v };
            }
            putDecision(d);
          });
        }
      }
    });
  }

  function bindPlanControls(scope) {
    var copy = scope.querySelector('#btn-copy-hash');
    if (!copy) { return; }
    copy.addEventListener('click', function () {
      var text = state.planHash;
      function fallback() {
        var node = $('plan-hash');
        var range = document.createRange();
        range.selectNodeContents(node);
        var sel = window.getSelection();
        sel.removeAllRanges();
        sel.addRange(range);
        copy.textContent = '已选中，按 Ctrl-C';
      }
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(function () {
          copy.textContent = '已复制';
          setTimeout(function () { copy.textContent = '复制'; }, 1500);
        }).catch(fallback);
      } else { fallback(); }
    });
  }

  /* ---------------- ④ 二次确认与执行 ---------------- */

  function resetConfirm() {
    state.confirm = { mode: 'all', ackSkipped: false, rebootAck: false, admin: {} };
  }

  // 必须逐项确认的提权操作。口径与 api/guard.go 的 adminRequirements 一致：
  // 系统级 PATH 写入也算提权项，名字形如「PATH[system] <值>」。
  function adminRequirements(p) {
    var out = [];
    (p.items || []).forEach(function (it) {
      if (it.status === 'planned' && it.command && it.needs_admin) {
        out.push({ name: it.name, command: it.command });
      }
    });
    (p.path_changes || []).forEach(function (c) {
      if (c.section === 'machine' && (c.mark === '+' || c.mark === '~')) {
        out.push({ name: 'PATH[system] ' + c.value, command: '写入系统级 PATH（HKLM）' });
      }
    });
    return out;
  }

  function confirmSection(p) {
    var s = p.summary || {};
    var admins = adminRequirements(p);
    var reboots = (p.items || []).filter(function (it) {
      return it.status === 'planned' && it.command && it.expect_reboot;
    });
    var pathWrites = (p.path_changes || []).filter(function (c) { return c.mark === '+' || c.mark === '~'; });
    var blocked = s.blocked_needs_decision || 0;
    var blockedNames = (p.items || []).filter(function (it) { return it.status === 'blocked-needs-decision'; })
      .map(function (it) { return it.name; })
      .concat((p.path_changes || []).filter(function (c) { return c.status === 'blocked-needs-decision'; })
        .map(function (c) { return 'PATH[' + c.section + '] ' + c.value; }));

    var out = [];
    out.push('<div class="section cf-panel" id="confirm-panel"><div class="section-head"><h2>二次确认</h2>'
      + '<div class="copy-row push-right"><span class="hint">plan_hash</span>'
      + '<code class="hash">' + esc(state.planHash) + '</code></div></div>'
      + '<div class="section-body cf-body">');

    out.push(tallyHtml([
      ['计划项', String((p.items || []).length)],
      ['可执行', String((s.winget || 0) + (s.local || 0))],
      ['需管理员运行', String(admins.length)],
      ['会重启', String(reboots.length)],
      ['未决策跳过', String(blocked)]
    ]));

    if (blocked > 0) {
      out.push('<div class="guard cf-guard"><strong>' + blocked + ' 项因未决策将被跳过：</strong>'
        + esc(blockedNames.join('、')) + '</div>');
    }

    var pathHint = 'PATH 变更 ' + pathWrites.length + ' 条；执行前会先把 HKCU/HKLM 两处 Path '
      + '的原始值与类型整体备份到 reports/path-backup-*.json 与 journal，失败或需要时可以逐字符回滚。';
    out.push('<p class="hint cf-path-hint" title="' + esc(pathHint) + '">PATH 变更 ' + pathWrites.length
      + ' 条；执行前会备份 HKCU/HKLM 两处 Path 原始值与类型到 <code>reports/path-backup-*.json</code>'
      + ' 与 journal，可逐字符回滚。</p>');

    out.push('<div class="cf-mode" role="radiogroup" aria-label="执行范围">'
      + '<label class="cf-mode-opt"><input type="radio" name="cf-mode" id="cf-mode-all" value="all" checked>'
      + '<span class="cf-mode-title">全部执行</span><span class="cf-mode-note">逐项确认管理员项</span></label>'
      + '<label class="cf-mode-opt"><input type="radio" name="cf-mode" id="cf-mode-skip" value="skip_admin">'
      + '<span class="cf-mode-title">仅无需管理员</span><span class="cf-mode-note">skip_admin</span></label>'
      + '</div>');

    out.push('<div class="cf-ack" hidden><label class="cf-ack-row">'
      + '<input type="checkbox" id="cf-ack-skip">'
      + '<span>我知道：需要管理员运行的项（' + admins.length + ' 项）会被阻止，不会执行</span></label></div>');

    out.push('<section class="cf-admin-card" id="cf-admin" aria-labelledby="cf-admin-title">'
      + '<div class="cf-admin-head">'
      + '<div class="cf-admin-title"><strong id="cf-admin-title">需管理员运行 ' + admins.length + ' 项</strong>'
      + '<span class="cf-admin-count" id="cf-admin-count">已选 0/' + admins.length + '</span></div>'
      + '<div class="cf-admin-tools">'
      + '<button class="btn btn-ghost btn-sm" id="cf-admin-all" type="button"'
      + ' title="只勾选全部管理员项；仍需点击「执行还原」才会开始">全选</button>'
      + '<button class="btn btn-ghost btn-sm" id="cf-admin-none" type="button">清空</button>'
      + '</div></div>'
      + '<div class="cf-admin-list" id="cf-admin-list">'
      + (admins.length
        ? admins.map(function (a) {
          return '<div class="cf-admin-row">'
            + '<label class="cf-admin-pick">'
            + '<input type="checkbox" class="cf-admin-check" data-name="' + esc(a.name) + '">'
            + '<span class="cf-admin-name" title="' + esc(a.name) + '">' + esc(a.name) + '</span>'
            + '</label>'
            + '<code class="cf-admin-cmd" role="button" tabindex="0" aria-expanded="false" title="'
            + esc(a.command) + '">' + esc(a.command) + '</code>'
            + '</div>';
        }).join('')
        : '<div class="cf-admin-empty hint">（无）</div>')
      + '</div></section>');

    if (reboots.length) {
      out.push('<div class="cf-reboot"><label class="cf-reboot-row"><input type="checkbox" id="cf-reboot-ack">'
        + '<span><strong>执行后会重启：</strong>'
        + esc(reboots.map(function (it) { return it.name; }).join('、')) + '</span></label></div>');
    } else {
      out.push('<input type="checkbox" id="cf-reboot-ack" hidden>');
    }

    out.push('<div id="apply-run" hidden></div>');
    out.push('<div class="cf-foot">'
      + '<button class="btn" id="btn-apply" type="button">执行还原</button>'
      + '<button class="btn btn-ghost btn-sm" id="btn-save-decisions-2" type="button">保存决策文件</button>'
      + '<span class="hint cf-note" id="cf-note">mode 必须显式选择；全选后仍需点击「执行还原」；空载荷不会被当成「可以跑」。</span>'
      + '</div>');
    out.push('</div></div>');
    return out.join('');
  }

  function bindConfirmControls(scope) {
    var modeAll = scope.querySelector('#cf-mode-all');
    var modeSkip = scope.querySelector('#cf-mode-skip');
    var ackWrap = scope.querySelector('.cf-ack');
    var ack = scope.querySelector('#cf-ack-skip');
    var adminCard = scope.querySelector('#cf-admin');
    var count = scope.querySelector('#cf-admin-count');
    var reboot = scope.querySelector('#cf-reboot-ack');
    var btn = scope.querySelector('#btn-apply');
    var save2 = scope.querySelector('#btn-save-decisions-2');
    var selectAll = scope.querySelector('#cf-admin-all');
    var clearAll = scope.querySelector('#cf-admin-none');
    if (save2) { save2.addEventListener('click', saveDecisionsFile); }
    if (!btn) { return; }

    function ready() {
      var skip = modeSkip && modeSkip.checked;
      var ok = true;
      var checks = scope.querySelectorAll('.cf-admin-check');
      var picked = 0;
      for (var j = 0; j < checks.length; j++) { if (checks[j].checked) { picked++; } }
      if (count) { count.textContent = '已选 ' + picked + '/' + checks.length; }
      if (skip) { ok = !!(ack && ack.checked); }
      else {
        for (var i = 0; i < checks.length; i++) { if (!checks[i].checked) { ok = false; } }
      }
      // 会重启的项：必须单独勾一次，skip_admin 下也一样（提权项被阻止时不计入。
      if (reboot && !reboot.hidden && !reboot.checked) { ok = false; }
      btn.disabled = !ok || state.applying;
      return ok;
    }

    function syncMode() {
      var skip = modeSkip && modeSkip.checked;
      if (ackWrap) { ackWrap.hidden = !skip; }
      if (adminCard) { adminCard.hidden = skip; }
      // 选中态用显式类，不依赖 :has()；老版 Edge/Chrome 也能正确高亮。
      Array.prototype.forEach.call(scope.querySelectorAll('.cf-mode-opt'), function (opt) {
        var radio = opt.querySelector('input[type="radio"]');
        opt.classList.toggle('is-checked', !!(radio && radio.checked));
      });
      ready();
    }

    function setAll(checked) {
      Array.prototype.forEach.call(scope.querySelectorAll('.cf-admin-check'), function (c) {
        c.checked = checked;
        c.dispatchEvent(new Event('change', { bubbles: true }));
      });
    }

    if (modeAll) { modeAll.addEventListener('change', syncMode); }
    if (modeSkip) { modeSkip.addEventListener('change', syncMode); }
    if (ack) { ack.addEventListener('change', ready); }
    if (selectAll) { selectAll.addEventListener('click', function () { setAll(true); }); }
    if (clearAll) { clearAll.addEventListener('click', function () { setAll(false); }); }
    Array.prototype.forEach.call(scope.querySelectorAll('.cf-admin-check'), function (c) {
      c.addEventListener('change', ready);
    });
    Array.prototype.forEach.call(scope.querySelectorAll('.cf-admin-cmd'), function (cmd) {
      function toggle() {
        var row = cmd.closest('.cf-admin-row');
        if (!row) { return; }
        var open = row.classList.toggle('is-open');
        cmd.setAttribute('aria-expanded', open ? 'true' : 'false');
      }
      cmd.addEventListener('click', toggle);
      cmd.addEventListener('keydown', function (e) {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          toggle();
        }
      });
    });
    if (reboot) { reboot.addEventListener('change', ready); }
    btn.addEventListener('click', function () { startApply(scope); });
    syncMode();
  }

  function startApply(scope) {
    if (state.applying) { return; }
    clearError();
    var skip = !!(scope.querySelector('#cf-mode-skip') && scope.querySelector('#cf-mode-skip').checked);
    var admins = [];
    Array.prototype.forEach.call(scope.querySelectorAll('.cf-admin-check:checked'), function (c) {
      admins.push(c.getAttribute('data-name'));
    });
    var ack = !!(scope.querySelector('#cf-ack-skip') && scope.querySelector('#cf-ack-skip').checked);
    var rebootAck = !!(scope.querySelector('#cf-reboot-ack') && scope.querySelector('#cf-reboot-ack').checked);

    var body = {
      config: ($('plan-config').value || 'envlift.yaml').trim(),
      decisions: state.decisions,
      plan_hash: state.planHash,
      mode: skip ? 'skip_admin' : 'all',
      ack_skipped_admin: ack,
      admin_confirmed: admins,
      reboot_ack: rebootAck
    };

    state.applying = true;
    var run = scope.querySelector('#apply-run');
    if (run) {
      run.hidden = false;
      run.innerHTML = '<div class="progress"><i style="width:6%"></i></div>'
        + '<div class="term"><div class="term-bar" aria-hidden="true"><i></i><i></i><i></i>'
        + '<span class="term-title">apply.log</span></div>'
        + '<div class="log" id="apply-log" role="log" aria-live="polite"></div></div>';
    }
    var logBox = scope.querySelector('#apply-log');
    var btn = scope.querySelector('#btn-apply');
    if (btn) { btn.disabled = true; }

    api(R.apply, { method: 'POST', body: body }).then(function (r) {
      appendApplyLog(logBox, 'info', '任务已启动：' + r.task_id);
      return pollTask(r.task_id, function (snap) {
        (snap.logs || []).forEach(function (l) { appendApplyLog(logBox, l.level, l.message); });
        if (snap.phase) { appendApplyLog(logBox, 'info', '阶段：' + snap.phase); }
      });
    }).then(function (snap) {
      state.applying = false;
      if (btn) { btn.disabled = false; }
      renderApplyDone(scope, snap);
      loadJournals();
    }).catch(function (e) {
      state.applying = false;
      if (btn) { btn.disabled = false; }
      appendApplyLog(logBox, 'error', (e.message || '执行失败'));
      showError(e, null);
    });
  }

  function appendApplyLog(box, level, msg) {
    if (!box) { return; }
    var line = document.createElement('div');
    line.className = 'l-' + (level || 'info');
    line.textContent = msg;
    box.appendChild(line);
    box.scrollTop = box.scrollHeight;
  }

  function renderApplyDone(scope, snap) {
    var run = scope.querySelector('#apply-run');
    if (!run) { return; }
    var res = snap.result || {};
    if (snap.state === 'awaiting_reboot') {
      run.innerHTML += '<div class="guard" style="margin-top:12px"><strong>等待重启。</strong>'
        + esc(snap.error || res.stopped || '有计划项执行后需要重启') + '<br>'
        + '重启后重新生成计划再执行：已完成的项会被幂等检查跳过。</div>';
      return;
    }
    if (snap.state === 'failed' || snap.state === 'cancelled') {
      run.innerHTML += '<div class="guard" style="margin-top:12px"><strong>'
        + (snap.state === 'failed' ? '执行失败' : '已取消') + '。</strong>' + esc(snap.error || '') + '</div>';
      return;
    }
    run.innerHTML += '<div class="guard" style="margin-top:12px"><strong>执行完成。</strong>'
      + '成功 ' + esc(String(res.executed || 0)) + ' · 跳过 ' + esc(String(res.skipped || 0))
      + ' · 未验证 ' + esc(String(res.unverified || 0))
      + ' · 失败 ' + esc(String(res.failed || 0)) + ' · 阻止 ' + esc(String(res.blocked || 0))
      + ' · 未决策跳过 ' + esc(String(res.undecided || 0))
      + ' · PATH 新增 ' + esc(String(res.path_added || 0)) + '</div>';
    api(R.reports).then(function (r) {
      var list = (r.reports || []).filter(function (x) {
        return x.kind === 'report' && !x.unsupported && !x.truncated;
      });
      if (!list.length) { return; }
      var newest = list[0];
      run.innerHTML += '<p class="hint" style="margin-top:8px">报告已生成：<code>' + esc(newest.name) + '</code>'
        + ' · <button class="btn btn-ghost btn-sm" id="btn-open-report" type="button">查看这份报告</button></p>';
      var openBtn = run.querySelector('#btn-open-report');
      if (openBtn) {
        openBtn.addEventListener('click', function () {
          selectTab('tab-report');
          openReport(newest.name);
        });
      }
      // E5：执行完成后按报告名跳到 ⑤ 详情页（链路闭合，不让用户自己找）。
      rep.detail = null;
      rep.diff = null;
      selectTab('tab-report');
      openReport(newest.name);
    }).catch(function () { /* 报告列表失败不影响执行结果展示 */ });
  }

  /* ---------------- PATH 页 ---------------- */

  var MARK_LEGEND = [
    ['+', '新增'], ['~', '用户名重写'], ['?', '待确认'], ['=', '已存在跳过'], ['!', '失效']
  ];

  function renderPathPanel() {
    var box = $('path-result');
    var p = state.plan;
    var out = [];

    if (!p) {
      box.innerHTML = '<div class="section"><div class="section-body"><div class="empty">'
        + '<strong>无计划</strong>请先在「还原」页生成一次计划。'
        + '<div style="margin-top:12px"><button class="btn btn-ghost" id="btn-goto-plan" type="button">去还原页</button></div>'
        + '</div></div></div>';
      show(box, true);
      $('btn-goto-plan').addEventListener('click', function () { selectTab('tab-restore', true); });
      return;
    }

    var legend = '<div class="mark-legend">' + MARK_LEGEND.map(function (m) {
      return '<span>' + markChip(m[0]) + esc(m[1]) + '</span>';
    }).join('') + '</div>';

    out.push('<div class="section"><div class="section-head"><h2>标记含义</h2></div>'
      + '<div class="section-body">' + legend + '</div></div>');

    ['user', 'machine'].forEach(function (sec) {
      var list = (p.path_changes || []).filter(function (c) { return c.section === sec; });
      var label = sec === 'user' ? 'PATH · 用户级（HKCU）' : 'PATH · 系统级（HKLM，需管理员运行）';
      var rows = list.map(function (c, i) {
        return '<tr><td class="num">' + (i + 1) + '</td>'
          + '<td>' + markChip(c.mark) + '</td>'
          + '<td class="data">' + esc(c.value) + '</td>'
          + '<td>' + (c.reason ? esc(c.reason) : '<span class="hint">—</span>') + '</td>'
          + '<td>' + (c.decision ? '<span class="chip" data-tone="ok">' + esc(c.decision) + '</span>' : '<span class="hint">—</span>') + '</td></tr>';
      }).join('');
      if (!rows) { rows = '<tr><td colspan="5" class="hint">（无）</td></tr>'; }
      out.push('<div class="section"><div class="section-head"><h2>' + esc(label) + '</h2>'
        + '<span class="count">' + list.length + ' 条</span></div>'
        + '<div class="section-body tight"><div class="table-wrap"><table class="ledger">'
        + '<thead><tr><th>#</th><th>标记</th><th>值</th><th>说明</th><th>决策</th></tr></thead>'
        + '<tbody>' + rows + '</tbody></table></div></div></div>');
    });

    out.push('<div class="section"><div class="section-head"><h2>备份与回滚</h2>'
      + '<div class="push-right"><button class="btn btn-ghost btn-sm" id="btn-refresh-journal" type="button">刷新</button></div></div>'
      + '<div class="section-body">'
      + '<p class="hint" style="margin:0 0 10px">执行还原时，PATH 改动前会先把两处 Path 的原始值与值类型整体写进 journal 与 <code>.path.bak</code>，落盘在 <code>reports/</code>。回滚以 journal 里的快照为准，逐字符写回。</p>'
      + '<p class="hint" style="margin:0 0 10px">注意：回滚只恢复 PATH，不卸载软件（D11）。</p>'
      + '<div id="journal-list"><span class="hint">读取中…</span></div>'
      + '</div></div>');

    box.innerHTML = out.join('');
    show(box, true);
    $('btn-refresh-journal').addEventListener('click', loadJournals);
    loadJournals();
  }

  function loadJournals() {
    var box = $('journal-list');
    if (!box) { return; }
    api(R.reports).then(function (r) {
      var list = (r.reports || []).filter(function (x) { return x.kind === 'journal'; });
      state.journals = list;
      if (!list.length) {
        box.innerHTML = '<span class="hint">reports/ 下还没有 journal —— 说明这台机器上还没执行过还原。</span>';
        return;
      }
      box.innerHTML = '<div class="table-wrap"><table class="ledger"><thead><tr>'
        + '<th>journal</th><th>修改时间</th><th>大小</th><th></th></tr></thead><tbody>'
        + list.map(function (j) {
          return '<tr><td class="code">' + esc(j.name) + '</td><td class="hint">' + esc(j.modified) + '</td>'
            + '<td class="code">' + esc(String(j.size_bytes)) + ' B</td>'
            + '<td><button class="btn btn-ghost btn-sm rb" data-name="' + esc(j.name) + '" type="button">回滚到这份快照</button></td></tr>';
        }).join('') + '</tbody></table></div>';

      Array.prototype.forEach.call(box.querySelectorAll('.rb'), function (btn) {
        btn.addEventListener('click', function () { confirmRollback(btn.getAttribute('data-name')); });
      });
    }).catch(function (e) {
      box.innerHTML = '';
      showError(e, loadJournals);
    });
  }

  function confirmRollback(name) {
    clearError();
    api(R.report(name, 'json')).then(function (j) {
      var snap = j.path_snapshot || {};
      var user = snap.user || {};
      var machine = snap.machine || {};
      var body = '<p>把两处 Path 恢复成这份快照里的值：</p>'
        + '<p class="hint">快照时间 ' + esc(snap.taken_at || '?') + '</p>'
        + '<p><strong>用户级（HKCU）</strong> · ' + esc(String((user.value || '').length)) + ' 字符 · '
        + esc(regKindName(user.kind)) + '</p>'
        + '<pre>' + esc(user.value || '（空）') + '</pre>'
        + '<p><strong>系统级（HKLM）</strong> · ' + esc(String((machine.value || '').length)) + ' 字符 · '
        + esc(regKindName(machine.kind)) + '</p>'
        + '<pre>' + esc(machine.value || '（空）') + '</pre>'
        + '<p class="hint">与当前值一致的段会跳过，不会平白要求管理员运行。</p>';
      return openDialog({
        title: '回滚 PATH',
        body: body,
        buttons: [
          { label: '取消', value: '' },
          { label: '确认回滚', value: 'go', variant: 'danger' }
        ]
      }).then(function (v) {
        if (v !== 'go') { return; }
        return api(R.rollback, { method: 'POST', body: { journal_id: name } }).then(function () {
          loadStatus();
          loadJournals();
          openDialog({ title: '已回滚', body: '<p>PATH 已按快照恢复。新开的终端才能读到恢复后的值。</p>', buttons: [{ label: '好', value: 'ok' }] });
        });
      });
    }).catch(function (e) { showError(e, function () { confirmRollback(name); }); });
  }

  /* ---------------- 对话框 ---------------- */

  function openDialog(opts) {
    var dlg = $('dlg');
    setText($('dlg-title'), opts.title || '');
    $('dlg-body').innerHTML = opts.body || '';
    var foot = $('dlg-foot');
    foot.innerHTML = '';
    var buttons = opts.buttons || [{ label: '好', value: 'ok' }];
    buttons.forEach(function (b, i) {
      var btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'btn' + (b.variant === 'danger' ? ' btn-danger' : (i === 0 ? ' btn-ghost' : ''));
      btn.textContent = b.label;
      btn.addEventListener('click', function () {
        dlg.close(b.value);
      });
      foot.appendChild(btn);
      // 默认聚焦第一个按钮（通常是「取消」）：破坏性操作不该是回车就能触发的那个。
      if (i === 0) { btn.setAttribute('data-default', '1'); }
    });
    return new Promise(function (resolve) {
      function done() {
        dlg.removeEventListener('close', done);
        resolve(dlg.returnValue || '');
      }
      dlg.addEventListener('close', done);
      dlg.showModal();
      var def = foot.querySelector('[data-default]');
      if (def) { def.focus(); }
    });
  }

  /* ---------------- ⑤ 报告页 ---------------- */

  var rep = {
    list: [], picked: [], detail: null, name: '', page: 0, filter: 'all',
    showMd: false, md: '', diff: null, diffA: '', diffB: ''
  };
  var REP_PAGE = 50;

  function loadReports() {
    return api(R.reports).then(function (r) {
      rep.list = (r.reports || []).filter(function (x) { return x.kind === 'report'; });
      renderReports();
    }).catch(function (e) { showError(e, loadReports); });
  }

  function renderReports() {
    var box = $('report-result');
    if (!box) { return; }
    if (rep.diff) {
      box.innerHTML = diffHtml();
      bindDiff(box);
    } else if (rep.detail) {
      box.innerHTML = detailHtml();
      bindDetail(box);
    } else {
      box.innerHTML = listHtml();
      bindList(box);
    }
    show(box, true);
  }

  function envBrief(env) {
    if (!env) { return '—'; }
    return (env.os || '?') + ' · build ' + (env.build || '?') + ' · ' + (env.arch || '?')
      + ' · winget ' + (env.winget_ok ? '可用' : '不可用')
      + ' · 网络 ' + (env.network_ok ? '可达' : '不可达');
  }

  function listHtml() {
    if (!rep.list.length) {
      return '<div class="section"><div class="section-head"><h2>报告</h2></div>'
        + '<div class="section-body"><div class="empty"><strong>还没有报告</strong>'
        + '每次预演（dry-run）或执行都会在 <code>reports/</code> 下写一份 md + json。'
        + '<div style="margin-top:10px">最安全的生成方式：先用 PATH-only 的配置预演一次——'
        + '只含一条 PATH、不含任何软件条目，跑 dry-run 不会动系统。</div>'
        + '<div style="margin-top:12px"><button class="btn btn-ghost" id="btn-goto-restore" type="button">去还原页生成计划</button>'
        + '<button class="btn btn-ghost" id="btn-rep-refresh" type="button">刷新列表</button></div>'
        + '</div></div></div>';
    }
    var rows = rep.list.map(function (r) {
      var s = r.summary || {};
      var picked = rep.picked.indexOf(r.name) >= 0;
      return '<tr>'
        + '<td><input type="checkbox" class="rep-pick" data-name="' + esc(r.name) + '"'
        + (picked ? ' checked' : '') + (r.truncated || r.unsupported ? ' disabled' : '') + '></td>'
        + '<td class="code">' + esc(r.modified || '') + '</td>'
        + '<td>' + (r.mode ? '<span class="chip" data-tone="' + (r.mode === 'apply' ? 'ok' : 'idle') + '">' + esc(r.mode) + '</span>' : '<span class="hint">—</span>') + '</td>'
        + '<td class="num">' + esc(String(s.succeeded || 0)) + '</td>'
        + '<td class="num">' + esc(String(s.skipped || 0)) + '</td>'
        + '<td class="num">' + esc(String(s.unverified || 0)) + '</td>'
        + '<td class="num">' + esc(String(s.manual || 0)) + '</td>'
        + '<td class="num">' + esc(String(s.blocked_needs_decision || 0)) + '</td>'
        + '<td class="num">' + esc(String(s.failed || 0)) + '</td>'
        + '<td class="hint">' + esc(envBrief(r.env)) + '</td>'
        + '<td><button class="btn btn-ghost btn-sm rep-open" data-name="' + esc(r.name) + '" type="button"'
        + (r.unsupported ? ' disabled title="格式版本不受支持，请升级 envlift"' : '') + '>打开</button></td>'
        + '</tr>';
    }).join('');

    return '<div class="section"><div class="section-head"><h2>报告</h2>'
      + '<span class="count">' + rep.list.length + ' 份</span>'
      + '<div class="row-actions push-right">'
      + '<button class="btn btn-ghost btn-sm" id="btn-rep-refresh" type="button">刷新</button>'
      + '<button class="btn btn-sm" id="btn-rep-diff" type="button" disabled>对比选中的两份</button>'
      + '</div></div>'
      + '<div class="section-body tight"><div class="table-wrap"><table class="ledger">'
      + '<thead><tr><th></th><th>时间</th><th>mode</th><th>成功</th><th>跳过</th><th>未验证</th><th>manual</th>'
      + '<th>未决策</th><th>失败</th><th>环境</th><th></th></tr></thead>'
      + '<tbody>' + rows + '</tbody></table></div>'
      + '<div class="section-foot"><span class="hint">勾选两份报告可以对比（只支持两份）。默认按时间倒序。</span></div>'
      + '</div></div>';
  }

  function bindList(box) {
    var refresh = box.querySelector('#btn-rep-refresh');
    if (refresh) { refresh.addEventListener('click', loadReports); }
    var go = box.querySelector('#btn-goto-restore');
    if (go) { go.addEventListener('click', function () { selectTab('tab-restore', true); }); }

    Array.prototype.forEach.call(box.querySelectorAll('.rep-pick'), function (cb) {
      cb.addEventListener('change', function () {
        var name = cb.getAttribute('data-name');
        if (cb.checked) {
          if (rep.picked.length >= 2) { cb.checked = false; return; }
          rep.picked.push(name);
        } else {
          rep.picked = rep.picked.filter(function (n) { return n !== name; });
        }
        var btn = box.querySelector('#btn-rep-diff');
        if (btn) { btn.disabled = rep.picked.length !== 2; }
      });
    });
    Array.prototype.forEach.call(box.querySelectorAll('.rep-open'), function (b) {
      b.addEventListener('click', function () { openReport(b.getAttribute('data-name')); });
    });
    var diffBtn = box.querySelector('#btn-rep-diff');
    if (diffBtn) {
      diffBtn.disabled = rep.picked.length !== 2;
      diffBtn.addEventListener('click', function () {
        if (rep.picked.length !== 2) { return; }
        // 列表是时间倒序：A 放**较早**的那份，B 放较新的，这样差异读作「以前 → 现在」。
        var a = rep.picked[0], b = rep.picked[1];
        var ia = rep.list.findIndex(function (x) { return x.name === a; });
        var ib = rep.list.findIndex(function (x) { return x.name === b; });
        if (ia < ib) { var t = a; a = b; b = t; }
        openDiff(a, b);
      });
    }
  }

  function openReport(name) {
    rep.name = name;
    rep.page = 0;
    rep.filter = 'all';
    rep.showMd = false;
    rep.diff = null;
    rep.detail = null;
    return api(R.report(name, 'json') + '&limit=500').then(function (r) {
      if (!r.supported) {
        showError({ code: 'report_unsupported_schema', message: r.message || '报告格式不受支持，请升级 envlift' }, null);
        rep.detail = null;
        renderReports();
        return;
      }
      rep.detail = r.report;
      rep.itemsTotal = r.items_total || (r.report.items || []).length;
      renderReports();
    }).catch(function (e) {
      showError(e, function () { openReport(name); });
      rep.detail = null;
      renderReports();
    });
  }

  function detailItems() {
    var items = (rep.detail && rep.detail.items) || [];
    if (rep.filter === 'failed') { return items.filter(function (x) { return x.status === 'failed'; }); }
    if (rep.filter === 'blocked') { return items.filter(function (x) { return x.status === 'blocked-needs-decision'; }); }
    if (rep.filter === 'manual') { return items.filter(function (x) { return x.status === 'manual'; }); }
    return items;
  }

  function detailHtml() {
    var d = rep.detail;
    var s = d.summary || {};
    var env = d.env || {};
    var items = detailItems();
    var pages = Math.max(1, Math.ceil(items.length / REP_PAGE));
    if (rep.page >= pages) { rep.page = pages - 1; }
    var pageItems = items.slice(rep.page * REP_PAGE, rep.page * REP_PAGE + REP_PAGE);
    var journal = String(d.journal || '').split(/[\\/]/).pop();

    var out = [];
    out.push('<div class="section"><div class="section-head"><h2>报告详情</h2>'
      + '<div class="row-actions push-right">'
      + '<button class="btn btn-ghost btn-sm" id="btn-rep-back" type="button">返回列表</button>'
      + '<button class="btn btn-ghost btn-sm" id="btn-rep-md" type="button">' + (rep.showMd ? '看结构化视图' : '看 md 原文') + '</button>'
      + '<button class="btn btn-ghost btn-sm" id="btn-rep-rollback" type="button"' + (journal ? '' : ' disabled') + '>回滚此报告的 PATH 变更</button>'
      + '</div></div><div class="section-body">'
      + tallyHtml([
        ['成功', String(s.succeeded || 0)],
        ['跳过', String(s.skipped || 0)],
        ['未验证', String(s.unverified || 0)],
        ['manual', String(s.manual || 0)],
        ['未决策', String(s.blocked_needs_decision || 0)],
        ['失败', String(s.failed || 0)],
        ['PATH 写入', String(s.path_writes || 0)]
      ])
      + '<p class="hint" style="margin:10px 0 0">' + esc(envBrief(env))
      + ' · 管理员运行 ' + (env.elevated ? '是' : '否') + '</p>'
      + '<p class="hint" style="margin:6px 0 0">plan_hash <code>' + esc(d.plan_hash || '') + '</code>'
      + ' · mode ' + esc(d.mode || '')
      + (journal ? ' · journal <code>' + esc(journal) + '</code>' : '') + '</p>'
      + (d.summary && d.summary.awaiting_reboot ? '<div class="guard" style="margin-top:12px"><strong>这份报告停在「等待重启」。</strong>重启后重跑，已完成的项会被幂等检查跳过。</div>' : '')
      + ((d.decisions && d.decisions.blocked || []).length
        ? '<div class="guard" style="margin-top:12px"><strong>因未决策被跳过：</strong>'
          + esc((d.decisions.blocked || []).join('、')) + '</div>'
        : '')
      + '</div></div>');

    if (rep.showMd) {
      out.push('<div class="section"><div class="section-head"><h2>md 原文</h2>'
        + '<span class="count">不渲染，只展示</span></div>'
        + '<div class="section-body"><pre class="md-raw" id="rep-md">' + esc(rep.md || '读取中…') + '</pre></div></div>');
      return out.join('');
    }

    out.push('<div class="section"><div class="section-head"><h2>逐项结果</h2>'
      + '<span class="count">' + items.length + ' / ' + (rep.itemsTotal || items.length) + ' 项</span>'
      + '<div class="row-actions push-right compact">'
      + [['all', '全部'], ['failed', '仅失败'], ['blocked', '仅未决策'], ['manual', '仅 manual']].map(function (f) {
        return '<button class="btn btn-sm ' + (rep.filter === f[0] ? '' : 'btn-ghost') + ' rep-filter" data-filter="' + f[0] + '" type="button">' + f[1] + '</button>';
      }).join('')
      + '</div></div><div class="section-body tight"><div class="table-wrap"><table class="ledger">'
      + '<thead><tr><th>#</th><th>类型</th><th>名称</th><th>期望 / 实际</th><th>层级</th><th>状态</th><th>退出码</th><th>校验</th><th>说明</th></tr></thead><tbody>'
      + (pageItems.map(function (it, i) {
        var idx = rep.page * REP_PAGE + i + 1;
        var note = it.failure_reason || it.decision || it.reason || '';
        return '<tr><td class="num">' + idx + '</td>'
          + '<td class="code">' + esc(it.kind) + '</td>'
          + '<td class="name">' + esc(it.name) + '</td>'
          + '<td class="code">' + esc(it.expected_version || '—')
          + '<br><span class="hint">前 ' + esc(it.before_version || '—') + ' · 后 '
          + esc(it.actual_version || (it.version_unparsed ? '解析失败' : '—')) + '</span></td>'
          + '<td>' + (it.tier ? tierChip(it.tier) : '<span class="hint">—</span>') + '</td>'
          + '<td>' + repStatusChip(it.status) + '</td>'
          + '<td class="code">' + (it.exit_code === undefined || it.exit_code === null ? '—' : esc(String(it.exit_code))) + '</td>'
          + '<td class="code">' + esc(it.verify_result || '—') + '</td>'
          + '<td class="hint">' + esc(note) + '</td></tr>';
      }).join('') || '<tr><td colspan="9" class="hint">（这个筛选下没有条目）</td></tr>')
      + '</tbody></table></div>'
      + (pages > 1
        ? '<div class="pager" style="display:flex; gap:8px; align-items:center; margin-top:10px">'
          + '<button class="btn btn-ghost btn-sm rep-pg" data-page="' + (rep.page - 1) + '" type="button"' + (rep.page === 0 ? ' disabled' : '') + '>上一页</button>'
          + '<span class="hint">第 ' + (rep.page + 1) + ' / ' + pages + ' 页</span>'
          + '<button class="btn btn-ghost btn-sm rep-pg" data-page="' + (rep.page + 1) + '" type="button"' + (rep.page >= pages - 1 ? ' disabled' : '') + '>下一页</button>'
          + '</div>'
        : '')
      + '</div></div>');

    var paths = d.path_changes || [];
    out.push('<div class="section"><div class="section-head"><h2>PATH 变更</h2>'
      + '<span class="count">' + paths.length + ' 条</span></div>'
      + '<div class="section-body tight">'
      + (paths.length
        ? '<div class="table-wrap"><table class="ledger"><thead><tr><th>段</th><th>标记</th><th>值</th><th>决策</th><th>说明</th></tr></thead><tbody>'
          + paths.map(function (c) {
            return '<tr><td>' + esc(c.section) + '</td><td>' + markChip(c.mark) + '</td>'
              + '<td class="data">' + esc(c.value) + '</td>'
              + '<td>' + (c.decision ? esc(c.decision) : '<span class="hint">—</span>') + '</td>'
              + '<td class="hint">' + esc(c.reason || '') + '</td></tr>';
          }).join('') + '</tbody></table></div>'
        : '<span class="hint">（无）</span>')
      + '</div></div>');

    var warnings = d.warnings || [];
    if (warnings.length) {
      out.push('<div class="section"><div class="section-head"><h2>警告</h2>'
        + '<span class="count">' + warnings.length + '</span></div><div class="section-body">'
        + '<ul style="margin:0; padding-left:20px">' + warnings.map(function (w) {
          return '<li>' + esc(w) + '</li>';
        }).join('') + '</ul></div></div>');
    }
    return out.join('');
  }

  function repStatusChip(status) {
    var tone = status === 'succeeded' ? 'ok'
      : status === 'failed' ? 'warn'
        : status === 'unverified' ? 'warn'
        : status === 'manual' ? 'manual' : 'skip';
    return '<span class="chip" data-tone="' + tone + '">' + esc(status || '—') + '</span>';
  }

  function bindDetail(box) {
    var back = box.querySelector('#btn-rep-back');
    if (back) { back.addEventListener('click', function () { rep.detail = null; rep.diff = null; renderReports(); }); }

    var md = box.querySelector('#btn-rep-md');
    if (md) {
      md.addEventListener('click', function () {
        rep.showMd = !rep.showMd;
        if (rep.showMd && !rep.md) {
          api(R.report(rep.name, 'md'), { raw: true }).then(function (text) {
            rep.md = typeof text === 'string' ? text : '';
          }).catch(function (e) { showError(e, null); })
            .then(function () { renderReports(); });
          return;
        }
        renderReports();
      });
    }

    var rb = box.querySelector('#btn-rep-rollback');
    if (rb) {
      rb.addEventListener('click', function () {
        var journal = String((rep.detail || {}).journal || '').split(/[\\/]/).pop();
        if (!journal) { return; }
        confirmRollback(journal);
      });
    }

    Array.prototype.forEach.call(box.querySelectorAll('.rep-filter'), function (b) {
      b.addEventListener('click', function () {
        rep.filter = b.getAttribute('data-filter');
        rep.page = 0;
        renderReports();
      });
    });
    Array.prototype.forEach.call(box.querySelectorAll('.rep-pg'), function (b) {
      b.addEventListener('click', function () {
        var p = parseInt(b.getAttribute('data-page'), 10);
        if (isNaN(p) || p < 0) { return; }
        rep.page = p;
        renderReports();
      });
    });
  }

  function openDiff(a, b) {
    return api(R.reportDiff(a, b)).then(function (r) {
      rep.diff = r.diff;
      rep.diffA = a;
      rep.diffB = b;
      rep.detail = null;
      renderReports();
    }).catch(function (e) { showError(e, function () { openDiff(a, b); }); });
  }

  function diffHtml() {
    var d = rep.diff;
    var out = [];
    var groups = [
      ['status_changes', '状态变化', '最该看的：跑成功了还是失败了'],
      ['tier_changes', '层级变化', 'winget → 本地包 / manual，通常是断网或源异常'],
      ['version_changes', '版本变化', '期望版本或目标机实际版本不同']
    ];
    out.push('<div class="section"><div class="section-head"><h2>报告对比</h2>'
      + '<div class="push-right"><button class="btn btn-ghost btn-sm" id="btn-diff-back" type="button">返回列表</button></div></div>'
      + '<div class="section-body">'
      + '<p class="hint" style="margin:0">A = <code>' + esc(rep.diffA) + '</code> · B = <code>' + esc(rep.diffB) + '</code></p>');

    if ((d.env_warnings || []).length) {
      out.push('<div class="guard" style="margin-top:12px"><strong>两份报告的环境不同（先看这里）</strong><ul style="margin:6px 0 0; padding-left:20px">'
        + d.env_warnings.map(function (w) { return '<li>' + esc(w) + '</li>'; }).join('')
        + '</ul>' + (d.env_note ? '<p class="hint" style="margin:8px 0 0">' + esc(d.env_note) + '</p>' : '')
        + '</div>');
    } else {
      out.push('<p class="hint" style="margin-top:12px">两份报告的环境一致（系统 / 架构 / winget / 网络 / 管理员运行 / mode）。</p>');
    }
    out.push('</div></div>');

    var total = 0;
    groups.forEach(function (g) {
      var rows = d[g[0]] || [];
      total += rows.length;
      out.push('<div class="section"><div class="section-head"><h2>' + g[1] + '</h2>'
        + '<span class="count">' + rows.length + ' 项</span></div>'
        + '<div class="section-body tight"><p class="hint" style="margin:0 0 8px">' + esc(g[2]) + '</p>'
        + (rows.length
          ? '<div class="table-wrap"><table class="ledger"><thead><tr>'
            + '<th>条目</th><th>说明</th><th>A</th><th>B</th></tr></thead><tbody>'
            + rows.map(function (x) { return diffRow(x); }).join('')
            + '</tbody></table></div>'
          : '<span class="hint">（无）</span>')
        + '</div></div>');
    });

    var otherRows = (d.added || []).concat(d.removed || []);
    out.push('<div class="section"><div class="section-head"><h2>其他（新增 / 消失）</h2>'
      + '<span class="count">' + otherRows.length + ' 项</span></div>'
      + '<div class="section-body tight">'
      + (otherRows.length
        ? '<details><summary class="hint">展开 ' + otherRows.length + ' 项</summary>'
          + '<div class="table-wrap" style="margin-top:8px"><table class="ledger"><thead><tr>'
          + '<th>条目</th><th>说明</th><th>A</th><th>B</th></tr></thead><tbody>'
          + otherRows.map(function (x) { return diffRow(x); }).join('')
          + '</tbody></table></div></details>'
        : '<span class="hint">（无）</span>')
      + '</div></div>');

    out.push('<div class="section"><div class="section-body"><p class="hint" style="margin:0">'
      + '未变化 ' + (d.unchanged || 0) + ' 项 · 差异合计 ' + (total + otherRows.length) + ' 项</p></div></div>');
    return out.join('');
  }

  function diffRow(x) {
    function side(s) {
      if (!s) { return '<span class="hint">—</span>'; }
      return repStatusChip(s.status)
        + (s.tier ? '<br><span class="hint">' + esc(s.tier) + '</span>' : '')
        + (s.expected_version || s.before_version || s.after_version
          ? '<br><span class="hint">期望 ' + esc(s.expected_version || '—')
            + ' · 前 ' + esc(s.before_version || '—')
            + ' · 后 ' + esc(s.after_version || (s.version_unparsed ? '解析失败' : '—')) + '</span>'
          : '');
    }
    return '<tr><td class="name">' + esc(x.name) + '<br><span class="hint">' + esc(x.kind) + '</span></td>'
      + '<td class="hint">' + esc(x.detail || '') + '</td>'
      + '<td>' + side(x.left) + '</td><td>' + side(x.right) + '</td></tr>';
  }

  function bindDiff(box) {
    var back = box.querySelector('#btn-diff-back');
    if (back) {
      back.addEventListener('click', function () {
        rep.diff = null;
        rep.picked = [];
        renderReports();
      });
    }
  }

  /* ---------------- 退出 ---------------- */

  function doQuit() {
    var body = state.busy
      ? '<p>有任务正在跑。关闭服务会立刻中断它，已做的改动不会回滚。</p><p>确定关闭？</p>'
      : '<p>关闭本地服务并退出 envlift。终端里按 Ctrl-C 效果一样。</p>';
    openDialog({
      title: '退出 envlift',
      body: body,
      buttons: [{ label: '取消', value: '' }, { label: '退出', value: 'go', variant: 'danger' }]
    }).then(function (v) {
      if (v !== 'go') { return; }
      api(R.quit, { method: 'POST' }).catch(function () { /* 服务可能先关连接，忽略 */ })
        .then(function () {
          document.body.innerHTML = '<div class="bye"><h1>envlift 已退出</h1>'
            + '<p>服务已关闭，端口已释放。需要再用时重新运行 <code>envlift</code>。</p></div>';
        });
    });
  }

  /* ---------------- 启动 ---------------- */

  // 决策文件是本机侧的持久选择：进页面先读回来，保证 GUI 与 CLI 看到同一份。
  function loadDecisions() {
    return api(R.decisions).then(function (r) {
      state.decisions = (r && r.decisions) || [];
    }).catch(function () {
      // 读不到不该拦住页面；等真正生成计划时会再报一次明确的错误。
      state.decisions = [];
    });
  }

  function boot() {
    initTabs();

    $('btn-scan').addEventListener('click', doScan);
    $('btn-scan-cancel').addEventListener('click', function () {
      if (!state.scanTaskId) { return; }
      api(R.taskCancel(state.scanTaskId), { method: 'POST' })
        .then(function () { setText($('scan-state-note'), '正在取消…'); })
        .catch(function (e) { showError(e); });
    });
    $('btn-log-scroll').addEventListener('click', function () {
      state.autoScroll = !state.autoScroll;
      $('btn-log-scroll').setAttribute('aria-pressed', state.autoScroll ? 'true' : 'false');
      $('btn-log-scroll').textContent = '自动滚到底：' + (state.autoScroll ? '开' : '关');
    });
    $('btn-plan').addEventListener('click', function () { doPlan(); });
    $('btn-config-current').addEventListener('click', loadCurrentConfig);
    $('btn-quit').addEventListener('click', doQuit);
    $('errbar-close').addEventListener('click', clearError);
    $('errbar-retry').addEventListener('click', function () {
      var fn = state.lastRetry;
      clearError();
      if (fn) { fn(); }
    });

    var hasToken = initToken();
    if (!hasToken) {
      setText($('fact-boot'), '未识别到token');
      showError({
        code: 'unauthorized',
        message: '未识别到token。GUI 的每个请求都要带启动时生成的 token。'
      });
      setBusy(false);
      return;
    }
    setBusy(true);
    loadDecisions().then(loadStatus).then(function () { setBusy(false); });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
