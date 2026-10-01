/* Cartography: five drawings of one chain, as a place rather than a table.
 *
 * ---------------------------------------------------------------------------
 * Why a file of its own
 *
 * index.html is 450 KB and every deep link into the site serves all of it. A
 * page that most readers will never open has no business in that number, so
 * this module and its stylesheet are fetched the first time /cartography is
 * routed to and never otherwise. index.html carries the route, an empty
 * container and a ten-line loader; everything below is the experiment.
 *
 * ---------------------------------------------------------------------------
 * Why five and not one
 *
 * The site already has the honest picture: /contracts is a force-directed
 * graph, and it is the right answer to "what is connected to what". It is a
 * poor answer to "what does this chain look like", because a reader gets no
 * sense of scale, age, or neighbourhood from a hairball, and no sense at all
 * of the people using it.
 *
 * So this is a deliberate set of metaphors, each committing to one question
 * the others cannot answer, and each saying in its caption which quantity is
 * which. They share one data load and one set of controls.
 *
 *   city        how big is this chain and how much of it is awake
 *   settlement  who actually uses it, and which realms share their people
 *   orbits      when was it built, namespace by namespace
 *   metro       what does everything stand on
 *   relief      where is the chain dense, ignoring who owns what
 *
 * ---------------------------------------------------------------------------
 * What it reads
 *
 * Nothing new. /api/contracts/map (one row per deployed package, with windowed
 * calls and all-time gas and storage), /api/contracts/edges (imports, and
 * caller overlap), /api/graph/callers (address to realm, with call counts).
 * All three already serve pages on this site. Adding a cartography-shaped
 * endpoint would have made this a second source of truth for numbers the rest
 * of the site already answers, which is the one thing /lab promises not to be.
 *
 * ---------------------------------------------------------------------------
 * Honesty rules these drawings follow
 *
 * A picture is easier to believe than a table and just as easy to get wrong,
 * so three rules hold across all five:
 *
 *  1. Nothing is invented. Every position that carries meaning is derived from
 *     a field. Where a position is arbitrary (which slot in a district a
 *     building lands in) it is stated as arbitrary in the caption, and it is
 *     seeded from the path so it does not move between reloads.
 *  2. Nothing is silently dropped. A view that shows the top N says N and says
 *     what the rest were, in the caption, with numbers.
 *  3. Zero is drawn. A realm nobody called in the window is a dark building,
 *     not a missing one: on mainnet that is 499 of 589 packages, and a map
 *     that omitted them would show a busy chain instead of a quiet one with a
 *     few busy corners.
 */

(function () {
'use strict';

// el, badge, api, fmtNum, navigate and friends are index.html's, already on the
// page by the time this module is fetched. Nothing here redefines them; this
// module owns a single global, the entry point index.html calls.

var W = 1200;               // SVG user-space width. The stage scales to fit.
var TIP = null;             // the one tooltip element, created on first use

// -----------------------------------------------------------------------------
// State
// -----------------------------------------------------------------------------

var S = {
  view: 'city',
  window: '30d',
  metric: 'calls',
  roads: true,     // the city's import highways between districts
  scale: 'log',    // how a metric becomes a size: 'log' or 'linear'
  yaw: 0,          // the city's compass, in 15-degree steps
  flat: false,     // the city as a plan rather than a model
  tilt: 0.42,      // the orbits' viewing angle: 1 is overhead, 0.12 is edge-on
  sun: 315,        // the relief's light direction, degrees clockwise from north
  gen: 0,          // bumped per load; a late response checks it before painting
  data: null,      // { nodes, imports, callers, people }
  dataKey: null,   // network + window the cached data is for
  reliefPos: null, // the relief layout, which depends on the data and not the metric
  reliefFor: null, // the dataKey reliefPos was laid out for
  anim: null,      // requestAnimationFrame handle owned by the current view
  // Per-view camera, kept across a redraw so changing the metric does not also
  // throw away where the reader had navigated to. Keyed by view because the
  // five are different spaces and a pan in one means nothing in another.
  cam: {},
};

var VIEWS = [
  { id: 'city',       name: 'city',       blurb: 'districts by namespace, one building per package, storeys by activity' },
  { id: 'settlement', name: 'settlement', blurb: 'villages and the people walking between them, from real caller counts' },
  { id: 'orbits',     name: 'orbits',     blurb: 'one solar system per namespace, orbit radius by deploy date' },
  { id: 'metro',      name: 'metro',      blurb: 'the import graph as transit lines, interchanges where code is shared' },
  { id: 'relief',     name: 'relief',     blurb: 'a contour map of where the chain is dense, owner-blind' },
];

// pure says whether a pure package may take a size from this metric.
//
// Calls, callers and gas are quantities a pure package does not have: it holds
// no state, receives no calls of its own, and the gas attributed to it is
// really its importers' gas. Drawing a tower from those would be the picture
// lying in the one channel a reader trusts, so the city flattens them.
//
// Storage and importers are the opposite case. Source bytes are the package's
// own, and being depended on is the *only* quantity a pure package has:
// p/nt/ufmt has 168 dependents and zero of everything else. Flattening it
// under "imported" would hide the single most load-bearing thing on the chain,
// which is the mistake this flag exists to stop.
var METRICS = {
  calls:     { label: 'calls',    get: function (n) { return n.calls; },          pure: false },
  callers:   { label: 'callers',  get: function (n) { return n.unique_callers; }, pure: false },
  gas:       { label: 'gas',      get: function (n) { return n.gas_used; },       pure: false },
  storage:   { label: 'storage',  get: function (n) { return n.storage_bytes; },  pure: true  },
  importers: { label: 'imported', get: function (n) { return n.importers; },      pure: true  },
};

var WINDOWS = ['24h', '7d', '30d', '90d', 'all'];

// -----------------------------------------------------------------------------
// Small helpers
// -----------------------------------------------------------------------------

function svgEl(tag, attrs) {
  var e = document.createElementNS('http://www.w3.org/2000/svg', tag);
  if (attrs) for (var k in attrs) if (attrs[k] !== null && attrs[k] !== undefined) e.setAttribute(k, attrs[k]);
  return e;
}

// hash32 is FNV-1a. Used wherever a layout needs a stable arbitrary number:
// which slot a building takes inside its district, which way a moon leans.
// Stable is the whole point, so the same chain draws the same picture on every
// reload and a reader can recognise the place they were looking at.
function hash32(s) {
  var h = 2166136261;
  for (var i = 0; i < s.length; i++) { h ^= s.charCodeAt(i); h = Math.imul(h, 16777619); }
  return h >>> 0;
}

// rngFrom is a deterministic 0..1 generator seeded by a string, for the same
// reason as hash32: a layout that jittered differently on every paint would
// make a reader think the chain had changed when only the page had reloaded.
function rngFrom(seed) {
  var s = hash32(seed) || 1;
  return function () {
    s ^= s << 13; s >>>= 0;
    s ^= s >> 17;
    s ^= s << 5;  s >>>= 0;
    return s / 4294967296;
  };
}

// Namespace colour. Golden-angle around the wheel keyed by a hash of the name,
// so 35 namespaces come out distinguishable without a hand-written palette
// that would go stale the moment a 36th deployed. Saturation and lightness are
// fixed, which is what keeps the set reading as one system on a dark ground.
var _hue = {};
function nsHue(ns) {
  if (_hue[ns] === undefined) _hue[ns] = (hash32(ns) * 137.508) % 360;
  return _hue[ns];
}
function nsColor(ns, l, s) { return 'hsl(' + nsHue(ns).toFixed(1) + ',' + (s || 52) + '%,' + (l || 58) + '%)'; }

// lg is the log transform, used wherever the *drawing* needs a compressed
// number regardless of what the reader asked for: a road's stroke width, a
// traveller's speed, a star's radius. Nothing a reader compares across two
// shapes goes through this directly.
function lg(v) { return Math.log10(1 + Math.max(0, v || 0)); }

// sc is the one a reader controls, and the reason the control exists.
//
// Most quantities on this chain are power-law: 8071 calls at the top and zero
// for 499 of 589 packages. Under a linear scale that draws one tower and 588
// paving slabs, which is the honest picture of the distribution and a useless
// picture of everything below the top. Under log it is a legible city that
// understates how extreme the top really is.
//
// Neither is the right default for every question, so both are offered and the
// caption always says which one is on. Log is the default because the first
// thing a reader wants is to see the shape of the chain at all.
function sc(v) { return S.scale === 'linear' ? Math.max(0, v || 0) : lg(v); }

function fmtBytes(b) {
  if (!b) return '0 B';
  var u = ['B', 'KB', 'MB', 'GB'], i = 0, v = b;
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
  return (i ? v.toFixed(1) : v) + ' ' + u[i];
}

function shortAddr(a) { return a && a.length > 14 ? a.slice(0, 8) + '…' + a.slice(-4) : a; }

// The path a reader clicks through to. Realms have a detail page; pure
// packages do not, so they go to the hub, which is where their source is.
function pathHref(n) {
  var rest = n.path.replace(/^gno\.land\//, '');
  return (n.is_realm ? '/realm/' + rest : '/gnohub/' + rest) + (window.netSuffix ? window.netSuffix() : '');
}

// -----------------------------------------------------------------------------
// The tooltip
// -----------------------------------------------------------------------------

function tip() {
  if (!TIP) { TIP = document.createElement('div'); TIP.className = 'carto-tip'; document.body.appendChild(TIP); }
  return TIP;
}

function tipShow(ev, rows) {
  var t = tip();
  t.textContent = '';
  rows.forEach(function (r) {
    if (typeof r === 'string') {
      var h = document.createElement('div'); h.className = 't-p'; h.textContent = r; t.appendChild(h);
      return;
    }
    var line = document.createElement('div'); line.className = 't-r';
    var k = document.createElement('span'); k.className = 't-k'; k.textContent = r[0];
    var v = document.createElement('span'); v.textContent = r[1];
    line.appendChild(k); line.appendChild(v); t.appendChild(line);
  });
  t.style.display = 'block';
  tipMove(ev);
}

function tipMove(ev) {
  if (!TIP || TIP.style.display === 'none') return;
  // Flipped rather than clamped near the right and bottom edges: a tooltip
  // clamped to the viewport sits on top of the thing it describes, which is
  // exactly the shape the reader is trying to look at.
  var r = TIP.getBoundingClientRect();
  var x = ev.clientX + 14, y = ev.clientY + 16;
  if (x + r.width > window.innerWidth - 8) x = ev.clientX - r.width - 14;
  if (y + r.height > window.innerHeight - 8) y = ev.clientY - r.height - 16;
  TIP.style.left = Math.max(4, x) + 'px';
  TIP.style.top = Math.max(4, y) + 'px';
}

function tipHide() { if (TIP) TIP.style.display = 'none'; }

// Wiring for one shape: hover shows the card, click navigates. Bound per shape
// rather than delegated because the canvas views have no shapes to delegate
// from and would need a second mechanism anyway.
function bindNode(shape, n, extra) {
  shape.setAttribute('data-cp', n.path);
  shape.addEventListener('mousemove', function (ev) { tipShow(ev, nodeCard(n, extra)); });
  shape.addEventListener('mouseleave', tipHide);
  shape.addEventListener('click', function () { tipHide(); window.navigate(pathHref(n)); });
}

function nodeCard(n, extra) {
  var rows = [n.path];
  rows.push(['kind', (n.is_realm ? 'realm' : 'package') + (n.parked ? ' · parked' : '')]);
  rows.push(['calls (' + S.window + ')', window.fmtNum(n.calls)]);
  rows.push(['callers', window.fmtNum(n.unique_callers)]);
  rows.push(['imported by', window.fmtNum(n.importers)]);
  rows.push(['imports', window.fmtNum(n.imports)]);
  rows.push(['storage', fmtBytes(n.storage_bytes)]);
  if (n.deployed_at) rows.push(['deployed', n.deployed_at.slice(0, 10)]);
  if (extra) extra.forEach(function (r) { rows.push(r); });
  return rows;
}

// -----------------------------------------------------------------------------
// The camera: pan and zoom, shared by every view
// -----------------------------------------------------------------------------

// mountSVG takes a finished svg, wraps everything already in it in one group,
// and makes that group pannable and zoomable. One call per view, in place of
// stage.appendChild(svg), so no view has to know this exists.
//
// A group transform rather than a viewBox rewrite, deliberately: the viewBox is
// what fits the drawing to the stage in the first place, and editing it would
// fight preserveAspectRatio and make the reset button a different calculation
// per view. A transform composes cleanly and resets to the identity.
//
// Strokes are left to scale with the zoom. The alternative, dividing every
// stroke-width by k on each frame, keeps lines a constant pixel width, and that
// is wrong here: zooming into the city should magnify the city, not magnify the
// buildings while the roads stay hairlines on top of them.
function mountSVG(stage, svg, opts) {
  var o = opts || {};
  var g = svgEl('g');
  while (svg.firstChild) g.appendChild(svg.firstChild);
  svg.appendChild(g);
  stage.appendChild(svg);

  var cam = S.cam[S.view] || { x: 0, y: 0, k: 1 };
  S.cam[S.view] = cam;

  function apply() {
    g.setAttribute('transform', 'translate(' + cam.x + ',' + cam.y + ') scale(' + cam.k + ')');
    stage.classList.toggle('carto-zoomed', cam.k !== 1 || cam.x !== 0 || cam.y !== 0);
  }
  apply();

  // Wheel zoom about the cursor, so the thing under the pointer stays under the
  // pointer. That is the whole difference between a zoom a reader can aim and
  // one they have to chase with the pan.
  //
  // Not passive: a map that scrolls the page out from under itself on the first
  // wheel tick is unusable, and preventDefault is the only way to stop that.
  svg.addEventListener('wheel', function (ev) {
    ev.preventDefault();
    var r = svg.getBoundingClientRect();
    // Into the svg's own user space first: the stage scales the viewBox to fit,
    // so a client pixel is not a user unit and treating it as one makes the
    // zoom drift away from the cursor on every tick.
    var vb = svg.viewBox.baseVal;
    var ux = vb.x + (ev.clientX - r.left) / r.width * vb.width;
    var uy = vb.y + (ev.clientY - r.top) / r.height * vb.height;
    var f = Math.exp(-ev.deltaY * 0.0014);
    var k = Math.min(24, Math.max(0.4, cam.k * f));
    f = k / cam.k;
    cam.x = ux - (ux - cam.x) * f;
    cam.y = uy - (uy - cam.y) * f;
    cam.k = k;
    apply();
    if (o.onZoom) o.onZoom(cam);
  }, { passive: false });

  // Drag to pan. Pointer events rather than mouse events so a trackpad, a
  // stylus and a touch drag all work from one implementation, and setPointer-
  // Capture so a drag that leaves the stage keeps panning instead of sticking.
  var drag = null;
  svg.addEventListener('pointerdown', function (ev) {
    if (ev.button !== 0) return;
    var r = svg.getBoundingClientRect();
    var vb = svg.viewBox.baseVal;
    drag = { x: ev.clientX, y: ev.clientY, sx: vb.width / r.width, sy: vb.height / r.height,
      moved: false, id: ev.pointerId };
    // Capture is deliberately NOT taken here. Capturing on pointerdown makes
    // the svg the target of the subsequent click, so a plain click on a
    // building never reaches that building's handler and nothing is clickable
    // any more. It is taken below, the moment a drag is real, which is the only
    // point at which it is needed (to keep panning when the pointer leaves).
  });
  svg.addEventListener('pointermove', function (ev) {
    if (!drag) return;
    var dx = (ev.clientX - drag.x) * drag.sx, dy = (ev.clientY - drag.y) * drag.sy;
    if (!drag.moved && Math.abs(dx) + Math.abs(dy) < 3) return;
    if (!drag.moved) {
      drag.moved = true;
      try { svg.setPointerCapture(drag.id); } catch (_) { /* pointer already gone */ }
    }
    stage.classList.add('carto-dragging');
    cam.x += dx; cam.y += dy;
    drag.x = ev.clientX; drag.y = ev.clientY;
    apply();
    tipHide();
  });
  // A drag that moved has to swallow the click that follows it, or releasing
  // the pointer over a building navigates to it and the reader loses the view
  // they just framed.
  //
  // A flag cleared on the next task, not a one-shot listener. The listener
  // version removed itself when it fired, and a drag whose press and release
  // land on different elements produces no click at all: the listener then
  // stayed armed and ate the reader's *next* genuine click, so after one pan
  // nothing on the map was clickable until a redraw. A flag that clears itself
  // whether or not a click arrives cannot do that.
  var swallow = false;
  svg.addEventListener('click', function (e) {
    if (!swallow) return;
    e.stopPropagation();
    e.preventDefault();
  }, true);

  function endDrag(ev) {
    if (!drag) return;
    if (drag.moved) {
      swallow = true;
      setTimeout(function () { swallow = false; }, 0);
    }
    stage.classList.remove('carto-dragging');
    if (ev && ev.pointerId !== undefined && svg.hasPointerCapture(ev.pointerId)) {
      svg.releasePointerCapture(ev.pointerId);
    }
    drag = null;
  }
  svg.addEventListener('pointerup', endDrag);
  svg.addEventListener('pointercancel', endDrag);

  // Double-click zooms in a step, which is the gesture people try first.
  svg.addEventListener('dblclick', function (ev) {
    ev.preventDefault();
    svg.dispatchEvent(new WheelEvent('wheel', {
      deltaY: ev.shiftKey ? 320 : -320, clientX: ev.clientX, clientY: ev.clientY, bubbles: false }));
  });

  stage.appendChild(camChrome(function () {
    cam.x = 0; cam.y = 0; cam.k = 1; apply(); if (o.onZoom) o.onZoom(cam);
  }, function (f) {
    var r = svg.getBoundingClientRect();
    svg.dispatchEvent(new WheelEvent('wheel', {
      deltaY: f, clientX: r.left + r.width / 2, clientY: r.top + r.height / 2, bubbles: false }));
  }));
  return { cam: cam, apply: apply };
}

// The zoom buttons and the reset. A wheel is not available to everyone and is
// awkward on a trackpad inside a page that also scrolls, so the same two
// operations are reachable by click.
function camChrome(reset, zoom) {
  var el = window.el;
  var box = el('div', { className: 'carto-cam' });
  var mk = function (label, title, fn) {
    var b = el('button', { title: title }, label);
    b.addEventListener('click', function (e) { e.preventDefault(); fn(); });
    return b;
  };
  box.appendChild(mk('+', 'zoom in', function () { zoom(-320); }));
  box.appendChild(mk('\u2212', 'zoom out', function () { zoom(320); }));
  box.appendChild(mk('reset', 'back to the whole map', reset));
  return box;
}

// -----------------------------------------------------------------------------
// Data
// -----------------------------------------------------------------------------

// One load for all five views. The import graph ignores the window by design
// (it is a property of deployed source, not of traffic), the other three
// honour it, and the /api/graph/callers day count is derived from it so the
// people on the settlement map are the same people the buildings are lit by.
//
// Two of the four are allowed to fail. A chain with no caller graph still has
// a city worth drawing, and a view that refused to paint because an optional
// query 500'd would be a worse answer than one that says the layer is missing.
function cartoLoad() {
  var net = window.getNetwork ? window.getNetwork() : '';
  var key = net + '|' + S.window;
  if (S.dataKey === key && S.data) return Promise.resolve(S.data);

  var days = { '24h': 1, '7d': 7, '30d': 30, '90d': 90, 'all': 3650 }[S.window] || 30;
  var soft = function (p) { return p.then(function (r) { return r; }, function () { return null; }); };

  // The three contracts endpoints resolve "all networks" to the first
  // configured chain themselves and say in the response which one they picked:
  // a bubble is identified by its path, 193 paths exist on more than one chain,
  // and a blended map would draw one building carrying two chains' traffic.
  //
  // /api/graph/callers does not have that fallback and answers 400 for "all",
  // which is why the people layer is fetched second rather than alongside: it
  // is asked for the chain the map actually resolved to. Serial by necessity,
  // and only on a cold window.
  return Promise.all([
    window.api('contracts/map?window=' + S.window),
    soft(window.api('contracts/edges?kind=imports')),
    soft(window.api('contracts/edges?kind=callers&window=' + S.window)),
  ]).then(function (r) {
    var resolved = r[0].network || net;
    return soft(window.api('graph/callers?days=' + days + '&topN=80&min_calls=1', resolved))
      .then(function (pe) { return [r[0], r[1], r[2], pe, resolved]; });
  }).then(function (r) {
    var d = {
      network: r[4],
      nodes: r[0].nodes || [],
      imports: (r[1] && r[1].edges) || [],
      overlap: (r[2] && r[2].edges) || [],
      people: (r[3] && r[3].edges) || [],
      peopleNodes: (r[3] && r[3].nodes) || [],
    };
    // byPath is built once here because four of the five views resolve an edge
    // endpoint to a node, and doing it with .find() over 589 rows per edge was
    // the whole cost of the metro view on the first draft.
    d.byPath = {};
    d.nodes.forEach(function (n) { d.byPath[n.path] = n; });
    S.data = d; S.dataKey = key;
    return d;
  });
}

// Namespaces, largest first, with their members. The cluster key is the store's
// own NamespaceOf: the first path element after the r/ or p/ marker, which puts
// a deployer's whole family of realms under one roof.
function groupNamespaces(nodes) {
  var by = {};
  nodes.forEach(function (n) {
    var ns = n.namespace || '?';
    (by[ns] = by[ns] || { ns: ns, nodes: [] }).nodes.push(n);
  });
  var out = Object.keys(by).map(function (k) { return by[k]; });
  out.forEach(function (g) {
    g.n = g.nodes.length;
    g.calls = g.nodes.reduce(function (a, n) { return a + n.calls; }, 0);
    g.live = g.nodes.filter(function (n) { return n.calls > 0; }).length;
    g.storage = g.nodes.reduce(function (a, n) { return a + n.storage_bytes; }, 0);
    // Deterministic within a namespace, so the same building sits in the same
    // slot on every reload.
    g.nodes.sort(function (a, b) { return b.calls - a.calls || a.path.localeCompare(b.path); });
  });
  out.sort(function (a, b) { return b.n - a.n || a.ns.localeCompare(b.ns); });
  return out;
}

// -----------------------------------------------------------------------------
// Chrome: the picker, the controls, the stage
// -----------------------------------------------------------------------------

function render(root) {
  var el = window.el;
  root.textContent = '';

  root.appendChild(el('div', { className: 'carto-intro' },
    el('h2', {}, 'cartography'),
    el('p', {}, 'The same chain the rest of the site tabulates, drawn as a place. Five metaphors, ' +
      'one data load, no endpoint of their own: every number here comes from the API that serves ' +
      '/contracts, /accounts and /gas, so a figure that disagrees with one of those pages is a bug ' +
      'in the drawing and not a second opinion. Each view says in its caption which quantity it ' +
      'put in which channel, because a picture is easier to believe than a table and just as easy ' +
      'to read wrong.')));

  var pick = el('div', { className: 'carto-pick' });
  VIEWS.forEach(function (v) {
    var b = el('button', { className: S.view === v.id ? 'on' : '' },
      el('b', {}, v.name), document.createTextNode(v.blurb));
    b.addEventListener('click', function () {
      if (S.view === v.id) return;
      S.view = v.id;
      writeURL();
      render(root);
    });
    pick.appendChild(b);
  });
  root.appendChild(pick);

  root.appendChild(controls(root));

  var stage = el('div', { className: 'carto-stage', id: 'carto-stage' });
  root.appendChild(stage);
  root.appendChild(el('div', { id: 'carto-below' }));

  draw();
}

// restate rebuilds the control bar in place and repaints the drawing, without
// touching the intro or the view picker.
//
// render() would do both and also rebuild those, which costs the reader their
// scroll position on a page where the drawing is below the fold. Every control
// goes through here; none of them calls render().
function restate(root) {
  var bar = document.getElementById('carto-bar');
  if (bar) bar.replaceWith(controls(root));
  draw();
}

// btnGroup is the one shape every control in this bar has: a label, then a row
// of buttons of which exactly one is on.
function btnGroup(label, options, isOn, pick) {
  var el = window.el;
  var g = el('div', { className: 'carto-grp' }, el('span', {}, label));
  options.forEach(function (o) {
    var b = el('button', { className: isOn(o[1]) ? 'on' : '', title: o[2] || '' }, o[0]);
    b.addEventListener('click', function () { pick(o[1]); });
    g.appendChild(b);
  });
  return g;
}

function controls(root) {
  var el = window.el;
  var bar = el('div', { className: 'carto-bar', id: 'carto-bar' });
  var sized = S.view === 'city' || S.view === 'orbits' || S.view === 'relief';

  bar.appendChild(btnGroup('window', WINDOWS.map(function (w) { return [w, w]; }),
    function (v) { return S.window === v; },
    function (v) { if (S.window !== v) { S.window = v; writeURL(); restate(root); } }));

  // The metric picker is hidden on the two views that do not have a size
  // channel to give it. A control that is present and inert is worse than one
  // that is absent: a reader clicks it, nothing moves, and they conclude the
  // page is broken rather than that the control does not apply.
  if (sized) {
    bar.appendChild(btnGroup('size by',
      Object.keys(METRICS).map(function (m) { return [METRICS[m].label, m]; }),
      function (v) { return S.metric === v; },
      function (v) { if (S.metric !== v) { S.metric = v; writeURL(); restate(root); } }));

    // Log or linear, wherever a metric becomes a size.
    //
    // This is not a cosmetic preference. On mainnet the top realm has 8071
    // calls and 499 of 589 packages have none, so linear draws one tower and
    // 588 slabs: the true shape of the distribution, and useless for seeing
    // anything below the top. Log makes the city legible and understates how
    // extreme the top is. Both are worth having and the caption says which is
    // on, which is the only way the picture stays honest either way.
    bar.appendChild(btnGroup('scale', [['log', 'log', 'compresses a power-law into a legible range'],
      ['linear', 'linear', 'true proportions: one tower and a lot of slabs']],
      function (v) { return S.scale === v; },
      function (v) { if (S.scale !== v) { S.scale = v; writeURL(); restate(root); } }));
  }

  // Roads belong to the city and nowhere else: the metro view is entirely
  // about imports and the others place by something imports cannot express.
  if (S.view === 'city') {
    bar.appendChild(btnGroup('roads', [['on', true], ['off', false]],
      function (v) { return S.roads === v; },
      function (v) { if (S.roads !== v) { S.roads = v; writeURL(); restate(root); } }));

    bar.appendChild(btnGroup('view', [['model', false, 'isometric, with heights'],
      ['plan', true, 'straight down, no heights: nothing hides behind a tower']],
      function (v) { return S.flat === v; },
      function (v) { if (S.flat !== v) { S.flat = v; writeURL(); restate(root); } }));

    // The compass. Fifteen-degree steps rather than a slider: the whole city is
    // rebuilt per step (589 buildings, ~1800 polygons), so a control that fires
    // on every pixel of a drag would queue redraws faster than they finish.
    var rot = el('div', { className: 'carto-grp' }, el('span', {}, 'rotate'));
    [['\u21ba', -15], ['\u21bb', 15]].forEach(function (o) {
      var b = el('button', { title: 'turn the city ' + (o[1] < 0 ? 'left' : 'right') + ' 15\u00b0' }, o[0]);
      b.addEventListener('click', function () {
        S.yaw = (((S.yaw + o[1]) % 360) + 360) % 360; writeURL(); restate(root);
      });
      rot.appendChild(b);
    });
    var deg = el('button', { className: S.yaw ? 'on' : '', title: 'back to north' }, S.yaw + '\u00b0');
    deg.addEventListener('click', function () { if (S.yaw) { S.yaw = 0; writeURL(); restate(root); } });
    rot.appendChild(deg);
    bar.appendChild(rot);
  }

  // The orbits' viewing angle, which is the one genuinely three-dimensional
  // thing on that drawing: overhead reads the rings as circles and makes two
  // systems comparable, edge-on stacks them and makes a single system's
  // deploy history read as a timeline.
  if (S.view === 'orbits') {
    bar.appendChild(btnGroup('tilt', [['overhead', 1], ['angled', 0.42], ['edge-on', 0.12]],
      function (v) { return Math.abs(S.tilt - v) < 0.01; },
      function (v) { S.tilt = v; writeURL(); restate(root); }));
  }

  // The relief's light. Hillshading is what turns a banded field into terrain a
  // reader can see the shape of, and the direction it comes from decides which
  // slopes are legible, so it is the reader's to move.
  if (S.view === 'relief') {
    bar.appendChild(btnGroup('light', [['NW', 315], ['NE', 45], ['SE', 135], ['SW', 225]],
      function (v) { return S.sun === v; },
      function (v) { if (S.sun !== v) { S.sun = v; writeURL(); restate(root); } }));
  }

  return bar;
}

function writeURL() {
  var q = new URLSearchParams(window.location.search);
  q.set('v', S.view); q.set('w', S.window); q.set('m', S.metric);
  q.set('r', S.roads ? '1' : '0');
  q.set('s', S.scale);
  if (S.yaw) q.set('yaw', String(S.yaw)); else q.delete('yaw');
  if (S.flat) q.set('flat', '1'); else q.delete('flat');
  q.set('tilt', String(S.tilt));
  q.set('sun', String(S.sun));
  var net = window.getNetwork ? window.getNetwork() : null;
  if (net && net !== 'all') q.set('network', net); else q.delete('network');
  history.replaceState(null, '', '/cartography?' + q.toString());
}

function readURL() {
  var q = new URLSearchParams(window.location.search);
  var v = q.get('v'), w = q.get('w'), m = q.get('m');
  if (v && VIEWS.some(function (x) { return x.id === v; })) S.view = v;
  if (w && WINDOWS.indexOf(w) >= 0) S.window = w;
  if (m && METRICS[m]) S.metric = m;
  var r = q.get('r');
  if (r === '0' || r === '1') S.roads = r === '1';
  var sc2 = q.get('s');
  if (sc2 === 'log' || sc2 === 'linear') S.scale = sc2;
  // Every one of these is clamped rather than trusted. A hand-edited yaw of
  // 1e9 or a tilt of 0 is not a crash, but it is a drawing nobody can read,
  // and a shared link is exactly where a nonsense value arrives from.
  var yaw = parseFloat(q.get('yaw'));
  if (isFinite(yaw)) S.yaw = ((Math.round(yaw / 15) * 15 % 360) + 360) % 360;
  S.flat = q.get('flat') === '1';
  var tilt = parseFloat(q.get('tilt'));
  if (isFinite(tilt)) S.tilt = Math.min(1, Math.max(0.12, tilt));
  var sun = parseFloat(q.get('sun'));
  if (isFinite(sun)) S.sun = ((Math.round(sun) % 360) + 360) % 360;
}

function stopAnim() {
  if (S.anim) { cancelAnimationFrame(S.anim); S.anim = null; }
}

function draw() {
  var gen = ++S.gen;
  stopAnim();
  tipHide();
  var stage = document.getElementById('carto-stage');
  var below = document.getElementById('carto-below');
  stage.textContent = '';
  below.textContent = '';
  var sk = window.el('div', { className: 'skeleton', style: { height: '520px' } });
  stage.appendChild(sk);

  cartoLoad().then(function (d) {
    if (gen !== S.gen) return;
    stage.textContent = '';
    // Which chain is on screen, always, even when the selector says "all
    // networks": these endpoints resolve that to one chain and it is not
    // obvious which. A map that quietly drew mainnet while the header said
    // "all" would be the picture lying by omission.
    var bar = document.getElementById('carto-bar');
    if (bar && !bar.querySelector('.carto-net')) {
      bar.appendChild(window.el('div', { className: 'carto-grp carto-net' },
        window.el('span', {}, 'chain'), window.el('b', {}, d.network || 'unknown')));
    }
    if (!d.nodes.length) {
      stage.appendChild(window.el('div', { className: 'carto-empty' },
        'no packages indexed on this chain yet, nothing to draw'));
      return;
    }
    ({ city: drawCity, settlement: drawSettlement, orbits: drawOrbits,
       metro: drawMetro, relief: drawRelief })[S.view](stage, below, d);
  }, function (e) {
    if (gen !== S.gen) return;
    stage.textContent = '';
    stage.appendChild(window.el('div', { className: 'carto-err' },
      'could not load the map: ' + e.message));
  });
}

function note(below, parts) {
  var n = window.el('div', { className: 'carto-note' });
  parts.forEach(function (p) {
    if (typeof p === 'string') n.appendChild(document.createTextNode(p));
    else n.appendChild(window.el('b', {}, p[0]));
  });
  below.appendChild(n);
  return n;
}

function legend(below, items) {
  var l = window.el('div', { className: 'carto-legend' });
  items.forEach(function (it) {
    var s = window.el('span', {});
    if (it[0]) { var i = window.el('i', {}); i.style.background = it[0]; s.appendChild(i); }
    s.appendChild(window.el('b', {}, it[1]));
    if (it[2]) s.appendChild(document.createTextNode(' ' + it[2]));
    l.appendChild(s);
  });
  below.appendChild(l);
  return l;
}

// =============================================================================
// 1. CITY  -- isometric, one building per package, districts by namespace
// =============================================================================
//
// The literal answer to "draw the chain as a city". Every deployed package is
// a building; the namespace it belongs to is its district; the district's size
// on the ground is how many packages it holds.
//
// Four channels, and the caption names all four:
//
//   footprint  fixed. A building's plot does not mean anything, and a city
//              whose plots varied would read as if it did.
//   storeys    the chosen metric, log-scaled, floor of one. A package with
//              nothing in the metric is a single-storey shed, not a hole.
//   hue        namespace. Same hash as everywhere else on this page.
//   windows    lit if the package was called in the window. This is the
//              channel that carries the chain's real story: on mainnet over
//              30d, 90 of 589 buildings light up.
//
// The isometric projection is the standard 2:1 dimetric one, computed here
// rather than by a library: it is four multiplications, and pulling in a 3D
// renderer for a picture that never rotates would be 200 KB for nothing.

var ISO = { tw: 26, th: 13, storey: 9 };   // tile half-width, half-height, storey height

// The camera for the city, as three numbers the reader controls.
//
// _cityCam is set once per draw rather than read from S inside the projection,
// so every shape in one frame is projected through the same camera even if the
// control moves mid-render.
var _cityCam = { cos: 1, sin: 0, cx: 0, cy: 0, th: ISO.th, flat: false };

function cityCamera(gridW, gridH) {
  var a = (S.yaw || 0) * Math.PI / 180;
  _cityCam = {
    cos: Math.cos(a), sin: Math.sin(a),
    cx: gridW / 2, cy: gridH / 2,
    // Flat is the plan view: the same map from directly overhead, with the
    // vertical squash removed and the heights dropped. It is the honest
    // counterpart to the model, because an isometric drawing hides whatever
    // stands behind a tower and a plan hides nothing.
    th: S.flat ? ISO.tw : ISO.th,
    flat: !!S.flat,
  };
}

// isoXY projects a grid cell to the canvas: rotate about the grid centre, then
// the standard 2:1 dimetric projection (or a plain overhead one when flat).
//
// The rotation is in grid space rather than applied to the finished drawing,
// which matters: rotating the output would turn the buildings with the city and
// leave them leaning. Rotating the ground and re-projecting keeps every box
// upright and axis-aligned, which is what makes this read as a camera moving
// around a model rather than a picture being spun.
function isoXY(gx, gy) {
  var c = _cityCam;
  var dx = gx - c.cx, dy = gy - c.cy;
  var rx = dx * c.cos - dy * c.sin, ry = dx * c.sin + dy * c.cos;
  if (c.flat) return [rx * ISO.tw, ry * ISO.tw * 0.52];
  return [(rx - ry) * ISO.tw, (rx + ry) * ISO.th];
}

function drawCity(stage, below, d) {
  var groups = groupNamespaces(d.nodes);
  var met = METRICS[S.metric];

  // Districts are laid out on a coarse grid in rank order, each sized to hold
  // its members in a near-square block. A treemap would pack tighter; it would
  // also put a 333-package district next to a 4-package one with no gap, and
  // the gaps are what make these read as city blocks rather than a quilt.
  var placed = [], cursorX = 0, cursorY = 0, rowH = 0, rowMax = 30;
  groups.forEach(function (g) {
    var cols = Math.max(2, Math.ceil(Math.sqrt(g.n * 1.35)));
    var rows = Math.ceil(g.n / cols);
    if (cursorX && cursorX + cols + 2 > rowMax) { cursorX = 0; cursorY += rowH + 2; rowH = 0; }
    g.gx = cursorX; g.gy = cursorY; g.cols = cols; g.rows = rows;
    cursorX += cols + 2;
    rowH = Math.max(rowH, rows);
    rowMax = Math.max(rowMax, cursorX);
    placed.push(g);
  });
  var gridW = rowMax, gridH = cursorY + rowH + 2;

  // The camera is set before anything is projected, because isoXY reads it.
  cityCamera(gridW, gridH);

  var cells = [];
  placed.forEach(function (g) {
    g.nodes.forEach(function (n, i) {
      // Which slot inside the district is arbitrary and stated as arbitrary in
      // the caption. Seeded from the path so it does not move between reloads.
      var gx = g.gx + (i % g.cols), gy = g.gy + Math.floor(i / g.cols);
      cells.push({ n: n, g: g, gx: gx, gy: gy });
    });
  });

  // Buildings are drawn back to front so a nearer one overlaps the one behind
  // it, which is the entire illusion.
  //
  // Depth is the projected y, not gx+gy. Those agree only at yaw 0; at any
  // other angle "behind" is a different direction in grid space, and sorting by
  // the old key turns the city inside out, with far towers painted over near
  // ones. Costs one projection per cell and is the only thing that makes the
  // rotation look like a solid model instead of a pile of stickers.
  cells.forEach(function (c) { c.p = isoXY(c.gx, c.gy); });
  cells.sort(function (a, b) { return a.p[1] - b.p[1]; });

  var maxM = Math.max(1, d.nodes.reduce(function (m, n) { return Math.max(m, met.get(n) || 0); }, 0));
  var maxStorey = 16;

  // Heights are resolved before the viewBox, not during the draw, because the
  // box has to be the bounds of what is actually drawn. Reserving headroom for
  // the tallest possible tower everywhere left a third of the frame empty on
  // every chain where nothing reaches sixteen storeys, and an SVG that scales
  // to fit turns that empty third into a smaller city.
  var pureRises = met.pure;
  var denom = Math.max(1e-9, sc(maxM));
  cells.forEach(function (c) {
    var v = met.get(c.n) || 0;
    var rises = c.n.is_realm || pureRises;
    c.storeys = rises && v > 0 ? Math.max(1, Math.round(sc(v) / denom * maxStorey)) : 1;
    // A plan view has no heights to draw. The storey count is still computed,
    // because the tooltip reports it and the plan still colours by it.
    c.h = _cityCam.flat ? 0 : (rises ? c.storeys * ISO.storey : ISO.storey * 0.7);
  });

  // Bounds from what is actually projected. Under rotation the grid's corners
  // are no longer the extremes, so taking min and max over the four of them
  // plus every cell is the only way the frame stays tight at every angle.
  var pad = 34;
  var minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
  [[0, 0], [gridW, 0], [0, gridH], [gridW, gridH]].forEach(function (q) {
    var p = isoXY(q[0], q[1]);
    minX = Math.min(minX, p[0]); maxX = Math.max(maxX, p[0]);
    minY = Math.min(minY, p[1]); maxY = Math.max(maxY, p[1]);
  });
  cells.forEach(function (c) {
    minX = Math.min(minX, c.p[0] - ISO.tw); maxX = Math.max(maxX, c.p[0] + ISO.tw);
    minY = Math.min(minY, c.p[1] - c.h - ISO.th); maxY = Math.max(maxY, c.p[1] + ISO.th);
  });
  minX -= pad; maxX += pad; minY -= pad; maxY += pad + 18;
  var vbW = maxX - minX, vbH = maxY - minY;

  var svg = svgEl('svg', { viewBox: minX + ' ' + minY + ' ' + vbW + ' ' + vbH,
    preserveAspectRatio: 'xMidYMid meet', style: 'max-height:78vh' });

  // District ground plates, painted first and once per district, so the tiles
  // under a building never show through a gap between two of them.
  var ground = svgEl('g');
  placed.forEach(function (g) {
    // Four projected corners, so a plate turns with the city and squares up
    // in the plan without either case needing its own geometry.
    var a = isoXY(g.gx - 0.6, g.gy - 0.6), b = isoXY(g.gx + g.cols + 0.1, g.gy - 0.6),
        c = isoXY(g.gx + g.cols + 0.1, g.gy + g.rows + 0.1), e = isoXY(g.gx - 0.6, g.gy + g.rows + 0.1);
    var plate = svgEl('polygon', {
      points: [a, b, c, e].map(function (p) { return p.join(','); }).join(' '),
      fill: nsColor(g.ns, 9, 38), stroke: nsColor(g.ns, 20, 34), 'stroke-width': 1 });
    ground.appendChild(plate);

    // The district's name, only where there is room for it. A label smaller
    // than its own text is noise, and 35 of them would bury the city.
    if (g.cols >= 4) {
      var mid = isoXY(g.gx + g.cols / 2 - 0.3, g.gy + g.rows + 0.1);
      var t = svgEl('text', { x: mid[0], y: mid[1] + 14, 'text-anchor': 'middle',
        fill: nsColor(g.ns, 62), 'font-size': 11, 'font-family': 'var(--mono)' });
      t.textContent = g.ns.length > 18 ? g.ns.slice(0, 7) + '…' + g.ns.slice(-4) : g.ns;
      ground.appendChild(t);
    }
  });
  svg.appendChild(ground);

  // Roads: the import graph aggregated to the district level, drawn on the
  // ground between district centres.
  //
  // Per-package lines were the obvious first try and they are unreadable:
  // 1329 import edges over 589 buildings is a grey haze with a city somewhere
  // under it. Rolled up per namespace pair it becomes a few dozen routes, each
  // one a real relationship (this team's code stands on that team's), which is
  // the thing a road is a good metaphor for in the first place.
  //
  // Under the town, so a road passes behind the buildings it serves, the way a
  // road does.
  var roadCount = 0, roadPairs = 0;
  if (S.roads && d.imports.length) {
    var gByNs = {};
    placed.forEach(function (g) { gByNs[g.ns] = g; });
    var flow = {};
    d.imports.forEach(function (e) {
      var a = d.byPath[e.source], b = d.byPath[e.target];
      if (!a || !b) return;
      if (a.namespace === b.namespace) return;   // internal, not a road
      var k = a.namespace + '\u0000' + b.namespace;
      flow[k] = (flow[k] || 0) + 1;
      roadCount++;
    });
    var routes = Object.keys(flow).map(function (k) {
      var pair = k.split('\u0000');
      return { from: gByNs[pair[0]], to: gByNs[pair[1]], w: flow[k] };
    }).filter(function (r) { return r.from && r.to; });
    roadPairs = routes.length;
    var maxW = routes.reduce(function (m, r) { return Math.max(m, r.w); }, 1);

    var roadsG = svgEl('g');
    routes.sort(function (a, b) { return a.w - b.w; }).forEach(function (r) {
      var A = isoXY(r.from.gx + r.from.cols / 2, r.from.gy + r.from.rows / 2);
      var B = isoXY(r.to.gx + r.to.cols / 2, r.to.gy + r.to.rows / 2);
      // Bowed off the straight line, consistently to one side, so two routes
      // between neighbouring districts do not lie on top of each other.
      var mx = (A[0] + B[0]) / 2 + (B[1] - A[1]) * 0.09;
      var my = (A[1] + B[1]) / 2 - (B[0] - A[0]) * 0.045;
      var frac = lg(r.w) / lg(maxW);
      var road = svgEl('path', {
        d: 'M' + A[0] + ',' + A[1] + ' Q' + mx + ',' + my + ' ' + B[0] + ',' + B[1],
        fill: 'none', stroke: nsColor(r.to.ns, 56), 'stroke-width': 1 + frac * 5.5,
        'stroke-linecap': 'round', opacity: 0.22 + frac * 0.38 });
      road.style.cursor = 'default';
      road.addEventListener('mousemove', function (ev) {
        tipShow(ev, [r.from.ns + '  \u2192  ' + r.to.ns,
          ['import edges', window.fmtNum(r.w)],
          ['meaning', 'packages in ' + r.from.ns + ' import ' + r.to.ns]]);
      });
      road.addEventListener('mouseleave', tipHide);
      roadsG.appendChild(road);
    });
    svg.appendChild(roadsG);
  }

  var lit = 0;
  var town = svgEl('g');
  cells.forEach(function (c) {
    var n = c.n;
    var storeys = c.storeys, h = c.h;
    var x = c.p[0], y = c.p[1];
    var tw = ISO.tw * 0.74, th = (_cityCam.flat ? ISO.tw * 0.52 : ISO.th) * 0.74;

    var g = svgEl('g');
    var base = n.is_realm ? 58 : 34;
    // In the plan view the roof carries the metric on its own, because there is
    // no height left to carry it: a tall building is a bright plot. Without
    // this the plan is a flat quilt of namespace colours saying nothing the
    // legend does not already say.
    var liftL = _cityCam.flat ? (storeys / 16) * 26 : 0;
    var top = nsColor(n.namespace, base + 8 + liftL);
    var left = nsColor(n.namespace, base - 16);
    var right = nsColor(n.namespace, base - 28);

    // Roof, then the two visible walls. Three polygons per building, 589
    // buildings: ~1800 nodes, which paints in one frame and stays interactive.
    //
    // The plan draws one axis-aligned rectangle instead. A diamond is the
    // shape a square plot *projects to* when the camera is oblique; keeping it
    // overhead would say the plots are diamonds, which they are not, and the
    // whole point of the plan is that it does not distort what it shows.
    if (_cityCam.flat) {
      g.appendChild(svgEl('rect', { x: x - tw * 0.86, y: y - th * 0.86,
        width: tw * 1.72, height: th * 1.72, rx: 1,
        fill: top, stroke: 'rgba(0,0,0,.35)', 'stroke-width': 0.5 }));
    } else {
      g.appendChild(svgEl('polygon', { points:
        [[x, y - h - th], [x + tw, y - h], [x, y - h + th], [x - tw, y - h]]
          .map(function (q) { return q.join(','); }).join(' '),
        fill: top, stroke: 'rgba(0,0,0,.35)', 'stroke-width': 0.5 }));
    }
    if (!_cityCam.flat) {
      g.appendChild(svgEl('polygon', { points:
        [[x - tw, y - h], [x, y - h + th], [x, y + th], [x - tw, y]]
          .map(function (q) { return q.join(','); }).join(' '),
        fill: left, stroke: 'rgba(0,0,0,.35)', 'stroke-width': 0.5 }));
      g.appendChild(svgEl('polygon', { points:
        [[x + tw, y - h], [x, y - h + th], [x, y + th], [x + tw, y]]
          .map(function (q) { return q.join(','); }).join(' '),
        fill: right, stroke: 'rgba(0,0,0,.35)', 'stroke-width': 0.5 }));
    }

    // Lit windows: called in the window. Count scales with the call count so a
    // busy realm glows rather than merely being on, but any call at all lights
    // at least one, because "somebody used this" is the fact worth seeing.
    if (n.calls > 0) {
      lit++;
      if (_cityCam.flat) {
        // No facade to light from overhead, so the lamp becomes a mark on the
        // plot. Still the same fact in the same colour, which is what keeps the
        // two views readable as one map.
        g.appendChild(svgEl('circle', { cx: x, cy: y, r: 2.1, fill: '#ffd93d', opacity: 0.9 }));
      } else {
        var lamps = Math.min(storeys, Math.max(1, Math.round(lg(n.calls) * 1.6)));
        for (var k = 0; k < lamps; k++) {
          var ly = y - (k + 0.6) * (h / Math.max(1, storeys));
          g.appendChild(svgEl('rect', { x: x - tw * 0.62, y: ly - 2.4, width: 2.6, height: 2.6,
            fill: '#ffd93d', opacity: 0.85 }));
          g.appendChild(svgEl('rect', { x: x + tw * 0.36, y: ly - 2.4 + ISO.th * 0.3, width: 2.6, height: 2.6,
            fill: '#ffd93d', opacity: 0.6 }));
        }
      }
    }

    // Parked: submitted, stored, never enabled. Drawn as scaffolding, because
    // a building that exists and cannot be entered is exactly that, and the
    // chain's liveness probes cannot see it either.
    if (n.parked) {
      if (_cityCam.flat) {
        g.appendChild(svgEl('rect', { x: x - tw * 0.86, y: y - th * 0.86,
          width: tw * 1.72, height: th * 1.72, rx: 1, fill: 'none',
          stroke: 'var(--amber)', 'stroke-width': 1, 'stroke-dasharray': '2 2', opacity: 0.9 }));
      } else {
        g.appendChild(svgEl('polygon', { points:
          [[x, y - h - th], [x + tw, y - h], [x, y + th], [x - tw, y]]
            .map(function (q) { return q.join(','); }).join(' '),
          fill: 'none', stroke: 'var(--amber)', 'stroke-width': 1, 'stroke-dasharray': '2 2', opacity: 0.8 }));
      }
    }

    bindNode(g, n, [['storeys', String(storeys) + ' (' + met.label + ')']]);
    town.appendChild(g);
  });
  svg.appendChild(town);
  mountSVG(stage, svg);

  var realms = d.nodes.filter(function (n) { return n.is_realm; }).length;
  note(below, [
    'One building per deployed package, ', [String(d.nodes.length)], ' of them, grouped into ',
    [String(groups.length)], ' districts by namespace. Storeys are ', [met.label],
    ' on a ', [S.scale], ' scale, with a floor of one so nothing with a zero disappears. ' +
    'Hue is the namespace. ',
    'Lit windows mean the package was called in the last ', [S.window], ': ', [String(lit)],
    ' of ', [String(d.nodes.length)], ' are lit, which is the single most useful thing this drawing says. ',
    'The ', [String(d.nodes.length - realms)], ' pure packages ',
    pureRises
      ? 'rise here like everything else, because ' + met.label + ' is a quantity they genuinely ' +
        'have: being depended on is the only one some of them have at all. '
      : 'are drawn as single-storey sheds, because ' + met.label + ' is not a quantity a pure ' +
        'package has (it holds no state and receives no calls of its own), and a tower built ' +
        'from that number would be the picture lying in the one channel a reader trusts. ' +
        'Switch to “imported” to see them. ',
    'Which plot a building takes inside its district is arbitrary and seeded from its path, so it ' +
    'stays put between reloads; nothing in the position means anything. District area is member ' +
    'count, not importance. ',
    S.yaw ? 'The city is turned ' + S.yaw + '\u00b0 from north; the angle is a camera and means ' +
      'nothing about the data. ' : '',
    _cityCam.flat
      ? 'This is the plan view: straight down, no heights, so nothing hides behind a tower. The ' +
        'metric moves to the brightness of each plot instead. ' : '',
    S.roads && roadPairs
      ? 'The roads are the import graph rolled up to districts: ' + roadPairs +
        ' routes carrying ' + roadCount + ' of the chain\u2019s ' + d.imports.length +
        ' import edges, width by how many. The other ' + (d.imports.length - roadCount) +
        ' are imports inside a single namespace and are not drawn, because a road from a ' +
        'district to itself is not a road. Per-package lines were the first try: 1329 of them ' +
        'is a haze with a city somewhere under it.'
      : 'Import roads are off; turn them on to see which districts stand on which.',
  ]);
  var leg = [
    ['var(--amber)', 'lit window', '· called in the window'],
    ['transparent', 'dashed outline', '· parked in the inert queue'],
    [nsColor(groups[0].ns, 58), groups[0].ns, '· largest district, ' + groups[0].n + ' packages'],
  ];
  if (S.roads && roadPairs) {
    leg.push(['', 'road', '· ' + roadPairs + ' district-to-district import routes, width by count']);
  }
  legend(below, leg);
}

// =============================================================================
// 2. SETTLEMENT  -- the caller graph as villages and the people walking between
// =============================================================================
//
// The one view about people rather than code. /api/graph/callers returns real
// address-to-realm call counts; this draws the realms as villages, the addresses
// as figures standing between the villages they use, and the calls as traffic
// on the road.
//
// A traveller dot moves along each road at a rate set by the call count, which
// is the one animation on this page and the reason it is here: a still picture
// of a caller graph is a hairball, and motion is what separates a road somebody
// walks from a road that merely exists.

function drawSettlement(stage, below, d) {
  if (!d.people.length) {
    stage.appendChild(window.el('div', { className: 'carto-empty' },
      'no caller graph for this chain and window: try a longer window, or a chain with an indexer behind it'));
    return;
  }

  var H = 760;
  // Villages are the realms in the caller graph, placed on a circle ordered by
  // namespace so a deployer's realms end up adjacent and the roads between
  // them stay short. Radius by call count.
  var realmCalls = {}, callerCalls = {};
  d.people.forEach(function (e) {
    realmCalls[e.pkg_path] = (realmCalls[e.pkg_path] || 0) + e.calls;
    callerCalls[e.caller] = (callerCalls[e.caller] || 0) + e.calls;
  });
  var villages = Object.keys(realmCalls).map(function (p) {
    var n = d.byPath[p] || { path: p, namespace: (p.split('/')[2] || '?'), is_realm: true,
      calls: realmCalls[p], unique_callers: 0, importers: 0, imports: 0, storage_bytes: 0, gas_used: 0 };
    return { p: p, n: n, calls: realmCalls[p], ns: n.namespace };
  });
  villages.sort(function (a, b) { return a.ns.localeCompare(b.ns) || b.calls - a.calls; });

  var cx = W / 2, cy = H / 2, R = Math.min(W, H) * 0.36;
  villages.forEach(function (v, i) {
    var a = (i / villages.length) * Math.PI * 2 - Math.PI / 2;
    v.x = cx + Math.cos(a) * R;
    v.y = cy + Math.sin(a) * R;
    v.a = a;
  });
  var vByPath = {};
  villages.forEach(function (v) { vByPath[v.p] = v; });

  // People sit inside the ring, at the weighted centre of the villages they
  // call, pushed toward the middle in proportion to how many different
  // villages that is. Somebody who only ever calls one realm stands at its
  // gate; somebody who calls six stands in the commons.
  var byCaller = {};
  d.people.forEach(function (e) { (byCaller[e.caller] = byCaller[e.caller] || []).push(e); });
  var people = Object.keys(byCaller).map(function (addr) {
    var es = byCaller[addr].filter(function (e) { return vByPath[e.pkg_path]; });
    var tot = es.reduce(function (a, e) { return a + e.calls; }, 0) || 1;
    var x = 0, y = 0;
    es.forEach(function (e) {
      var v = vByPath[e.pkg_path];
      x += v.x * e.calls / tot; y += v.y * e.calls / tot;
    });
    // A single-village caller would land exactly on the village and vanish
    // under it, so it is pulled back toward the gate by a fixed fraction. The
    // tangential spread is what turns fourteen such callers from one dot into
    // a crowd at the gate: deterministic, seeded by the address, so a reader
    // who recognises a figure finds it in the same place next time.
    var pull = es.length === 1 ? 0.80 : 1;
    var px = cx + (x - cx) * pull, py = cy + (y - cy) * pull;
    var ang = Math.atan2(py - cy, px - cx);
    var jit = ((hash32(addr) % 1000) / 1000 - 0.5) * (es.length === 1 ? 26 : 10);
    return { addr: addr, x: px + Math.cos(ang + Math.PI / 2) * jit,
      y: py + Math.sin(ang + Math.PI / 2) * jit,
      calls: callerCalls[addr], reach: es.length, edges: es };
  }).filter(function (p) { return p.edges.length; });

  var maxRoad = Math.max.apply(null, d.people.map(function (e) { return e.calls; }));
  var maxV = Math.max.apply(null, villages.map(function (v) { return v.calls; }));

  var svg = svgEl('svg', { viewBox: '0 0 ' + W + ' ' + H, preserveAspectRatio: 'xMidYMid meet',
    style: 'max-height:78vh' });

  // Roads first, under everything. Curved toward the centre so two roads
  // between the same pair of neighbours do not overlap into one line.
  var roads = svgEl('g');
  var walkers = [];
  d.people.forEach(function (e) {
    var v = vByPath[e.pkg_path]; if (!v) return;
    var p = people.find(function (q) { return q.addr === e.caller; }); if (!p) return;
    // A gentle bow toward the centre, enough to separate two roads between the
    // same pair without turning every road into a spoke through the middle.
    var mx = (p.x + v.x) / 2 + (cx - (p.x + v.x) / 2) * 0.12;
    var my = (p.y + v.y) / 2 + (cy - (p.y + v.y) / 2) * 0.12;
    var dpath = 'M' + p.x + ',' + p.y + ' Q' + mx + ',' + my + ' ' + v.x + ',' + v.y;
    var wgt = 0.4 + lg(e.calls) / lg(maxRoad) * 2.6;
    var road = svgEl('path', { d: dpath, fill: 'none', stroke: nsColor(v.ns, 46),
      'stroke-width': wgt, opacity: 0.3 + 0.35 * (lg(e.calls) / lg(maxRoad)) });
    roads.appendChild(road);
    walkers.push({ el: road, calls: e.calls, ns: v.ns });
  });
  svg.appendChild(roads);

  // Travellers: one dot per road, offset along it, speed by call count. The
  // dots are the traffic, not a decoration; a road with 3546 calls on it moves
  // visibly faster than one with 4.
  var dots = svgEl('g');
  walkers.forEach(function (w) {
    var c = svgEl('circle', { r: 1.9, fill: nsColor(w.ns, 74), opacity: 0.85 });
    dots.appendChild(c);
    w.dot = c;
    w.speed = 0.22 + 1.5 * (lg(w.calls) / lg(maxRoad));
    w.t = (hash32(w.ns + w.calls) % 1000) / 1000;
    // Deliberately NOT w.el.getTotalLength() here. The svg is still detached
    // at this point and a detached path measures 0, which made every traveller
    // fail the `if (!w.len)` guard below and stand still for good: 744 dots
    // that never received a cx at all. Lengths are taken in the first frame,
    // after the stage has the svg.
  });
  svg.appendChild(dots);

  // People, then villages on top: a village is the thing you click, so nothing
  // may sit over it.
  var pg = svgEl('g');
  people.forEach(function (p) {
    var r = 1.6 + lg(p.calls) * 0.9;
    var c = svgEl('circle', { cx: p.x, cy: p.y, r: r, fill: 'var(--fg)', opacity: 0.55,
      stroke: 'var(--bg)', 'stroke-width': 0.6 });
    c.style.cursor = 'pointer';
    c.addEventListener('mousemove', function (ev) {
      tipShow(ev, [p.addr,
        ['calls (' + S.window + ')', window.fmtNum(p.calls)],
        ['realms used', String(p.reach)],
        ['top', p.edges.slice().sort(function (a, b) { return b.calls - a.calls; })[0].pkg_path.replace(/^gno\.land\//, '')]]);
    });
    c.addEventListener('mouseleave', tipHide);
    c.addEventListener('click', function () {
      tipHide();
      window.navigate('/address/' + p.addr + (window.netSuffix ? window.netSuffix() : ''));
    });
    pg.appendChild(c);
  });
  svg.appendChild(pg);

  var vg = svgEl('g');
  villages.forEach(function (v) {
    var r = 3.5 + lg(v.calls) / lg(maxV) * 13;
    var g = svgEl('g');
    g.appendChild(svgEl('circle', { cx: v.x, cy: v.y, r: r, fill: nsColor(v.ns, 30, 45),
      stroke: nsColor(v.ns, 66), 'stroke-width': 1.4 }));
    // A roof mark, so a village reads as a settlement and not as one more dot
    // in a scatter plot.
    g.appendChild(svgEl('path', { d: 'M' + (v.x - r * 0.55) + ',' + (v.y + r * 0.1) +
      ' L' + v.x + ',' + (v.y - r * 0.6) + ' L' + (v.x + r * 0.55) + ',' + (v.y + r * 0.1),
      fill: 'none', stroke: nsColor(v.ns, 80), 'stroke-width': 1.2, 'stroke-linejoin': 'round' }));

    // Labels read outward along the radius, flipped on the left half so none of
    // them is upside down. Horizontal text on a ring this dense collides on
    // every chain; radial text cannot, because each label points away from its
    // own village along a line no neighbour shares.
    var deg = v.a * 180 / Math.PI;
    var flip = Math.cos(v.a) < 0;
    var lx = v.x + Math.cos(v.a) * (r + 6), ly = v.y + Math.sin(v.a) * (r + 6);
    var lab = svgEl('text', {
      x: lx, y: ly, 'text-anchor': flip ? 'end' : 'start', 'dominant-baseline': 'middle',
      transform: 'rotate(' + (flip ? deg + 180 : deg) + ' ' + lx + ' ' + ly + ')',
      fill: nsColor(v.ns, 68), 'font-size': 9.5, 'font-family': 'var(--mono)' });
    lab.textContent = v.n.name || v.p.split('/').pop();
    g.appendChild(lab);

    bindNode(g, v.n, [['calls on these roads', window.fmtNum(v.calls)]]);
    vg.appendChild(g);
  });
  svg.appendChild(vg);
  mountSVG(stage, svg);

  // The walk. One rAF for every traveller: 585 dots at 60 Hz is a few hundred
  // microseconds of setAttribute per frame, and the loop is cancelled the
  // moment the view changes, so nothing keeps ticking behind another drawing.
  // Measure and place once, synchronously, now that the svg is in the document
  // (a detached path measures 0, which is what made the first draft's dots
  // never receive a cx at all). Doing it here rather than in the first frame
  // also fixes the background tab: requestAnimationFrame does not fire while
  // document.hidden, so a reader who opens this in a new tab would otherwise
  // find a map with no traffic on it until they looked at it.
  function place(w) {
    if (!w.len) return;
    var pt = w.el.getPointAtLength(w.t * w.len);
    w.dot.setAttribute('cx', pt.x); w.dot.setAttribute('cy', pt.y);
  }
  walkers.forEach(function (w) { w.len = w.el.getTotalLength(); place(w); });

  var last = 0;
  function step(ts) {
    var dt = last ? Math.min(60, ts - last) : 16; last = ts;
    for (var i = 0; i < walkers.length; i++) {
      var w = walkers[i];
      if (!w.len) continue;
      w.t += (w.speed * dt) / (w.len * 1.4);
      if (w.t > 1) w.t -= 1;
      place(w);
    }
    S.anim = requestAnimationFrame(step);
  }
  S.anim = requestAnimationFrame(step);

  note(below, [
    'The caller graph drawn as a place: ', [String(villages.length)], ' villages (realms), ',
    [String(people.length)], ' figures (addresses), ', [String(walkers.length)],
    ' roads between them. A village is sized by the calls it received in the last ', [S.window],
    '; a figure stands at the weighted centre of the realms it calls, so somebody who uses one ' +
    'realm waits at its gate and somebody who uses six stands in the commons. Road width and the ' +
    'speed of the traveller on it are both that pair’s call count, which is why a busy road ' +
    'looks busy rather than merely thick. ',
    'This layer is the API’s top ', ['80'], ' callers and their realms, not the whole chain: ' +
    'the full address-by-realm matrix is tens of thousands of pairs and would draw as a solid disc. ' +
    'Everything else on this page draws all ', [String(d.nodes.length)], ' packages.',
  ]);
  legend(below, [
    ['var(--fg)', 'figure', '· an address, sized by calls'],
    [nsColor(villages[0].ns, 60), 'village', '· a realm, sized by calls received'],
    ['', 'moving dot', '· traffic on that road, speed by call count'],
  ]);
}

// =============================================================================
// 3. ORBITS  -- one solar system per namespace, orbit radius by deploy date
// =============================================================================
//
// The view about time. The city says how big and how awake; nothing in it says
// when. Here each namespace is a star and each of its packages is a planet on
// an orbit whose radius is the deploy date: the innermost planet is the oldest
// thing that namespace put on chain, the outermost is the newest, so a system's
// spread is literally how long its owner has been building.

function drawOrbits(stage, below, d) {
  var groups = groupNamespaces(d.nodes).slice(0, 24);
  var shown = groups.reduce(function (a, g) { return a + g.n; }, 0);
  var met = METRICS[S.metric];

  var times = d.nodes.map(function (n) { return n.deployed_at ? Date.parse(n.deployed_at) : 0; })
    .filter(function (t) { return t > 0; });
  var t0 = Math.min.apply(null, times), t1 = Math.max.apply(null, times);
  var span = Math.max(1, t1 - t0);

  // Systems on a grid, biggest first, each cell sized to the system it holds.
  // Not a force layout: a reader comparing two systems needs them on a stable
  // grid, and a settled force layout puts the second-largest somewhere
  // different on every reload.
  var cols = Math.min(5, Math.max(3, Math.round(Math.sqrt(groups.length))));
  var cellW = W / cols, rows = Math.ceil(groups.length / cols);
  var cellH = 268, H = rows * cellH + 20;

  var svg = svgEl('svg', { viewBox: '0 0 ' + W + ' ' + H, preserveAspectRatio: 'xMidYMid meet',
    style: 'max-height:80vh' });

  var maxM = Math.max(1, d.nodes.reduce(function (m, n) { return Math.max(m, met.get(n) || 0); }, 0));

  groups.forEach(function (g, gi) {
    var cx = (gi % cols) * cellW + cellW / 2, cy = Math.floor(gi / cols) * cellH + cellH / 2;
    var rMax = Math.min(cellW, cellH) * 0.44;
    var sys = svgEl('g');

    // The star: sized by how many packages the namespace holds, coloured by
    // its hue, with a halo whose opacity is the share of them that were called
    // in the window. A dead namespace is a cold star.
    var live = g.live / Math.max(1, g.n);
    var starR = 4 + lg(g.n) * 5;
    sys.appendChild(svgEl('circle', { cx: cx, cy: cy, r: starR + 9, fill: nsColor(g.ns, 55),
      opacity: 0.04 + live * 0.22 }));
    sys.appendChild(svgEl('circle', { cx: cx, cy: cy, r: starR, fill: nsColor(g.ns, 62),
      stroke: nsColor(g.ns, 78), 'stroke-width': 1 }));

    var rng = rngFrom(g.ns);
    // One ring per deploy day, not per pixel band. This chain has 17 distinct
    // deploy days across its whole life, so a pixel quantisation collapsed
    // most systems into one smeared ring and threw the channel away. A ring
    // per day makes a deploy session read as a deploy session, which is the
    // unit anyone actually works in, and the radius still comes from the
    // global date scale so two systems remain comparable.
    var orbits = {};
    g.nodes.forEach(function (n) {
      var day = (n.deployed_at || '').slice(0, 10) || 'unknown';
      (orbits[day] = orbits[day] || []).push(n);
    });
    var days = Object.keys(orbits).sort();

    days.forEach(function (day, di) {
      var ring = orbits[day];
      var t = day === 'unknown' ? t0 : Date.parse(day + 'T00:00:00Z');
      var frac = span > 864e5 ? (t - t0) / span : di / Math.max(1, days.length - 1);
      var r = starR + 15 + frac * (rMax - starR - 15);
      sys.appendChild(svgEl('ellipse', { cx: cx, cy: cy, rx: r, ry: r * S.tilt, fill: 'none',
        stroke: nsColor(g.ns, 30, 30), 'stroke-width': 0.6, opacity: 0.55 }));
      var a0 = rng() * Math.PI * 2;
      ring.forEach(function (n, i) {
        var a = a0 + (i / ring.length) * Math.PI * 2;
        var x = cx + Math.cos(a) * r, y = cy + Math.sin(a) * r * S.tilt;
        var v = met.get(n) || 0;
        var pr = 1.1 + (v > 0 ? sc(v) / Math.max(1e-9, sc(maxM)) * 5.2 : 0);
        var pg = svgEl('g');
        pg.appendChild(svgEl('circle', { cx: x, cy: y, r: pr,
          fill: n.is_realm ? nsColor(n.namespace, n.calls > 0 ? 72 : 44)
                           : nsColor(n.namespace, 30, 26),
          stroke: n.calls > 0 ? 'var(--amber)' : 'none', 'stroke-width': n.calls > 0 ? 0.7 : 0 }));
        // Moons: one ring per package that other code imports, because on this
        // chain being depended on is the quantity a pure package has and a
        // size channel fed by calls or gas cannot show it at all.
        if (n.importers > 0) {
          sys.appendChild(svgEl('circle', { cx: x, cy: y, r: pr + 2 + Math.min(4, lg(n.importers) * 2),
            fill: 'none', stroke: nsColor(n.namespace, 60), 'stroke-width': 0.5, opacity: 0.6 }));
        }
        bindNode(pg, n, [['orbit', day + ' · ' + ring.length + ' deployed that day']]);
        sys.appendChild(pg);
      });
    });

    var lab = svgEl('text', { x: cx, y: cy + cellH / 2 - 14, 'text-anchor': 'middle',
      fill: nsColor(g.ns, 66), 'font-size': 11, 'font-family': 'var(--mono)' });
    lab.textContent = (g.ns.length > 16 ? g.ns.slice(0, 6) + '…' + g.ns.slice(-4) : g.ns) +
      ' · ' + g.n;
    sys.appendChild(lab);
    svg.appendChild(sys);
  });

  mountSVG(stage, svg);

  var all = groupNamespaces(d.nodes).length;
  note(below, [
    'One solar system per namespace, the ', [String(groups.length)], ' largest of ', [String(all)],
    ', holding ', [String(shown)], ' of ', [String(d.nodes.length)], ' packages. ',
    'Orbit radius is the deploy date, oldest innermost, on one scale shared by every system, ' +
    'running from ', [new Date(t0).toISOString().slice(0, 10)], ' to ',
    [new Date(t1).toISOString().slice(0, 10)], ', ',
    [String(Math.round(span / 864e5)) + ' days'], ', which is the whole life of this chain and why ' +
    'the systems are compressed rather than sprawling. One ring per deploy day, so everything ' +
    'pushed in one session shares an orbit and a batch deploy reads as a batch. Planet size is ',
    [met.label], ' on a ', [S.scale],
    ' scale; a gold rim means it was called in the last ', [S.window],
    '; a faint circle around a planet means other code imports it, sized by how much. ' +
    'Angle around the orbit carries nothing and is seeded from the namespace name. ' +
    'The star is the namespace itself, sized by package count, its halo brightening with the ' +
    'share of its packages that saw a call.',
  ]);
  legend(below, [
    ['var(--amber)', 'gold rim', '· called in the window'],
    ['', 'faint ring', '· imported by other code'],
    ['', 'inner orbit', '· deployed earlier'],
  ]);
}

// =============================================================================
// 4. METRO  -- the import graph as a transit map
// =============================================================================
//
// The view about dependency. One line per namespace, its stations the packages
// that namespace imports, ordered by how widely each is depended on across the
// whole chain. Where two lines stop at the same station, that package is shared
// infrastructure, and the interchange marks it.
//
// This is the picture the city cannot draw. p/nt/ufmt has 168 dependents, zero
// calls and 23 KB of storage: it is a shed on the skyline and the busiest
// interchange on this map, and both are true.

function drawMetro(stage, below, d) {
  if (!d.imports.length) {
    stage.appendChild(window.el('div', { className: 'carto-empty' },
      'no import edges indexed for this chain, nothing to route'));
    return;
  }

  // Stations are the most-imported packages: the ones a line can usefully stop
  // at. A station per package would be 589 stops and no map.
  var STATIONS = 26;
  var stationNodes = d.nodes.slice()
    .filter(function (n) { return n.importers > 0; })
    .sort(function (a, b) { return b.importers - a.importers || a.path.localeCompare(b.path); })
    .slice(0, STATIONS);
  if (!stationNodes.length) {
    stage.appendChild(window.el('div', { className: 'carto-empty' },
      'nothing on this chain imports anything else yet'));
    return;
  }
  var stationSet = {};
  stationNodes.forEach(function (n, i) { stationSet[n.path] = i; });

  // Each namespace rides one line, stopping at every station its packages
  // import. Namespaces that stop nowhere get no line.
  var lines = {};
  d.imports.forEach(function (e) {
    var si = stationSet[e.target]; if (si === undefined) return;
    var src = d.byPath[e.source]; if (!src) return;
    var ns = src.namespace || '?';
    var L = lines[ns] = lines[ns] || { ns: ns, stops: {}, riders: {} };
    L.stops[si] = (L.stops[si] || 0) + 1;
    L.riders[e.source] = true;
  });
  var lineList = Object.keys(lines).map(function (k) { return lines[k]; });
  lineList.forEach(function (L) {
    L.order = Object.keys(L.stops).map(Number).sort(function (a, b) { return a - b; });
    L.n = Object.keys(L.riders).length;
  });
  lineList = lineList.filter(function (L) { return L.order.length >= 2; })
    .sort(function (a, b) { return b.order.length - a.order.length || a.ns.localeCompare(b.ns); })
    .slice(0, 14);
  if (!lineList.length) {
    stage.appendChild(window.el('div', { className: 'carto-empty' },
      'no namespace imports two or more of the top packages, nothing to draw as a line'));
    return;
  }

  // Layout. Stations sit on one spine at the bottom, ordered by dependents,
  // most-depended on the left; every line runs above it. The first draft put
  // lines on both sides of the spine and the ones below collided with the
  // station names, which is the one piece of text on this drawing a reader
  // has to be able to follow.
  //
  // The line name gets a reserved gutter on the left rather than being hung
  // off the first stop: most namespaces import the chain's most-depended
  // package, so most lines start at station 0 and every one of those labels
  // ran off the canvas.
  var padL = 148, padR = 44, topPad = 26, trackGap = 24;
  var spineY = topPad + lineList.length * trackGap + 26;
  var H = spineY + 150;
  var stepX = (W - padL - padR) / Math.max(1, stationNodes.length - 1);
  var sx = function (i) { return padL + i * stepX; };

  var svg = svgEl('svg', { viewBox: '0 0 ' + W + ' ' + H, preserveAspectRatio: 'xMidYMid meet',
    style: 'max-height:80vh' });

  var linesG = svgEl('g');
  lineList.forEach(function (L, li) {
    var y = topPad + li * trackGap;
    L.y = y;
    var col = nsColor(L.ns, 58);
    var first = sx(L.order[0]), last = sx(L.order[L.order.length - 1]);

    // The line itself: one stroke from its first stop to its last. A transit
    // line is a single continuous thing and drawing it as one path is what
    // separates this from a scatter of ticks on a rule.
    linesG.appendChild(svgEl('line', { x1: first, y1: y, x2: last, y2: y,
      stroke: col, 'stroke-width': 3.4, 'stroke-linecap': 'round', opacity: 0.9 }));

    // The drop to the spine at each station this line serves. Faint on
    // purpose: fourteen lines times a dozen stops is 150 verticals, and at
    // full strength they read as a grid rather than as connections.
    L.order.forEach(function (si) {
      linesG.appendChild(svgEl('line', { x1: sx(si), y1: y, x2: sx(si), y2: spineY,
        stroke: col, 'stroke-width': 0.8, opacity: 0.16 }));
      linesG.appendChild(svgEl('circle', { cx: sx(si), cy: y, r: 3.1, fill: 'var(--carto-sky)',
        stroke: col, 'stroke-width': 1.8 }));
    });

    var lab = svgEl('text', { x: padL - 14, y: y + 3.6, 'text-anchor': 'end',
      fill: col, 'font-size': 10.5, 'font-family': 'var(--mono)' });
    lab.textContent = (L.ns.length > 18 ? L.ns.slice(0, 7) + '…' + L.ns.slice(-5) : L.ns) +
      '  (' + L.order.length + ')';
    lab.addEventListener('mousemove', function (ev) {
      tipShow(ev, ['namespace ' + L.ns,
        ['packages that import', String(L.n)],
        ['stations on this line', String(L.order.length)]]);
    });
    lab.addEventListener('mouseleave', tipHide);
    linesG.appendChild(lab);
  });
  svg.appendChild(linesG);

  // The spine and its stations, on top of every line.
  svg.appendChild(svgEl('line', { x1: padL - 18, y1: spineY, x2: W - padR + 18, y2: spineY,
    stroke: 'var(--border)', 'stroke-width': 2 }));

  var stG = svgEl('g');
  stationNodes.forEach(function (n, i) {
    var x = sx(i);
    var served = lineList.filter(function (L) { return L.order.indexOf(i) >= 0; }).length;
    var g = svgEl('g');
    // An interchange (two or more lines) gets the hollow double ring the idiom
    // reserves for one; a single-line stop gets a plain tick.
    var r = 3.4 + Math.min(5, lg(n.importers) * 2.2);
    if (served >= 2) {
      g.appendChild(svgEl('circle', { cx: x, cy: spineY, r: r + 2.6, fill: 'var(--carto-sky)',
        stroke: 'var(--fg)', 'stroke-width': 1.6 }));
    }
    g.appendChild(svgEl('circle', { cx: x, cy: spineY, r: r,
      fill: n.calls > 0 ? 'var(--amber)' : 'var(--fg2)',
      stroke: 'var(--carto-sky)', 'stroke-width': 1 }));

    var ty = spineY + r + 10;
    var t = svgEl('text', { x: x, y: ty, fill: 'var(--fg2)', 'font-size': 9.5,
      'font-family': 'var(--mono)', 'text-anchor': 'start',
      transform: 'rotate(55 ' + x + ' ' + ty + ')' });
    t.textContent = n.name + ' · ' + n.importers;
    g.appendChild(t);

    bindNode(g, n, [['lines stopping here', String(served)]]);
    stG.appendChild(g);
  });
  svg.appendChild(stG);
  mountSVG(stage, svg);

  var interchanges = stationNodes.filter(function (n, i) {
    return lineList.filter(function (L) { return L.order.indexOf(i) >= 0; }).length >= 2;
  }).length;
  var totalImporting = Object.keys(lines).length;
  note(below, [
    'The import graph in transit-map form. Stations along the spine are the ', [String(stationNodes.length)],
    ' most-imported packages on this chain, ordered left to right by how many packages depend on ' +
    'them, with the count printed under each name. Each coloured line is one namespace, stopping ' +
    'at every station its packages import: ', [String(lineList.length)], ' lines drawn of ',
    [String(totalImporting)], ' namespaces that import anything, and ', [String(d.imports.length)],
    ' import edges behind them. ', [String(interchanges)],
    ' stations are interchanges, served by two or more namespaces, and those are this chain’s ' +
    'shared infrastructure: the packages a break would take several unrelated teams down with it. ' +
    'Vertical order of the lines carries nothing; it is rank, most stops at the top, and the ' +
    'number after each line name is how many of these stations it stops at. A gold station was ' +
    'itself called in the last ', [S.window], '; most are grey, because ' +
    'a pure package is imported rather than called and that is the point of this drawing.',
  ]);
  legend(below, [
    ['var(--fg)', 'double ring', '· interchange, two or more namespaces'],
    ['var(--amber)', 'gold', '· the package was itself called'],
    ['var(--fg2)', 'grey', '· imported only, never called directly'],
  ]);
}

// =============================================================================
// 5. RELIEF  -- a contour map of density, owner-blind
// =============================================================================
//
// Every other view here groups by namespace, and on mainnet one namespace holds
// 333 of 589 packages, so every other view is partly a picture of that. This one
// deliberately throws ownership away.
//
// Packages are placed by what they are near in the import graph (a few rounds
// of cheap attraction along import edges, starting from a fixed spiral), then a
// scalar field is accumulated from the chosen metric and drawn as terrain.
// Reading it: broad high ground is a region of the chain that is both dense and
// busy, an isolated peak is one package carrying its neighbourhood alone.

function drawRelief(stage, below, d) {
  var met = METRICS[S.metric];
  var nodes = d.nodes;

  // Layout: a deterministic spiral seed, then a small fixed number of rounds of
  // attraction along import edges *and* repulsion between near neighbours.
  //
  // The first draft had attraction only. It is worth saying why that failed,
  // because it looked plausible until it was drawn: everything that imports a
  // common hub converges onto the hub, packages that import nothing stay out
  // on the seed spiral, and renormalising min-to-max then stretches the few
  // outliers across the canvas and squeezes the other 550 into one blob. The
  // result was an ocean with a single island in it, which said nothing about
  // the chain and everything about the layout.
  //
  // Repulsion is what makes this a map. It is a real force layout, kept
  // deliberately small: a fixed round count and a bucketed neighbour lookup,
  // so it is milliseconds rather than a simulation that has to settle, and it
  // is deterministic, so the same chain draws the same terrain every time.
  //
  // Cached per chain and window, because the layout does not depend on the
  // metric: switching "size by" only changes the elevation poured over this
  // terrain, and recomputing 90 rounds to redraw the same coastline made every
  // metric click a 95 ms stall for nothing.
  if (S.reliefFor === S.dataKey && S.reliefPos) {
    return drawReliefField(stage, below, d, S.reliefPos, met);
  }

  var pos = {}, i;
  nodes.forEach(function (n, k) {
    var a = k * 2.399963, r = Math.sqrt((k + 0.5) / nodes.length);
    pos[n.path] = { x: 0.5 + Math.cos(a) * r * 0.46, y: 0.5 + Math.sin(a) * r * 0.46 };
  });
  var links = d.imports.filter(function (e) { return pos[e.source] && pos[e.target]; });

  var ROUNDS = 90, REP_R = 0.045, CELL = REP_R;
  var pts = nodes.map(function (n) { return pos[n.path]; });
  for (var round = 0; round < ROUNDS; round++) {
    var t = 1 - round / ROUNDS;
    var ka = 0.035 * t, kr = 0.020 * t;

    for (i = 0; i < links.length; i++) {
      var a2 = pos[links[i].source], b2 = pos[links[i].target];
      var dx = (b2.x - a2.x) * ka, dy = (b2.y - a2.y) * ka;
      a2.x += dx; a2.y += dy; b2.x -= dx * 0.4; b2.y -= dy * 0.4;
    }

    // Repulsion over a uniform grid: only pairs inside one cell or its eight
    // neighbours are considered, which turns an O(n^2) pass into a linear one
    // and is exact enough at this radius.
    var buckets = {};
    for (i = 0; i < pts.length; i++) {
      var key = Math.floor(pts[i].x / CELL) + ':' + Math.floor(pts[i].y / CELL);
      (buckets[key] = buckets[key] || []).push(pts[i]);
    }
    for (i = 0; i < pts.length; i++) {
      var P = pts[i], gx0 = Math.floor(P.x / CELL), gy0 = Math.floor(P.y / CELL);
      for (var ox = -1; ox <= 1; ox++) for (var oy = -1; oy <= 1; oy++) {
        var bk = buckets[(gx0 + ox) + ':' + (gy0 + oy)];
        if (!bk) continue;
        for (var j = 0; j < bk.length; j++) {
          var Q = bk[j]; if (Q === P) continue;
          var rx = P.x - Q.x, ry = P.y - Q.y;
          var dd = Math.sqrt(rx * rx + ry * ry);
          if (dd >= REP_R) continue;
          // Two packages at exactly the same point (the seed spiral cannot
          // produce it, but the attraction above can) get a deterministic
          // nudge rather than a division by zero.
          if (dd < 1e-6) { rx = ((hash32(String(i + j)) % 100) - 50) / 5000; ry = 1e-4; dd = 1e-4; }
          var push = kr * (1 - dd / REP_R) / dd;
          P.x += rx * push; P.y += ry * push;
        }
      }
    }
  }

  // Renormalise on percentiles rather than min and max: one package flung to a
  // corner should not shrink the other 588 into the middle.
  function pct(arr, q) {
    var a = arr.slice().sort(function (x, y) { return x - y; });
    return a[Math.min(a.length - 1, Math.max(0, Math.round(q * (a.length - 1))))];
  }
  var xs = pts.map(function (p) { return p.x; }), ys = pts.map(function (p) { return p.y; });
  var x0 = pct(xs, 0.01), x1 = pct(xs, 0.99), y0 = pct(ys, 0.01), y1 = pct(ys, 0.99);
  pts.forEach(function (p) {
    p.x = Math.max(0.01, Math.min(0.99, 0.045 + 0.91 * (p.x - x0) / Math.max(1e-6, x1 - x0)));
    p.y = Math.max(0.01, Math.min(0.99, 0.045 + 0.91 * (p.y - y0) / Math.max(1e-6, y1 - y0)));
  });

  S.reliefPos = pos; S.reliefFor = S.dataKey;
  return drawReliefField(stage, below, d, pos, met);
}

// The part of the relief that does depend on the metric: pour elevation over a
// layout that is already settled, band it, contour it, label it. Split out so a
// metric switch redraws the terrain without relaying it out.
function drawReliefField(stage, below, d, pos, met) {
  var nodes = d.nodes;
  var GW = 300, GH = 170;              // samples across the visible window
  var Hpx = Math.round(W * GH / GW);
  var i;

  var maxM = Math.max(1, nodes.reduce(function (m, n) { return Math.max(m, met.get(n) || 0); }, 0));
  var denom = Math.max(1e-9, sc(maxM));

  // The window being looked at, in the unit square the layout lives in. Zoom
  // narrows it; pan slides it.
  //
  // This is why the relief zooms differently from the other four. The field is
  // a continuous function of the layout, not an image: a grid is only where it
  // was sampled. So zooming re-samples the same function over a smaller window
  // at the same 300x170 and genuinely resolves more terrain, instead of
  // magnifying pixels into squares. Two packages that merge into one hill at
  // full extent separate into two when you zoom into them, which is a true
  // statement about the data and not an artefact of the renderer.
  var cam = S.cam.relief || { cx: 0.5, cy: 0.5, k: 1 };
  S.cam.relief = cam;

  var dpr = Math.min(2, window.devicePixelRatio || 1);
  var cv = document.createElement('canvas');
  cv.width = Math.round(W * dpr); cv.height = Math.round(Hpx * dpr);
  cv.style.width = '100%'; cv.style.height = 'auto'; cv.style.maxHeight = '78vh';
  cv.style.cursor = 'crosshair';
  var ctx = cv.getContext('2d');
  ctx.scale(dpr, dpr);

  // Terrain ramp: sea, shore, plain, upland, peak. Discrete bands rather than a
  // smooth gradient, because bands are what make a contour map readable at a
  // glance and a smooth one just looks like a blur.
  var BANDS = [
    [0.00, [7, 7, 11]], [0.06, [12, 20, 32]], [0.14, [16, 32, 44]],
    [0.26, [19, 51, 47]], [0.40, [27, 74, 51]], [0.55, [47, 99, 56]],
    [0.70, [106, 114, 56]], [0.84, [150, 112, 47]], [0.94, [201, 138, 58]],
  ];
  function band(v) {
    for (var b = BANDS.length - 1; b >= 0; b--) if (v >= BANDS[b][0]) return BANDS[b][1];
    return BANDS[0][1];
  }

  var labelled = [], lastMaxF = 1;

  // win returns the visible window in layout space, clamped so the camera can
  // never leave the map entirely.
  function win() {
    var half = 0.5 / cam.k;
    var cx = Math.min(1 - half, Math.max(half, cam.cx));
    var cy = Math.min(1 - half, Math.max(half, cam.cy));
    if (half >= 0.5) { cx = 0.5; cy = 0.5; }
    cam.cx = cx; cam.cy = cy;
    return { x0: cx - half, y0: cy - half, w: half * 2 };
  }

  function paint() {
    var v = win();
    // Sample the field over the window. Radii are in window units, so a hill
    // keeps its size on screen as you zoom and simply gains detail.
    var field = new Float32Array(GW * GH);
    // The window is square in layout space and the grid is not, so y is
    // compressed on screen by GH/GW. That is the same distortion the first
    // version had, and it is harmless here because the caption says absolute
    // position and direction carry nothing: what matters is that it stays the
    // same at every zoom, which it does, because both axes divide by the same
    // window width.
    var sxu = GW / v.w, syu = GH / v.w;
    nodes.forEach(function (n) {
      var amp = 0.18 + 0.82 * (sc(met.get(n) || 0) / denom);
      var P = pos[n.path];
      var cxp = (P.x - v.x0) * sxu, cyp = (P.y - v.y0) * syu;
      var rad = (9 + amp * 13) * cam.k, r2 = rad * rad;
      if (cxp < -rad || cxp > GW + rad || cyp < -rad || cyp > GH + rad) return;
      var x0i = Math.max(0, Math.floor(cxp - rad)), x1i = Math.min(GW - 1, Math.ceil(cxp + rad));
      var y0i = Math.max(0, Math.floor(cyp - rad)), y1i = Math.min(GH - 1, Math.ceil(cyp + rad));
      for (var yy = y0i; yy <= y1i; yy++) {
        for (var xx = x0i; xx <= x1i; xx++) {
          var ddx = xx - cxp, ddy = yy - cyp, dd = ddx * ddx + ddy * ddy;
          if (dd > r2) continue;
          field[yy * GW + xx] += amp * Math.exp(-dd / (r2 * 0.34));
        }
      }
    });

    // Normalised against the whole map's peak, not the window's.
    //
    // Per-window normalisation was the first version and it lies: zooming into
    // a quiet corner repainted it as a mountain range, because the brightest
    // thing in frame is always full scale. A colour has to mean the same
    // elevation at every zoom or the ramp is decoration.
    var maxF = 0;
    for (i = 0; i < field.length; i++) if (field[i] > maxF) maxF = field[i];
    if (cam.k === 1 || !lastMaxF) lastMaxF = maxF || 1;
    var norm = Math.max(lastMaxF, 1e-9);

    // Hillshade: the Lambertian term from the field's own gradient, which is
    // what turns a set of flat bands into something a reader sees the shape of.
    // Cheap (two differences per cell) and it costs no channel: it modulates
    // brightness within a band rather than changing which band a cell is in.
    var sunA = (S.sun || 315) * Math.PI / 180;
    var lx = Math.sin(sunA), ly = -Math.cos(sunA);

    var img = ctx.createImageData(GW, GH);
    var px = img.data;
    for (var yy2 = 0; yy2 < GH; yy2++) {
      for (var xx2 = 0; xx2 < GW; xx2++) {
        var idx = yy2 * GW + xx2;
        var h = field[idx] / norm;
        var c = band(h);
        var hl = field[yy2 * GW + Math.max(0, xx2 - 1)] / norm;
        var hr = field[yy2 * GW + Math.min(GW - 1, xx2 + 1)] / norm;
        var hu = field[Math.max(0, yy2 - 1) * GW + xx2] / norm;
        var hd = field[Math.min(GH - 1, yy2 + 1) * GW + xx2] / norm;
        var gx = (hl - hr) * 26, gy = (hu - hd) * 26;
        var shade = 1 + (gx * lx + gy * ly) * 0.55;
        shade = Math.max(0.45, Math.min(1.7, shade));
        // A contour where this cell crosses a band edge below or to the left.
        var cl = band(hl), cu = band(hu);
        var edge = (cl !== c || cu !== c) ? 0.56 : 1;
        var o = idx * 4;
        px[o]     = Math.min(255, c[0] * shade * edge);
        px[o + 1] = Math.min(255, c[1] * shade * edge);
        px[o + 2] = Math.min(255, c[2] * shade * edge);
        px[o + 3] = 255;
      }
    }

    // The field is painted through an offscreen canvas because putImageData
    // ignores the context transform, so it cannot be scaled up to the visible
    // size on its own.
    var off = document.createElement('canvas');
    off.width = GW; off.height = GH;
    off.getContext('2d').putImageData(img, 0, 0);
    ctx.imageSmoothingEnabled = true;
    ctx.clearRect(0, 0, W, Hpx);
    ctx.drawImage(off, 0, 0, GW, GH, 0, 0, W, Hpx);

    // Peak labels: the highest-metric packages inside the window, so the
    // terrain has place names. Without them this is a pretty texture that
    // tells a reader nothing they can act on. Recomputed per window, so
    // zooming in names the things that were too crowded to name before.
    var scrX = function (p) { return (p.x - v.x0) / v.w * W; };
    var scrY = function (p) { return (p.y - v.y0) / v.w * Hpx; };
    var top = nodes.filter(function (n) {
      var P = pos[n.path];
      return (met.get(n) || 0) > 0 &&
        P.x >= v.x0 && P.x <= v.x0 + v.w && P.y >= v.y0 && P.y <= v.y0 + v.w;
    }).sort(function (a, b) { return (met.get(b) || 0) - (met.get(a) || 0); });

    labelled = [];
    var MIND = 74;
    for (i = 0; i < top.length && labelled.length < 14; i++) {
      var P2 = pos[top[i].path], ax = scrX(P2), ay = scrY(P2);
      var clash = labelled.some(function (q) {
        var qx = scrX(pos[q.path]) - ax, qy = scrY(pos[q.path]) - ay;
        return qx * qx + qy * qy < MIND * MIND;
      });
      if (!clash) labelled.push(top[i]);
    }

    ctx.font = '11px ui-monospace, monospace';
    ctx.textAlign = 'center';
    labelled.forEach(function (n) {
      var P3 = pos[n.path], ax = scrX(P3), ay = scrY(P3);
      ctx.fillStyle = 'rgba(0,0,0,.65)';
      ctx.beginPath(); ctx.arc(ax, ay, 3.2, 0, 6.284); ctx.fill();
      ctx.fillStyle = n.calls > 0 ? '#ffd93d' : '#e0e0e0';
      ctx.beginPath(); ctx.arc(ax, ay, 2, 0, 6.284); ctx.fill();
      var txt = n.name;
      ctx.fillStyle = 'rgba(0,0,0,.72)';
      var tw2 = ctx.measureText(txt).width;
      ctx.fillRect(ax - tw2 / 2 - 3, ay - 17, tw2 + 6, 13);
      ctx.fillStyle = '#e8e8e8';
      ctx.fillText(txt, ax, ay - 7);
    });

    cv.style.transform = '';
    stage.classList.toggle('carto-zoomed', cam.k !== 1);
    updateNote();
  }

  // Live gestures move the canvas with a CSS transform, which is one compositor
  // operation, and the field is re-sampled once the gesture stops.
  //
  // Re-sampling per wheel tick was the first version: ~40 ms each, so a trackpad
  // sending thirty events a second queued redraws faster than they finished and
  // the map lagged a second behind the fingers. The transform is the preview
  // and the resample is the answer.
  var pend = null, prev = null;

  // mark captures where the camera was when the gesture started. It has to run
  // *before* the handler moves the camera: the first version initialised prev
  // lazily inside the preview, by which time cam had already been updated, so
  // base and target were the same numbers and every preview transform came out
  // as the identity. The painted frame never moved until the resample landed,
  // which read as a map that ignored the first scroll of every gesture.
  function mark() { if (!prev) prev = { cx: cam.cx, cy: cam.cy, k: cam.k }; }

  function previewing() {
    if (!prev) return;
    var sX = cam.k / prev.k;
    // The painted frame shows prev's window. Sliding it by the camera's move,
    // in that frame's pixels, is what makes the preview line up with the
    // resample that replaces it.
    var tx = (prev.cx - cam.cx) * cam.k * W, ty = (prev.cy - cam.cy) * cam.k * Hpx;
    cv.style.transformOrigin = '50% 50%';
    cv.style.transform = 'translate(' + tx + 'px,' + ty + 'px) scale(' + sX + ')';
  }
  function settle() {
    if (pend) clearTimeout(pend);
    pend = setTimeout(function () { prev = null; paint(); }, 110);
  }

  cv.addEventListener('wheel', function (ev) {
    ev.preventDefault();
    var r = cv.getBoundingClientRect();
    var v = win();
    // Zoom about the cursor: the point under the pointer stays under it.
    var ux = v.x0 + (ev.clientX - r.left) / r.width * v.w;
    var uy = v.y0 + (ev.clientY - r.top) / r.height * v.w;
    var k = Math.min(14, Math.max(1, cam.k * Math.exp(-ev.deltaY * 0.0014)));
    var f = cam.k / k;
    mark();
    cam.cx = ux + (cam.cx - ux) * f;
    cam.cy = uy + (cam.cy - uy) * f;
    cam.k = k;
    previewing();
    settle();
  }, { passive: false });

  var drag = null;
  cv.addEventListener('pointerdown', function (ev) {
    if (ev.button !== 0) return;
    // Same reason as the svg camera: capturing here would retarget the click
    // and make the terrain unclickable. Taken once the drag is real.
    drag = { x: ev.clientX, y: ev.clientY, moved: false, id: ev.pointerId };
  });
  cv.addEventListener('pointermove', function (ev) {
    if (!drag) {
      hover(ev, false);
      return;
    }
    var r = cv.getBoundingClientRect();
    var v = win();
    var dx = (ev.clientX - drag.x) / r.width * v.w, dy = (ev.clientY - drag.y) / r.height * v.w;
    if (!drag.moved && Math.abs(dx) + Math.abs(dy) < 0.004) return;
    if (!drag.moved) {
      drag.moved = true;
      try { cv.setPointerCapture(drag.id); } catch (_) { /* pointer already gone */ }
    }
    tipHide();
    mark();
    cam.cx -= dx; cam.cy -= dy;
    drag.x = ev.clientX; drag.y = ev.clientY;
    previewing();
    settle();
  });
  function endDrag(ev) {
    if (!drag) return;
    var moved = drag.moved;
    if (ev && ev.pointerId !== undefined && cv.hasPointerCapture(ev.pointerId)) {
      cv.releasePointerCapture(ev.pointerId);
    }
    drag = null;
    if (moved) { cv.__swallow = true; setTimeout(function () { cv.__swallow = false; }, 0); }
  }
  cv.addEventListener('pointerup', endDrag);
  cv.addEventListener('pointercancel', endDrag);
  cv.addEventListener('mouseleave', tipHide);

  // Hover and click both resolve to the package nearest the cursor, in window
  // coordinates so the hit radius stays a constant distance on screen however
  // far the reader has zoomed in.
  function nearest(ev) {
    var r = cv.getBoundingClientRect();
    var v = win();
    var mx = v.x0 + (ev.clientX - r.left) / r.width * v.w;
    var my = v.y0 + (ev.clientY - r.top) / r.height * v.w;
    var best = null, bd = 1e9;
    // Weighted so the hit radius is a circle on screen rather than in layout
    // space: y is compressed by Hpx/W when drawn, so an unweighted distance
    // picks a package the reader can see is further away than another.
    var ay = (Hpx / W) * (Hpx / W);
    for (var j = 0; j < nodes.length; j++) {
      var q = pos[nodes[j].path];
      var qd = (q.x - mx) * (q.x - mx) + (q.y - my) * (q.y - my) * ay;
      if (qd < bd) { bd = qd; best = nodes[j]; }
    }
    return (best && bd < 0.0009 / (cam.k * cam.k)) ? best : null;
  }
  function hover(ev) {
    var n = nearest(ev);
    if (n) tipShow(ev, nodeCard(n)); else tipHide();
  }
  cv.addEventListener('click', function (ev) {
    if (cv.__swallow) return;
    var n = nearest(ev);
    if (n) { tipHide(); window.navigate(pathHref(n)); }
  });

  stage.appendChild(cv);
  stage.appendChild(camChrome(function () {
    cam.cx = 0.5; cam.cy = 0.5; cam.k = 1; prev = null; paint();
  }, function (f) {
    var r = cv.getBoundingClientRect();
    cv.dispatchEvent(new WheelEvent('wheel', {
      deltaY: f, clientX: r.left + r.width / 2, clientY: r.top + r.height / 2, bubbles: false }));
  }));

  var noteEl = null;
  function updateNote() {
    if (!noteEl) return;
    noteEl.textContent = '';
    var parts = [
      'The one drawing here that does not group by namespace, which matters because one namespace ' +
      'holds ', [String(groupNamespaces(nodes)[0].n)], ' of the ', [String(nodes.length)],
      ' packages on this chain and every other view is partly a picture of that. ',
      'Position comes only from the import graph: packages start on a fixed spiral, are pulled ' +
      'toward what they import and pushed off their near neighbours for a fixed number of rounds, ' +
      'so code that shares dependencies ends up as one landmass and nothing collapses onto a hub. ' +
      'Elevation is ', [met.label], ' on a ', [S.scale], ' scale, accumulated from every package ' +
      'nearby, so broad high ground is a dense and busy region and a lone peak is one package ' +
      'carrying its neighbourhood alone. Absolute position and compass direction carry nothing; ' +
      'the shading is a light from the ', [cardinal(S.sun)], ' and carries nothing either. ',
      'Zoom re-samples the field over the smaller window rather than magnifying it, so two ' +
      'packages that share a hill here separate into two when you zoom into them. Colour is ' +
      'pinned to the whole map\u2019s peak at every zoom, so a band always means the same ' +
      'elevation. ',
      'Showing ', [cam.k === 1 ? 'the whole map' : (cam.k).toFixed(1) + '\u00d7 in'], ', ',
      [String(labelled.length)], ' peaks named; hover anywhere for the package nearest the cursor.',
    ];
    parts.forEach(function (q) {
      if (typeof q === 'string') noteEl.appendChild(document.createTextNode(q));
      else noteEl.appendChild(window.el('b', {}, q[0]));
    });
  }

  noteEl = note(below, []);
  legend(below, [
    ['#0c1420', 'sea', '\u00b7 nothing deployed nearby'],
    ['#2f6338', 'plain', '\u00b7 a populated region'],
    ['#c98a3a', 'peak', '\u00b7 the top of the chosen metric'],
    ['#ffd93d', 'gold dot', '\u00b7 that package was called in the window'],
  ]);

  paint();
}

// cardinal names a compass bearing, for the relief's light.
function cardinal(deg) {
  var names = ['north', 'north-east', 'east', 'south-east', 'south', 'south-west', 'west', 'north-west'];
  return names[Math.round((((deg % 360) + 360) % 360) / 45) % 8];
}

// -----------------------------------------------------------------------------
// Entry point
// -----------------------------------------------------------------------------

// Called by index.html's route() every time /cartography is entered. Re-reads
// the URL each time so a shared link opens on the view it names, and drops any
// animation the previous visit left running.
window.loadCartography = function (root) {
  stopAnim();
  readURL();
  render(root);
};

// Leaving the page has to stop the settlement walk: a cancelled frame loop is
// the difference between an idle tab and one holding a core at 60 Hz for as
// long as it stays open.
window.unloadCartography = function () { stopAnim(); tipHide(); };

window.addEventListener('mousemove', function (ev) {
  if (TIP && TIP.style.display === 'block') tipMove(ev);
}, { passive: true });

})();
