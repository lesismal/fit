// Cluster → a member's page (I142), #/cluster/node/<name>: the member as the manager keeps it and its state (GET
// /api/m/cluster/nodes/<name>), with Test, Trust, Edit and Remove; then what the member itself answers, read through
// the manager (GET .../nodes/<name>/read/<what>, which passes on the member's GET /node, /status, /versions and
// /config/check with the session the poller keeps, and nothing else): who it is and its versions, its running
// config, its listeners, its upstreams and their backends, the certificates it serves, the configs it keeps
// (versions) and its consistency check. Each part says its own problem when the member cannot be read. I143's node
// switcher opens the member's own pages from here.
//
// 集群 → 成员页（I142），#/cluster/node/<name>：管理端保存的成员及其状态（GET /api/m/cluster/nodes/<name>），可测试、
// 信任、编辑与删除；随后是经管理端读取的成员自身应答（GET .../nodes/<name>/read/<what>：以轮询器保持的会话转发成员的
// GET /node、/status、/versions 与 /config/check，不转发其他接口）：成员身份与版本、运行配置、监听、上游及其后端、所提供
// 的证书、保存的配置版本与一致性检查。成员无法读取时，各部分显示各自的问题。I143 的节点切换器从这里打开成员自己的页面。
(function () {
  'use strict';
  var FC = window.FitCluster;
  FC.WORDS.push(
    ['mb.member', 'Member', '成员'], ['mb.state', 'State', '状态'], ['mb.node', 'Node', '节点'],
    ['mb.config', 'Running config', '运行中的配置'], ['mb.listeners', 'Listeners', '监听'],
    ['mb.upstreams', 'Upstreams and backends', '上游与后端'], ['mb.certs', 'Certificates', '证书'],
    ['mb.versions', 'Versions', '配置版本'], ['mb.check', 'Consistency', '一致性检查'], ['mb.tls', 'The certificate it presents', '其出示的证书'],
    ['mb.missing', 'No member is named {name}.', '没有名为 {name} 的成员。'], ['mb.back', 'All nodes', '全部节点'],
    ['mb.id', 'Node ID', '节点 ID'], ['mb.host', 'Host', '主机'], ['mb.versions.sw', 'fit · fib · Go', 'fit · fib · Go'],
    ['mb.platform', 'Platform', '平台'], ['mb.started', 'Started', '启动时间'], ['mb.uptime', 'Uptime', '运行时长'],
    ['mb.roles', 'Roles', '角色'], ['mb.schema', 'Schema', 'Schema'], ['mb.caps', 'Capabilities', '能力'],
    ['mb.version', 'Version', '版本'], ['mb.etag', 'ETag', 'ETag'], ['mb.applied', 'Applied', '应用时间'], ['mb.source', 'Source', '来源'],
    ['mb.reloads', 'Reloads', '重载'], ['mb.reloads.v', '{ok} applied, {failed} failed', '{ok} 次成功，{failed} 次失败'],
    ['mb.conns', 'Connections', '连接'], ['mb.conns.v', '{open} open, {refused} refused', '{open} 个打开，{refused} 个被拒绝'],
    ['mb.name', 'Name', '名称'], ['mb.address', 'Address', '地址'], ['mb.protocols', 'Protocols', '协议'],
    ['mb.backend', 'Backend', '后端'], ['mb.up', 'up', '可用'], ['mb.down', 'down', '不可用'], ['mb.probed', 'probed down', '探测为不可用'],
    ['mb.inflight', 'In flight', '进行中'], ['mb.ok', 'Succeeded', '成功'], ['mb.failed', 'Failed', '失败'],
    ['mb.listener', 'Listener', '监听'], ['mb.names', 'Names', '名称'], ['mb.issuer', 'Issuer', '签发者'], ['mb.expires', 'Expires', '到期'],
    ['mb.current', 'running', '运行中'], ['mb.bytes', 'Bytes', '字节'],
    ['mb.nodiff', 'What runs is what the member\'s config says.', '运行状态与成员的配置一致。'],
    ['mb.diff', '{drift} differences, {info} notes, checked {ago}', '{drift} 处差异，{info} 条说明，检查于 {ago}'],
    ['mb.category', 'Category', '类别'], ['mb.kind', 'Kind', '类型'], ['mb.detail', 'Detail', '详情'],
    ['mb.none', 'None.', '无。'], ['mb.notread', 'Not read: the member is not up.', '未读取：成员当前不可用。'],
    ['mb.via', 'Read through the manager, as the member answers.', '经管理端读取，内容为成员的应答。']
  );

  FC.pages.member = function (UI, sh, params) {
    var h = UI.h, t = UI.t, name = params.name;
    var el = sh.page('nodes', name, t('m.cluster') + ' / ' + t('n.nodes') + ' / ' + name);
    var top = h('div.stack'), parts = h('div.stack');
    var list = null, mem = null, st = null;
    UI.mount(el, h('div.stack', h('div', h('a.small', { href: '#/cluster/nodes' }, '← ' + t('mb.back'))), top, parts));
    UI.mount(top, h('div.row', h('span.spinner'), h('span.muted', t('loading'))));
    UI.mount(sh.tools, UI.btn(t('refresh'), 'refresh', function () { read(true); }, 'ghost sm'));

    function errorBox(e) {
      if (e && e.problem) return FC.problemBox(UI, e.problem);
      return h('div.callout.callout--danger', e && e.message ? e.message : String(e));
    }
    function loadingBox() { return h('div.row', h('span.spinner'), h('span.muted', t('loading'))); }
    // section is a card whose body is drawn once its read is answered. / 读取应答后才绘制主体的卡片。
    function section(title, flush) {
      var box = h('div' + (flush ? '.cl-flush' : ''), loadingBox());
      return { card: UI.card(title, box, null, flush), box: box };
    }

    function drawTop() {
      var readOnly = list.readOnly;
      var e = st && st.error, untrusted = e && e.certificate && (e.kind === 'tls_untrusted' || e.kind === 'tls_pin_mismatch');
      var tools = h('span.btn-row',
        UI.btn(t('fm.test'), 'zap', function () { FC.testSaved(UI, mem, list.etag, function () { read(true); }); }, 'ghost sm'),
        untrusted ? UI.btn(t('ct.trust'), 'shield', function () {
          FC.trustSaved(UI, mem, e.certificate, e.kind === 'tls_pin_mismatch', list.etag).then(function (ok) { if (ok) read(true); });
        }, 'primary sm') : null,
        UI.btn(t('edit'), 'edit', function () {
          (mem.access === 'link' && FC.linkForm ? FC.linkForm : FC.memberForm)(UI, { member: mem, etag: list.etag, reload: function () { read(true); }, then: function (saved) {
            if (saved && saved.name && saved.name !== name) location.hash = '#/cluster/node/' + encodeURIComponent(saved.name);
            else read(true);
          } });
        }, 'ghost sm'),
        UI.btn(t('delete'), 'trash', function () {
          FC.remove(UI, name, list.etag).then(function (ok) { if (ok) location.hash = '#/cluster/nodes'; });
        }, 'ghost sm'));
      if (readOnly) tools.querySelectorAll('button').forEach(function (b, i) { if (i > 0) b.disabled = true; });
      var s = st || {};
      var access = mem.access === 'link' && FC.linkAccess ? FC.linkAccess(UI) : mem.access === 'admin_api' ? t('nd.token') : t('nd.as', { user: mem.username || '?' });
      UI.mount(top,
        readOnly ? h('div.callout.callout--warn', t('readonly', { why: readOnly })) : null,
        UI.card(t('mb.member'), h('div.stack',
          h('div.row.row--between', h('span.cl-health', FC.stateBadge(UI, s.status || 'unknown'),
            h('span.small.muted', ' ' + t('f.since', { ago: UI.ago(FC.sec(s.since)) })),
            s.failures ? h('span.small.muted', ' · ' + t('f.failures', { n: s.failures })) : null, FC.extras(UI, mem, s)), tools),
          s.error ? FC.problemBox(UI, s.error, FC.askMember(UI, s)) : null,
          UI.kv([
            [t('fm.url'), mem.access === 'link' ? '—' : h('code', mem.url)],
            [t('fm.labels'), FC.labelChips(UI, mem.labels) || '—'],
            [t('fm.access'), access],
            [t('fm.tls'), mem.access === 'link' ? '—' : mem.tls && mem.tls.fingerprint ? h('code.cl-fp', mem.tls.fingerprint) : /^https:/i.test(mem.url) ? t(mem.tls && mem.tls.ca ? 'fm.tls.ca' : 'fm.tls.system') : t('nd.http')],
            [t('f.latency'), s.node ? t('f.ms', { n: s.latency_ms }) : '—'],
            [t('f.polled'), s.polled ? UI.ago(FC.sec(s.polled)) : t('st.unknown')]
          ]),
          s.tls ? h('details.cl-details', h('summary', t('mb.tls')), FC.certCard(UI, s.tls)) : null)));
    }

    // readPart reads the member's what through the manager. / 经管理端读取成员的 what。
    function readPart(what) {
      return FC.call(UI, 'GET', FC.nodePath(name) + '/read/' + what).then(function (r) { return r.body || {}; });
    }

    function nodeCard(box, n, s) {
      var c = s.config || {};
      UI.mount(box, UI.kv([
        [t('mb.id'), h('code', n.id || '—')],
        [t('mb.host'), n.hostname],
        [t('mb.versions.sw'), h('code', [n.fit_version, n.fib_version, n.go_version].map(function (v) { return v || '?'; }).join(' · '))],
        [t('mb.platform'), (n.os || '?') + '/' + (n.arch || '?')],
        [t('mb.started'), FC.when(n.started)], [t('mb.uptime'), UI.fmtDur(n.uptime_seconds)],
        [t('mb.roles'), n.roles && n.roles.length ? h('span.btn-row', n.roles.map(function (r) { return UI.badge(r); })) : t('mb.none')],
        [t('mb.schema'), h('code.small', 'v' + n.schema_version + ' ' + (n.schema_digest || ''))],
        [t('mb.caps'), h('span.btn-row', (n.capabilities || []).map(function (x) { return UI.badge(x); }))]
      ]));
      return c;
    }
    function configCard(box, n, status) {
      var rl = status && status.reloads, cn = status && status.connections;
      UI.mount(box, UI.kv([
        [t('mb.version'), String(n.config_version == null ? '—' : n.config_version)],
        [t('mb.etag'), h('code', n.config_etag || '—')],
        [t('mb.applied'), FC.when(n.config_applied)], [t('mb.source'), n.config_source],
        [t('mb.reloads'), rl ? t('mb.reloads.v', { ok: rl.succeeded, failed: rl.failed }) : '—'],
        [t('mb.conns'), cn ? t('mb.conns.v', { open: cn.open, refused: cn.refused }) : '—']
      ]));
    }
    function listenersTable(ls) {
      return UI.table([
        { label: t('mb.name'), render: function (l) { return h('b', l.name || '—'); } },
        { label: t('mb.address'), render: function (l) { return h('code', l.address); } },
        { label: t('mb.protocols'), render: function (l) { return h('span.btn-row', (l.protocols || []).map(function (p) { return UI.badge(p); })); } }
      ], ls, { empty: t('mb.none') });
    }
    function upstreamsBox(ups) {
      if (!ups || !ups.length) return UI.empty(t('mb.none'));
      return h('div.stack.cl-ups', ups.map(function (u) {
        var bs = u.backends || [], down = bs.filter(function (b) { return !b.up; }).length;
        return h('div.cl-up', h('div.row.row--between', h('b', u.name),
          down ? UI.badge(t('f.backends.down', { n: down, all: bs.length }), 'warn') : UI.badge(bs.length + ' / ' + bs.length, 'ok')),
        UI.table([
          { label: t('mb.backend'), render: function (b) { return h('code', b.address); } },
          { label: t('mb.state'), render: function (b) { return b.up ? UI.badge(t('mb.up'), 'ok', true) : UI.badge(t(b.probed_down ? 'mb.probed' : 'mb.down'), 'danger', true); } },
          { label: t('mb.inflight'), cls: 'num', render: function (b) { return String(b.in_flight || 0); } },
          { label: t('mb.ok'), cls: 'num', render: function (b) { return String(b.succeeded || 0); } },
          { label: t('mb.failed'), cls: 'num', render: function (b) { return String(b.failed || 0); } }
        ], bs, { empty: t('mb.none') }));
      }));
    }
    function certsTable(cs) {
      return UI.table([
        { label: t('mb.listener'), render: function (c) { return c.listener; } },
        { label: t('mb.names'), render: function (c) { return (c.names || []).join(', '); } },
        { label: t('mb.issuer'), render: function (c) { return c.issuer || '—'; } },
        { label: t('mb.expires'), render: function (c) { return h('span', FC.expiry(UI, c.not_after), ' ', h('span.small.muted', FC.when(c.not_after))); } }
      ], (cs || []).slice().sort(function (a, b) { return Date.parse(a.not_after) - Date.parse(b.not_after); }), { empty: t('mb.none') });
    }
    function versionsTable(v) {
      return UI.table([
        { label: t('mb.version'), render: function (x) { return h('span', h('b', 'v' + x.version), x.version === v.current ? [' ', UI.badge(t('mb.current'), 'primary')] : null); } },
        { label: t('mb.applied'), render: function (x) { return FC.when(x.applied); } },
        { label: t('mb.source'), render: function (x) { return x.source; } },
        { label: t('mb.bytes'), cls: 'num', render: function (x) { return String(x.bytes); } },
        { label: t('mb.etag'), render: function (x) { return h('code.small', x.etag); } }
      ], (v.versions || []).slice().reverse(), { empty: t('mb.none') });
    }
    function checkBox(c) {
      var sm = c.summary || {}, diffs = c.differences || [];
      var head = !sm.drift && !sm.info ? h('div.callout.callout--ok', UI.icon('check'), ' ', t('mb.nodiff'))
        : h('div.callout' + (sm.drift ? '.callout--warn' : ''), t('mb.diff', { drift: sm.drift || 0, info: sm.info || 0, ago: UI.ago(FC.sec(c.checked_at)) }));
      return h('div.stack', head, diffs.length ? UI.table([
        { label: t('mb.category'), render: function (d) { return d.category; } },
        { label: t('mb.kind'), render: function (d) { return UI.badge(d.kind, d.kind === 'info' ? 'info' : 'warn'); } },
        { label: t('mb.name'), render: function (d) { return h('b', d.name || '—'); } },
        { label: t('mb.detail'), render: function (d) { return h('span.small', d.detail || ''); } }
      ], diffs) : null);
    }

    // drawParts reads the member's node, status, versions and check through the manager, each drawn when it comes,
    // or with its own problem. A member not up is not asked. / 经管理端读取成员的 node、status、versions 与 check，
    // 各自到达即绘制，失败则显示各自的问题；成员不可用时不读取。
    function drawParts() {
      var s = st || {};
      var node = section(t('mb.node')), cfg = section(t('mb.config')), ls = section(t('mb.listeners'), true),
        ups = section(t('mb.upstreams')), certs = section(t('mb.certs'), true), vers = section(t('mb.versions'), true),
        chk = section(t('mb.check'));
      UI.mount(parts, h('div.small.muted', t('mb.via')), h('div.grid.grid--2.cl-top', node.card, cfg.card), ls.card, ups.card, certs.card,
        vers.card, chk.card);
      if (s.status !== 'up') {
        [node, cfg, ls, ups, certs, vers, chk].forEach(function (x) { UI.mount(x.box, h('div.muted.small', t('mb.notread'))); });
        if (s.node) { nodeCard(node.box, s.node, s); configCard(cfg.box, Object.assign({}, s.node, nodeConfig(s)), null); }
        return;
      }
      var caps = (s.node && s.node.capabilities) || [];
      var nodeP = readPart('node');
      var statusP = caps.indexOf('status') >= 0 ? readPart('status') : Promise.resolve(null);
      nodeP.then(function (n) { if (!document.body.contains(el)) return; nodeCard(node.box, n, s); }, function (e) { UI.mount(node.box, errorBox(e)); });
      Promise.all([nodeP.catch(function () { return Object.assign({}, s.node || {}, nodeConfig(s)); }), statusP.catch(function () { return null; })]).then(function (r) {
        if (document.body.contains(el)) configCard(cfg.box, r[0], r[1]);
      });
      statusP.then(function (x) {
        if (!document.body.contains(el)) return;
        if (!x) { [ls, ups, certs].forEach(function (p) { UI.mount(p.box, FC.problemBox(UI, { kind: 'unsupported', message: 'GET /status' })); }); return; }
        UI.mount(ls.box, listenersTable(x.listeners || []));
        UI.mount(ups.box, upstreamsBox(x.upstreams || []));
        UI.mount(certs.box, certsTable(x.certificates || []));
      }, function (e) { [ls, ups, certs].forEach(function (p) { UI.mount(p.box, errorBox(e)); }); });
      readPart('versions').then(function (v) { if (document.body.contains(el)) UI.mount(vers.box, versionsTable(v)); }, function (e) { UI.mount(vers.box, errorBox(e)); });
      readPart('check').then(function (c) { if (document.body.contains(el)) UI.mount(chk.box, checkBox(c)); }, function (e) { UI.mount(chk.box, errorBox(e)); });
    }
    // nodeConfig is the running config as the state keeps it, in GET /node's names. / 状态中保存的运行配置（按 GET /node 的字段名）。
    function nodeConfig(s) {
      var c = s.config || {};
      return { config_version: c.version, config_etag: c.etag, config_applied: c.applied, config_source: c.source };
    }

    // read reads the member and its state; all reads the member's parts again too. / 读取成员及其状态；all 时也重新读取成员各部分。
    var wasUp = null;
    function read(all) {
      return FC.members(UI).then(function (l) {
        list = l;
        mem = l.nodes.filter(function (m) { return m.name === name; })[0] || null;
        if (!mem) { UI.mount(top, UI.empty(t('mb.missing', { name: name }))); UI.clear(parts); return; }
        return FC.call(UI, 'GET', FC.nodePath(name)).then(function (r) {
          if (!document.body.contains(el)) return;
          st = (r.body && r.body.state) || null;
          drawTop();
          var up = !!st && st.status === 'up';
          if (all || up !== wasUp) drawParts();
          wasUp = up;
        });
      }).catch(function (e) {
        if (!document.body.contains(el) || e.status === 401) return;
        UI.mount(top, h('div.callout.callout--danger', e.message));
      });
    }
    read(true);
    // The state follows the poller; the member's parts are read again when it comes up or goes down, or on Refresh.
    // 状态跟随轮询器；成员恢复或离线时，或点击刷新时，重新读取其各部分。
    UI.every(10000, function () { if (!document.querySelector('.modal')) read(false); });
  };
})();
