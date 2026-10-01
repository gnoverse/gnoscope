/* Cartography: ten drawings of one chain, as a place rather than a table.
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
 * Why ten and not one
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
 * and five more borrowed from city-builders, each with a centre (the second
 * five, below, says why):
 *
 *   metropolis  where is downtown: what the chain leans on and uses most
 *   boroughs    how do namespaces compare when each gets the same ground
 *   hexes       which namespaces are neighbours, and what each one yields
 *   frontier    who settled the chain, and in what order
 *   old town    how did the whole chain grow, era by era
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
 * so three rules hold across all ten:
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
  { id: 'metropolis', name: 'metropolis', blurb: 'a downtown by land value, owner-blind, zoned by what each package does' },
  { id: 'boroughs',   name: 'boroughs',   blurb: 'one equal block per namespace, the busiest at the centre' },
  { id: 'hexes',      name: 'hexes',      blurb: 'a board of equal hexes, neighbours by imports, terrain by yield' },
  { id: 'frontier',   name: 'frontier',   blurb: 'deployers as players, settled outward from (0|0) in deploy order' },
  { id: 'oldtown',    name: 'old town',   blurb: 'a walled town grown ring by ring, one wall per era of deploys' },
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

// One load for every view. The import graph ignores the window by design
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
    el('p', {}, 'The same chain the rest of the site tabulates, drawn as a place. Ten metaphors, ' +
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
  // hexes is the one city-builder view without a size channel: a tile is the
  // same size whatever is in it, which is its point.
  var sized = S.view !== 'settlement' && S.view !== 'metro' && S.view !== 'hexes';
  // The three isometric drawings share one camera model, so they share its
  // controls: the compass and the plan view.
  var iso = S.view === 'city' || S.view === 'metropolis' || S.view === 'boroughs';

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
  }
  if (iso) {

    bar.appendChild(btnGroup('view', [['model', false, 'isometric, with heights'],
      ['plan', true, 'straight down, no heights: nothing hides behind a tower']],
      function (v) { return S.flat === v; },
      function (v) { if (S.flat !== v) { S.flat = v; writeURL(); restate(root); } }));

    // The compass. Fifteen-degree steps rather than a slider: the whole city is
    // rebuilt per step (589 buildings, ~1800 polygons), so a control that fires
    // on every pixel of a drag would queue redraws faster than they finish.
    var rot = el('div', { className: 'carto-grp' }, el('span', {}, 'rotate'));
    [['\u21ba', -15], ['\u21bb', 15]].forEach(function (o) {
      var b = el('button', { title: 'turn the map ' + (o[1] < 0 ? 'left' : 'right') + ' 15\u00b0' }, o[0]);
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
       metro: drawMetro, relief: drawRelief, metropolis: drawMetropolis, boroughs: drawBoroughs,
       hexes: drawHexes, frontier: drawFrontier, oldtown: drawOldTown })[S.view](stage, below, d);
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

// =============================================================================
// The second five: the chain as a city-builder would draw it
// =============================================================================
//
// The first five each answer one question well and share one weakness: four of
// them are laid out by namespace with a district's area set by its member
// count, and on mainnet one namespace holds 337 of 598 packages. The city is
// then a single slab with a village beside it, and there is no centre, because
// nothing in a row-packed layout says where the middle of the chain is.
//
// City-builders solved both problems a long time ago, so these borrow from them
// on purpose:
//
//   metropolis  SimCity: a downtown. Owner-blind, placed by land value, zoned
//               by what each package does.
//   boroughs    the same grid of streets, but every namespace gets one block of
//               the same size, so a big namespace is dense rather than wide.
//   hexes       a Catan board: one equal hex per namespace, neighbours chosen
//               by who imports whom, terrain by what it contributes most of.
//   frontier    a Travian world map: deployers are the players, the first
//               settler holds (0|0), later arrivals settle further out.
//   old town    a walled town grown ring by ring: the oldest code around the
//               market square, a new wall for every era of deploys.
//
// Every honesty rule at the top of this file holds here too. Where a game
// supplies a convention with no data behind it (a Catan number, a wilderness
// tile), the caption says it is a convention.

// byDeploy orders packages by when they reached the chain: time first, then
// height, then path. Time leads because genesis packages all carry height 0
// and a stable date, so a height-first sort would put them in the right group
// only by accident of the zero.
function byDeploy(a, b) {
  var ta = a.deployed_at ? Date.parse(a.deployed_at) : 0;
  var tb = b.deployed_at ? Date.parse(b.deployed_at) : 0;
  return ta - tb || (a.deploy_height || 0) - (b.deploy_height || 0) || a.path.localeCompare(b.path);
}

// landValue is the metropolis' one invented quantity, and it is invented from
// three real ones: how widely a package is imported, how often it was called,
// and by how many distinct addresses. Each is log-scaled against the chain's
// own maximum, so the score is a rank-like number in 0..2.5 that no single
// power-law outlier can own.
//
// Callers weigh half, because a realm with many callers is already a realm with
// many calls and counting both at full weight would count the same traffic
// twice.
function landValuer(nodes) {
  var mi = 0, mc = 0, mu = 0;
  nodes.forEach(function (n) {
    mi = Math.max(mi, n.importers || 0); mc = Math.max(mc, n.calls || 0); mu = Math.max(mu, n.unique_callers || 0);
  });
  var li = lg(mi) || 1, lc = lg(mc) || 1, lu = lg(mu) || 1;
  return function (n) { return lg(n.importers) / li + lg(n.calls) / lc + 0.5 * lg(n.unique_callers) / lu; };
}

// Zoning, SimCity's three colours plus two of its oddities. The zone is a
// function of two facts every row already carries (realm or not, called or
// not, imported or not), so a reader can check any plot against /contracts.
var ZONES = {
  C:     { hue: 212, sat: 62, name: 'commercial',  what: 'a realm called in the window' },
  R:     { hue: 128, sat: 44, name: 'residential', what: 'a realm nobody called in the window' },
  I:     { hue: 44,  sat: 72, name: 'industrial',  what: 'a pure package other code imports' },
  P:     { hue: 150, sat: 22, name: 'park',        what: 'a pure package nothing imports' },
  build: { hue: 28,  sat: 70, name: 'construction', what: 'parked: stored, never enabled' },
};
function zoneOf(n) {
  if (n.parked) return 'build';
  if (n.is_realm) return n.calls > 0 ? 'C' : 'R';
  return n.importers > 0 ? 'I' : 'P';
}
function zoneColor(z, l, s) { var Z = ZONES[z]; return 'hsl(' + Z.hue + ',' + (s || Z.sat) + '%,' + l + '%)'; }

function pts(a) { return a.map(function (q) { return q[0].toFixed(1) + ',' + q[1].toFixed(1); }).join(' '); }

// isoBox draws one building at a projected point: a roof and the two walls the
// camera can see, or a flat plot in the plan view. hw and hh are the roof's
// half-extents on screen, so a caller can give a mansion a wider footprint than
// a terrace house without a second drawing routine.
function isoBox(x, y, h, hw, hh, top, left, right) {
  var g = svgEl('g');
  var edge = { stroke: 'rgba(0,0,0,.35)', 'stroke-width': 0.5 };
  if (_cityCam.flat) {
    g.appendChild(svgEl('rect', Object.assign({ x: x - hw, y: y - hh, width: hw * 2, height: hh * 2, rx: 1,
      fill: top }, edge)));
    return g;
  }
  g.appendChild(svgEl('polygon', Object.assign({ points: pts([[x - hw, y - h], [x, y - h + hh], [x, y + hh], [x - hw, y]]),
    fill: left }, edge)));
  g.appendChild(svgEl('polygon', Object.assign({ points: pts([[x + hw, y - h], [x, y - h + hh], [x, y + hh], [x + hw, y]]),
    fill: right }, edge)));
  g.appendChild(svgEl('polygon', Object.assign({ points: pts([[x, y - h - hh], [x + hw, y - h], [x, y - h + hh], [x - hw, y - h]]),
    fill: top }, edge)));
  return g;
}

// isoLamps lights a building's facade, the same rule as the city: any call at
// all lights one window, more calls light more, capped by the storeys there
// are to light.
function isoLamps(g, n, x, y, h, hw, hh, storeys) {
  if (!(n.calls > 0)) return;
  if (_cityCam.flat) {
    g.appendChild(svgEl('circle', { cx: x, cy: y, r: Math.max(1.2, Math.min(2.4, hw * 0.25)), fill: '#ffd93d', opacity: 0.9 }));
    return;
  }
  var lamps = Math.min(storeys, Math.max(1, Math.round(lg(n.calls) * 1.6)));
  var s = Math.max(1.4, Math.min(2.6, hw * 0.16));
  for (var k = 0; k < lamps; k++) {
    var ly = y - (k + 0.6) * (h / Math.max(1, storeys));
    g.appendChild(svgEl('rect', { x: x - hw * 0.62, y: ly - s + hh * 0.18, width: s, height: s, fill: '#ffd93d', opacity: 0.85 }));
    g.appendChild(svgEl('rect', { x: x + hw * 0.4, y: ly - s + hh * 0.18, width: s, height: s, fill: '#ffd93d', opacity: 0.6 }));
  }
}

// storeysFor is the city's height rule, shared so the three isometric drawings
// cannot disagree about how tall a package is: the metric on the reader's
// scale, a floor of one, and pure packages held flat under a metric they do
// not have (see METRICS).
// isoParked outlines a parked package as scaffolding, in whichever of the
// two projections is on.
function isoParked(g, x, y, h, hw, hh) {
  var dash = { fill: 'none', stroke: 'var(--amber)', 'stroke-width': 1, 'stroke-dasharray': '2 2', opacity: 0.85 };
  g.appendChild(_cityCam.flat
    ? svgEl('rect', Object.assign({ x: x - hw, y: y - hh, width: hw * 2, height: hh * 2, rx: 1 }, dash))
    : svgEl('polygon', Object.assign({ points: pts([[x, y - h - hh], [x + hw, y - h], [x, y + hh], [x - hw, y]]) }, dash)));
}

function storeysFor(nodes, met, maxStorey) {
  var maxM = Math.max(1, nodes.reduce(function (m, n) { return Math.max(m, met.get(n) || 0); }, 0));
  var denom = Math.max(1e-9, sc(maxM));
  return function (n) {
    var v = met.get(n) || 0;
    var rises = n.is_realm || met.pure;
    return { rises: rises, storeys: rises && v > 0 ? Math.max(1, Math.round(sc(v) / denom * maxStorey)) : 1 };
  };
}

// isoFrame turns a list of projected extents into a viewBox and an svg. Every
// isometric view needs the frame to be the bounds of what is drawn and nothing
// more, for the reason drawCity gives at length.
function isoFrame(ext, pad) {
  var minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
  ext.forEach(function (e) {
    minX = Math.min(minX, e[0]); maxX = Math.max(maxX, e[0]);
    minY = Math.min(minY, e[1]); maxY = Math.max(maxY, e[1]);
  });
  minX -= pad; maxX += pad; minY -= pad; maxY += pad;
  return svgEl('svg', { viewBox: minX + ' ' + minY + ' ' + (maxX - minX) + ' ' + (maxY - minY),
    preserveAspectRatio: 'xMidYMid meet', style: 'max-height:80vh' });
}

// An iso-projected circle on the ground, for the metropolis' ring roads.
function isoRing(cx, cy, r) {
  var out = [];
  for (var i = 0; i < 72; i++) {
    var a = i / 72 * Math.PI * 2;
    out.push(isoXY(cx + Math.cos(a) * r, cy + Math.sin(a) * r));
  }
  return out;
}

// =============================================================================
// 6. METROPOLIS  -- a downtown, by land value, zoned by role
// =============================================================================
//
// The view the city should have been if it had a centre. Ownership is thrown
// away: every package competes for the same land, the most valuable takes the
// plot nearest the middle, and the chain sorts itself into a downtown, a
// midtown and a sprawl the way a real city does under a bid-rent curve.
//
//   position   land value (landValuer), highest at the centre. The streets
//              are a fixed grid of three-by-three blocks; only which plot
//              each package lands on carries meaning, and only as distance.
//   storeys    the reader's metric, same rule as the city.
//   colour     zone: commercial, residential, industrial, park, construction.
//   windows    lit if called in the window, same as the city.

function drawMetropolis(stage, below, d) {
  var met = METRICS[S.metric];
  var nodes = d.nodes;
  var lv = landValuer(nodes);
  var ranked = nodes.slice().sort(function (a, b) {
    return lv(b) - lv(a) || b.calls - a.calls || a.path.localeCompare(b.path);
  });

  // Plots: three-by-three blocks with a one-cell street between them, on an
  // odd number of blocks so one block sits exactly on the centre. Enough
  // blocks for every package with room left over, because a city that fills
  // its square to the corners reads as a square and not as a city.
  var BLK = 3, P = 4;
  var L = Math.ceil(Math.sqrt(nodes.length / (BLK * BLK) * 1.5));
  if (L % 2 === 0) L++;
  var mid = Math.floor(L / 2) * P + 1;
  var plots = [];
  for (var bx = 0; bx < L; bx++) for (var by = 0; by < L; by++) {
    for (var px = 0; px < BLK; px++) for (var py = 0; py < BLK; py++) {
      var gx = bx * P + px, gy = by * P + py;
      // A small seeded jitter on the distance, so the edge of the built-up
      // area is ragged like a real city's instead of a perfect disc. It moves
      // a plot by less than one ring of plots, so it cannot carry a package
      // from midtown to downtown.
      var j = (hash32(gx + ',' + gy) % 1000) / 1000 * 1.6;
      plots.push({ gx: gx, gy: gy, bx: bx, by: by, d: Math.hypot(gx - mid, gy - mid) + j });
    }
  }
  plots.sort(function (a, b) { return a.d - b.d || a.gx - b.gx || a.gy - b.gy; });

  cityCamera(L * P - 1, L * P - 1);
  // The camera rotates about the grid centre, which for this grid is the
  // centre plot: rotation then turns the city about its own downtown.
  _cityCam.cx = mid; _cityCam.cy = mid;

  var height = storeysFor(nodes, met, 18);
  var cells = [], usedBlock = {};
  ranked.forEach(function (n, i) {
    var p = plots[i];
    var hs = height(n);
    usedBlock[p.bx + ':' + p.by] = true;
    cells.push({ n: n, gx: p.gx, gy: p.gy, rank: i, dist: p.d, z: zoneOf(n),
      storeys: hs.storeys, h: _cityCam.flat ? 0 : (hs.rises ? hs.storeys * ISO.storey : ISO.storey * 0.6),
      p: isoXY(p.gx, p.gy) });
  });
  cells.sort(function (a, b) { return a.p[1] - b.p[1]; });

  // Districts as rings: the radius that contains the top 5%, 25% and 60% of
  // the chain by land value. They are drawn, not just named in the caption,
  // because the whole claim of this view is that the chain has a middle.
  var ringAt = function (q) { return plots[Math.max(0, Math.round(q * nodes.length) - 1)].d + 0.6; };
  var RINGS = [['downtown', ringAt(0.05)], ['midtown', ringAt(0.25)], ['suburbs', ringAt(0.6)], ['outskirts', ringAt(1)]];

  var ext = [];
  cells.forEach(function (c) { ext.push([c.p[0] - ISO.tw, c.p[1] - c.h - ISO.th], [c.p[0] + ISO.tw, c.p[1] + ISO.th]); });
  RINGS.forEach(function (r) { isoRing(mid, mid, r[1]).forEach(function (q) { ext.push(q); }); });
  var svg = isoFrame(ext, 30);

  // Asphalt under every block anyone built on, then the block's own pavement
  // on top. Adjacent asphalt squares abut, which is what draws the streets:
  // nothing here draws a road, the roads are the gaps between blocks.
  var ground = svgEl('g');
  var corners = function (x0, y0, x1, y1) { return pts([isoXY(x0, y0), isoXY(x1, y0), isoXY(x1, y1), isoXY(x0, y1)]); };
  Object.keys(usedBlock).forEach(function (k) {
    var b = k.split(':').map(Number), x0 = b[0] * P, y0 = b[1] * P;
    ground.appendChild(svgEl('polygon', { points: corners(x0 - 1.02, y0 - 1.02, x0 + BLK + 0.02, y0 + BLK + 0.02),
      fill: '#17181c' }));
  });
  Object.keys(usedBlock).forEach(function (k) {
    var b = k.split(':').map(Number), x0 = b[0] * P, y0 = b[1] * P;
    ground.appendChild(svgEl('polygon', { points: corners(x0 - 0.55, y0 - 0.55, x0 + BLK - 0.45, y0 + BLK - 0.45),
      fill: '#24262c', stroke: '#2e3139', 'stroke-width': 0.6 }));
    // A dashed centre line on the street along the block's two near edges.
    var a = isoXY(x0 - 1, y0 + BLK - 0.5 + 0.5), e = isoXY(x0 + BLK, y0 + BLK - 0.5 + 0.5);
    ground.appendChild(svgEl('line', { x1: a[0], y1: a[1], x2: e[0], y2: e[1], stroke: '#3a3d45',
      'stroke-width': 0.7, 'stroke-dasharray': '3 4' }));
    var a2 = isoXY(x0 + BLK, y0 - 1), e2 = isoXY(x0 + BLK, y0 + BLK);
    ground.appendChild(svgEl('line', { x1: a2[0], y1: a2[1], x2: e2[0], y2: e2[1], stroke: '#3a3d45',
      'stroke-width': 0.7, 'stroke-dasharray': '3 4' }));
  });
  svg.appendChild(ground);

  var rings = svgEl('g');
  RINGS.forEach(function (r, i) {
    var ring = isoRing(mid, mid, r[1]);
    rings.appendChild(svgEl('polygon', { points: pts(ring), fill: 'none',
      stroke: i === 0 ? 'var(--accent)' : 'rgba(255,255,255,.22)', 'stroke-width': i === 0 ? 1.4 : 1,
      'stroke-dasharray': '6 5' }));
  });
  svg.appendChild(rings);

  var lit = 0, zc = { C: 0, R: 0, I: 0, P: 0, build: 0 };
  var town = svgEl('g');
  var hw = ISO.tw * 0.74, hh = (_cityCam.flat ? ISO.tw * 0.52 : ISO.th) * 0.74;
  // A plan cell is one tw wide (isoXY's flat branch), so a plot has to stay
  // under half of that or neighbours overlap into one slab.
  if (_cityCam.flat) { hw = ISO.tw * 0.42; hh = ISO.tw * 0.52 * 0.42; }
  cells.forEach(function (c) {
    var n = c.n, x = c.p[0], y = c.p[1], g;
    zc[c.z]++;
    if (n.calls > 0) lit++;
    if (c.z === 'P') {
      // A park is a lawn with trees on it, not a building: a package nothing
      // imports and nobody calls holds no weight in the city, and drawing a
      // shed for it would make the sprawl look busier than it is.
      g = isoBox(x, y, _cityCam.flat ? 0 : 1.5, hw, hh, zoneColor('P', 26), zoneColor('P', 18), zoneColor('P', 14));
      var rng = rngFrom(n.path);
      for (var t = 0; t < 2; t++) {
        var tx = x + (rng() - 0.5) * hw, ty = y - 1.5 + (rng() - 0.5) * hh;
        if (!_cityCam.flat) g.appendChild(svgEl('line', { x1: tx, y1: ty, x2: tx, y2: ty - 4, stroke: '#3b2a1a', 'stroke-width': 1 }));
        g.appendChild(svgEl('circle', { cx: tx, cy: _cityCam.flat ? ty : ty - 6, r: 3.2, fill: 'hsl(130,35%,30%)' }));
      }
    } else {
      var base = c.z === 'C' ? 56 : c.z === 'I' ? 52 : c.z === 'build' ? 40 : 46;
      var liftL = _cityCam.flat ? (c.storeys / 18) * 22 : 0;
      g = isoBox(x, y, c.h, hw, hh, zoneColor(c.z, base + 10 + liftL), zoneColor(c.z, base - 12), zoneColor(c.z, base - 24));
      // Industry gets a chimney, so the zone reads in the model's shape and
      // not only in a hue a colour-blind reader may not separate from green.
      if (c.z === 'I' && !_cityCam.flat) {
        g.appendChild(svgEl('rect', { x: x + hw * 0.25, y: y - c.h - 9, width: 3, height: 9, fill: zoneColor('I', 28) }));
      }
      if (c.z === 'build') isoParked(g, x, y, c.h, hw, hh);
      isoLamps(g, n, x, y, c.h, hw, hh, c.storeys);
    }
    bindNode(g, n, [['zone', ZONES[c.z].name], ['land value rank', '#' + (c.rank + 1) + ' of ' + nodes.length],
      ['storeys', String(c.storeys) + ' (' + met.label + ')']]);
    town.appendChild(g);
  });
  svg.appendChild(town);

  // Ring names on top of the town, at each ring's lowest point on screen (the
  // edge nearest the reader) on a dark halo: drawn under the buildings, three
  // of the four were hidden behind the very towers they name.
  var ringNames = svgEl('g', { 'pointer-events': 'none' });
  RINGS.forEach(function (r, i) {
    var ring = isoRing(mid, mid, r[1]);
    var low = ring.reduce(function (m, q) { return q[1] > m[1] ? q : m; }, ring[0]);
    var t = svgEl('text', { x: low[0], y: low[1] + 4, 'text-anchor': 'middle',
      fill: i === 0 ? 'var(--accent)' : 'rgba(255,255,255,.72)', 'font-size': 12, 'font-family': 'var(--mono)',
      'paint-order': 'stroke', stroke: 'rgba(7,7,11,.9)', 'stroke-width': 4 });
    t.textContent = r[0];
    ringNames.appendChild(t);
  });
  svg.appendChild(ringNames);
  mountSVG(stage, svg);

  var top = ranked.slice(0, 3).map(function (n) { return n.name; }).join(', ');
  note(below, [
    'Owner-blind: all ', [String(nodes.length)], ' packages compete for the same land, and the ' +
    'most valuable takes the plot nearest the centre. Land value is the one composite on this ' +
    'page, and it is built from three real numbers: how many packages import it, how many calls it ' +
    'took in the last ', [S.window], ', and (at half weight, so the same traffic is not counted ' +
    'twice) from how many addresses, each log-scaled against the chain’s maximum. Downtown is ' +
    'therefore what the chain leans on and uses most, and on this chain it is led by ', [top], '. ',
    'The rings mark the radius holding the top 5%, 25% and 60% by land value. Storeys are ',
    [met.label], ' on a ', [S.scale], ' scale, the same rule as the city. Colour is the zone: ',
    [String(zc.C)], ' commercial (a realm called in the window), ', [String(zc.R)],
    ' residential (a realm nobody called), ', [String(zc.I)], ' industrial (a pure package others ' +
    'import, with a chimney), ', [String(zc.P)], ' parks (a pure package nothing imports), ',
    [String(zc.build)], ' under construction (parked). ', [String(lit)], ' buildings have lit windows. ',
    'The street grid is fixed and carries nothing; only distance from the centre does, and which ' +
    'of two plots at the same distance a package takes is a tie broken by a seeded jitter.',
  ]);
  legend(below, [
    [zoneColor('C', 56), 'commercial', '· realm, called'],
    [zoneColor('R', 46), 'residential', '· realm, not called'],
    [zoneColor('I', 52), 'industrial', '· pure package, imported'],
    [zoneColor('P', 30), 'park', '· pure package, not imported'],
    ['var(--accent)', 'downtown', '· top 5% by land value'],
  ]);
}

// =============================================================================
// 7. BOROUGHS  -- one equal block per namespace, around a centre
// =============================================================================
//
// The city's districts with the one change it needed: every namespace gets the
// same ground. A namespace of 337 packages and a namespace of one are both one
// block; the first is a dense quarter of narrow towers, the second a single
// mansion on a lawn. Size becomes density, which a reader can see without the
// biggest namespace pushing every other one off the edge of the frame.
//
// Blocks spiral out from the centre in order of the namespace's total in the
// chosen metric, so the middle of the map is the busiest namespace and the
// edge is the quietest, and every block has room for its own name.

function drawBoroughs(stage, below, d) {
  var met = METRICS[S.metric];
  var groups = groupNamespaces(d.nodes);
  groups.forEach(function (g) { g.score = g.nodes.reduce(function (a, n) { return a + (met.get(n) || 0); }, 0); });
  groups.sort(function (a, b) { return b.score - a.score || b.n - a.n || a.ns.localeCompare(b.ns); });

  var B = 6, ST = 1.6, P = B + ST;
  var L = Math.ceil(Math.sqrt(groups.length));
  if (L % 2 === 0) L++;
  var c0 = Math.floor(L / 2);
  var slots = [];
  for (var bx = 0; bx < L; bx++) for (var by = 0; by < L; by++) {
    // Chebyshev ring first, then the Euclidean distance inside it, then the
    // angle: a square spiral that fills the ring nearest the centre before
    // starting the next, so rank is distance.
    var dx = bx - c0, dy = by - c0;
    slots.push({ bx: bx, by: by, ring: Math.max(Math.abs(dx), Math.abs(dy)), d: Math.hypot(dx, dy),
      a: (Math.atan2(dy, dx) + Math.PI * 2.75) % (Math.PI * 2) });
  }
  slots.sort(function (a, b) { return a.ring - b.ring || a.d - b.d || a.a - b.a; });

  var span = L * P - ST;
  cityCamera(span, span);
  var height = storeysFor(d.nodes, met, 16);

  var cells = [];
  groups.forEach(function (g, gi) {
    var s = slots[gi];
    g.ox = s.bx * P; g.oy = s.by * P; g.rank = gi;
    var k = Math.max(1, Math.ceil(Math.sqrt(g.n)));
    var plot = B / k;
    // A mansion is capped well short of the whole block, so a one-package
    // namespace still reads as a building on a lawn and not as a block-sized
    // monolith that looks like the most important thing on the map.
    var foot = Math.min(plot * 0.78, 2.4);
    g.nodes.forEach(function (n, i) {
      var gx = g.ox + ((i % k) + 0.5) * plot - 0.5, gy = g.oy + (Math.floor(i / k) + 0.5) * plot - 0.5;
      // Rows that do not fill are centred, so a block of five sits as two
      // rows in the middle of the lawn and not crammed against one side.
      var rows = Math.ceil(g.n / k);
      gy += (k - rows) * plot / 2;
      var hs = height(n);
      cells.push({ n: n, g: g, foot: foot, storeys: hs.storeys,
        h: _cityCam.flat ? 0 : (hs.rises ? hs.storeys * ISO.storey : ISO.storey * 0.6) * Math.max(0.55, Math.min(1, foot * 0.9)),
        p: isoXY(gx, gy) });
    });
  });
  cells.sort(function (a, b) { return a.p[1] - b.p[1]; });

  var ext = [];
  groups.forEach(function (g) {
    [[g.ox - 1, g.oy - 1], [g.ox + B, g.oy - 1], [g.ox - 1, g.oy + B + 1], [g.ox + B, g.oy + B + 1]]
      .forEach(function (q) { ext.push(isoXY(q[0], q[1])); });
  });
  cells.forEach(function (c) { ext.push([c.p[0], c.p[1] - c.h - ISO.th * 2]); });
  var svg = isoFrame(ext, 24);

  var corners = function (x0, y0, x1, y1) { return pts([isoXY(x0, y0), isoXY(x1, y0), isoXY(x1, y1), isoXY(x0, y1)]); };
  var ground = svgEl('g');
  // One asphalt sheet under the whole used area, so the streets are
  // continuous even where a slot in the last ring is empty.
  ground.appendChild(svgEl('polygon', { points: corners(-ST, -ST, span + ST - 1, span + ST - 1), fill: '#15161a' }));
  groups.forEach(function (g) {
    ground.appendChild(svgEl('polygon', { points: corners(g.ox - 0.75, g.oy - 0.75, g.ox + B - 0.25, g.oy + B - 0.25),
      fill: nsColor(g.ns, 10, 34), stroke: nsColor(g.ns, 24, 34), 'stroke-width': 1, 'class': 'carto-block',
      'data-ns': g.ns }));
  });
  // The centre: the busiest namespace's block is outlined so the middle of the
  // map is visible as a middle and not inferred from the caption.
  var g0 = groups[0];
  ground.appendChild(svgEl('polygon', { points: corners(g0.ox - 1.05, g0.oy - 1.05, g0.ox + B + 0.05, g0.oy + B + 0.05),
    fill: 'none', stroke: 'var(--accent)', 'stroke-width': 1.3, 'stroke-dasharray': '6 4' }));
  svg.appendChild(ground);

  var town = svgEl('g');
  var lit = 0;
  cells.forEach(function (c) {
    var n = c.n, x = c.p[0], y = c.p[1];
    var hw = ISO.tw * c.foot * 0.5, hh = (_cityCam.flat ? ISO.tw * 0.52 : ISO.th) * c.foot * 0.5;
    if (!_cityCam.flat) { hw = ISO.tw * c.foot * 0.62; hh = ISO.th * c.foot * 0.62; }
    var base = n.is_realm ? 58 : 34;
    var liftL = _cityCam.flat ? (c.storeys / 16) * 26 : 0;
    var g = isoBox(x, y, c.h, hw, hh, nsColor(n.namespace, base + 8 + liftL), nsColor(n.namespace, base - 16),
      nsColor(n.namespace, base - 28));
    if (n.calls > 0) lit++;
    isoLamps(g, n, x, y, c.h, hw, hh, c.storeys);
    if (n.parked) isoParked(g, x, y, c.h, hw, hh);
    bindNode(g, n, [['borough', c.g.ns + ' · #' + (c.g.rank + 1) + ' from the centre'],
      ['storeys', String(c.storeys) + ' (' + met.label + ')']]);
    town.appendChild(g);
  });
  svg.appendChild(town);

  // Names last, on the street in front of each block: every block is the same
  // size, so every one has room, which the city could never promise.
  var labels = svgEl('g');
  groups.forEach(function (g) {
    var a = isoXY(g.ox + B / 2 - 0.5, g.oy + B - 0.1);
    var t = svgEl('text', { x: a[0], y: a[1] + 12, 'text-anchor': 'middle', fill: nsColor(g.ns, 70),
      'font-size': 10.5, 'font-family': 'var(--mono)', 'paint-order': 'stroke', stroke: '#0a0a0e',
      'stroke-width': 3 });
    t.textContent = (g.ns.length > 16 ? g.ns.slice(0, 6) + '…' + g.ns.slice(-4) : g.ns) + ' · ' + g.n;
    t.addEventListener('mousemove', function (ev) {
      tipShow(ev, ['namespace ' + g.ns, ['packages', String(g.n)], ['called in ' + S.window, String(g.live)],
        [met.label + ' (total)', met.label === 'storage' ? fmtBytes(g.score) : window.fmtNum(g.score)],
        ['rank from the centre', '#' + (g.rank + 1)]]);
    });
    t.addEventListener('mouseleave', tipHide);
    labels.appendChild(t);
  });
  svg.appendChild(labels);
  mountSVG(stage, svg);

  var biggest = groups.slice().sort(function (a, b) { return b.n - a.n; })[0];
  note(below, [
    'One block per namespace, ', [String(groups.length)], ' of them, every block the same size. ' +
    'Package count becomes density instead of area: ', [biggest.ns], '’s ', [String(biggest.n)],
    ' packages are a packed quarter of narrow towers, and a namespace of one is a single house on a ' +
    'lawn, so the biggest namespace no longer decides the shape of the whole map. Blocks spiral ' +
    'outward from the centre by the namespace’s total ', [met.label], ', so the outlined block in ' +
    'the middle, ', [g0.ns], ', is the busiest by that measure and the corners are the quietest. ' +
    'Storeys are ', [met.label], ' on a ', [S.scale], ' scale, the same rule as the city; ' +
    'hue is the namespace; ', [String(lit)], ' buildings have lit windows (called in the last ',
    [S.window], '). Where a building stands inside its block is deployment-agnostic and ordered ' +
    'busiest first, the same order as the city.',
  ]);
  legend(below, [
    ['var(--accent)', 'outlined block', '· the centre: busiest namespace by ' + met.label],
    ['var(--amber)', 'lit window', '· called in the window'],
    [nsColor(biggest.ns, 58), 'dense block', '· many packages on equal ground'],
  ]);
}

// =============================================================================
// 8. HEXES  -- a Catan board, one equal hex per namespace
// =============================================================================
//
// The view about namespaces as units. Every namespace is one hex of the same
// size, which is the board game's whole conceit: the land is equal, what it
// produces is not.
//
//   adjacency  chosen from the import graph. The hub everyone imports sits in
//              the middle, and each slot after it, spiralling outward, takes
//              the namespace with the most imports to and from its already
//              placed neighbours. A road on a shared edge is real import flow
//              between those two neighbours.
//   terrain    what the namespace contributes most of, as a share of the
//              chain: grain for calls, wool for callers, lumber for being
//              imported, ore for gas, brick for storage. Desert when nothing
//              in it was called or imported.
//   token      the game's number disc, by rank of calls in the window: 6 and
//              8 (five pips, red) are the busiest, 2 and 12 the least. The
//              number is the game's convention; the rank behind it is real.
//   pieces     its busiest called realms, up to six, on the hex's corners.
//              A city (the big piece) when that realm is in the chain's top
//              tenth by calls, a settlement otherwise.

var TERRAIN = {
  fields:    { q: 'calls',     fill: '#cfa83f', name: 'fields',    res: 'grain',  what: 'calls' },
  pasture:   { q: 'callers',   fill: '#86b552', name: 'pasture',   res: 'wool',   what: 'distinct callers' },
  forest:    { q: 'importers', fill: '#2f6a3c', name: 'forest',    res: 'lumber', what: 'being imported' },
  mountains: { q: 'gas',       fill: '#7f8590', name: 'mountains', res: 'ore',    what: 'gas' },
  hills:     { q: 'storage',   fill: '#b25a33', name: 'hills',     res: 'brick',  what: 'storage' },
  desert:    { q: null,        fill: '#d6c596', name: 'desert',    res: 'nothing', what: 'nothing called or imported' },
};

function hexCorner(cx, cy, r, i) {
  var a = Math.PI / 180 * (60 * i - 30);
  return [cx + r * Math.cos(a), cy + r * Math.sin(a)];
}

// Axial hex coordinates in spiral order: the centre, then ring 1, ring 2...
// The six axial directions, in the order a ring walk takes them.
var HEX_DIRS = [[1, 0], [1, -1], [0, -1], [-1, 0], [-1, 1], [0, 1]];
function hexSpiral(count) {
  var out = [[0, 0]];
  for (var k = 1; out.length < count; k++) {
    var q = HEX_DIRS[4][0] * k, r = HEX_DIRS[4][1] * k;
    for (var side = 0; side < 6; side++) {
      for (var s = 0; s < k; s++) {
        out.push([q, r]);
        q += HEX_DIRS[side][0]; r += HEX_DIRS[side][1];
      }
    }
  }
  return out;
}

function drawHexes(stage, below, d) {
  var groups = groupNamespaces(d.nodes);
  var byNs = {};
  groups.forEach(function (g) { byNs[g.ns] = g; });

  // Import flow between namespaces, symmetric for placement, directed for the
  // tooltip.
  var flow = {}, inbound = {};
  d.imports.forEach(function (e) {
    var a = d.byPath[e.source], b = d.byPath[e.target];
    if (!a || !b || a.namespace === b.namespace) return;
    var k = a.namespace + '\u0000' + b.namespace;
    flow[k] = (flow[k] || 0) + 1;
    inbound[b.namespace] = (inbound[b.namespace] || 0) + 1;
  });
  var fl = function (a, b) { return (flow[a + '\u0000' + b] || 0) + (flow[b + '\u0000' + a] || 0); };

  // Terrain: the quantity in which this namespace's share of the chain is
  // largest. A share, not a raw total, so a small namespace that carries a
  // tenth of the chain's callers is pasture even if a big one has more of
  // every quantity in absolute terms.
  var Q = {
    calls: function (n) { return n.calls; }, callers: function (n) { return n.unique_callers; },
    importers: function (n) { return n.importers; }, gas: function (n) { return n.gas_used; },
    storage: function (n) { return n.storage_bytes; },
  };
  var tot = {};
  Object.keys(Q).forEach(function (q) { tot[q] = d.nodes.reduce(function (a, n) { return a + (Q[q](n) || 0); }, 0) || 1; });
  groups.forEach(function (g) {
    g.sum = {};
    Object.keys(Q).forEach(function (q) { g.sum[q] = g.nodes.reduce(function (a, n) { return a + (Q[q](n) || 0); }, 0); });
    if (!g.sum.calls && !g.sum.importers) { g.terrain = 'desert'; g.share = 0; return; }
    var best = null, bs = -1;
    Object.keys(TERRAIN).forEach(function (t) {
      var q = TERRAIN[t].q; if (!q) return;
      var s = g.sum[q] / tot[q];
      if (s > bs) { bs = s; best = t; }
    });
    g.terrain = best; g.share = bs;
  });

  // Placement. The centre is the namespace the rest of the chain imports most;
  // every later slot takes the unplaced namespace with the strongest ties to
  // that slot's placed neighbours, falling back to ties with anything placed,
  // then size. Greedy, deterministic, and every adjacency it produces is one
  // the import graph argued for.
  var slots = hexSpiral(groups.length);
  var center = groups.slice().sort(function (a, b) {
    return (inbound[b.ns] || 0) - (inbound[a.ns] || 0) || b.n - a.n || a.ns.localeCompare(b.ns);
  })[0];
  var at = {}, placed = [center], left = groups.filter(function (g) { return g !== center; });
  center.q = 0; center.r = 0; at['0,0'] = center;
  for (var si = 1; si < slots.length && left.length; si++) {
    var sq = slots[si][0], sr = slots[si][1];
    var nb = HEX_DIRS.map(function (dd) { return at[(sq + dd[0]) + ',' + (sr + dd[1])]; }).filter(Boolean);
    var best = null, bk = null;
    left.forEach(function (g) {
      var local = nb.reduce(function (a, h) { return a + fl(g.ns, h.ns); }, 0);
      var glob = placed.reduce(function (a, h) { return a + fl(g.ns, h.ns); }, 0);
      var key = [local, glob, g.n];
      if (!bk || key[0] > bk[0] || (key[0] === bk[0] && (key[1] > bk[1] || (key[1] === bk[1] &&
          (key[2] > bk[2] || (key[2] === bk[2] && g.ns < best.ns)))))) { best = g; bk = key; }
    });
    best.q = sq; best.r = sr; at[sq + ',' + sr] = best;
    placed.push(best);
    left.splice(left.indexOf(best), 1);
  }

  // Number tokens, by rank of calls among the namespaces that had any.
  var active = groups.filter(function (g) { return g.sum.calls > 0; })
    .sort(function (a, b) { return b.sum.calls - a.sum.calls || b.sum.callers - a.sum.callers || a.ns.localeCompare(b.ns); });
  active.forEach(function (g, i) {
    var f = i / Math.max(1, active.length);
    var pair = f < 0.12 ? [6, 8] : f < 0.32 ? [5, 9] : f < 0.55 ? [4, 10] : f < 0.78 ? [3, 11] : [2, 12];
    g.token = pair[i % 2];
    g.pips = 6 - Math.abs(7 - g.token);
  });

  // Cities: a realm in the chain's top tenth by calls.
  var called = d.nodes.filter(function (n) { return n.is_realm && n.calls > 0; })
    .sort(function (a, b) { return b.calls - a.calls; });
  var cityAt = called.length ? called[Math.max(0, Math.floor(called.length * 0.1) - 1)].calls : Infinity;

  var R = 62, sq3 = Math.sqrt(3);
  var hx = function (q, r) { return [R * sq3 * (q + r / 2), R * 1.5 * r]; };
  var rings = 0;
  slots.forEach(function (s) { rings = Math.max(rings, (Math.abs(s[0]) + Math.abs(s[1]) + Math.abs(s[0] + s[1])) / 2); });
  var sea = hexSpiral(1 + 3 * (rings + 1) * (rings + 2)).filter(function (s) { return !at[s[0] + ',' + s[1]]; });

  var ext = sea.map(function (s) { return hx(s[0], s[1]); });
  var minX = Math.min.apply(null, ext.map(function (p) { return p[0]; })) - R - 10;
  var maxX = Math.max.apply(null, ext.map(function (p) { return p[0]; })) + R + 10;
  var minY = Math.min.apply(null, ext.map(function (p) { return p[1]; })) - R - 10;
  var maxY = Math.max.apply(null, ext.map(function (p) { return p[1]; })) + R + 10;
  var svg = svgEl('svg', { viewBox: minX + ' ' + minY + ' ' + (maxX - minX) + ' ' + (maxY - minY),
    preserveAspectRatio: 'xMidYMid meet', style: 'max-height:82vh' });

  var hexPts = function (c, r) { var o = []; for (var i = 0; i < 6; i++) o.push(hexCorner(c[0], c[1], r, i)); return pts(o); };

  // The sea frame, the board's border, drawn as hexes so the coast is the
  // same shape as the land.
  var seaG = svgEl('g');
  sea.forEach(function (s) {
    var c = hx(s[0], s[1]);
    seaG.appendChild(svgEl('polygon', { points: hexPts(c, R - 1), fill: '#1d3f63', stroke: '#16314d', 'stroke-width': 2 }));
    var rng = rngFrom('sea' + s[0] + ',' + s[1]);
    for (var w = 0; w < 2; w++) {
      var wx = c[0] + (rng() - 0.5) * R, wy = c[1] + (rng() - 0.5) * R * 0.8;
      seaG.appendChild(svgEl('path', { d: 'M' + (wx - 7) + ',' + wy + ' q3.5,-3 7,0 t7,0', fill: 'none',
        stroke: '#3a6a98', 'stroke-width': 1.2, opacity: 0.7 }));
    }
  });
  svg.appendChild(seaG);

  var land = svgEl('g');
  placed.forEach(function (g) {
    var c = hx(g.q, g.r), T = TERRAIN[g.terrain];
    var hex = svgEl('g');
    hex.appendChild(svgEl('polygon', { points: hexPts(c, R - 1.5), fill: T.fill, stroke: '#e9dcb5', 'stroke-width': 3,
      'class': 'carto-hex', 'data-ns': g.ns, 'data-terrain': g.terrain }));
    // A darker inner bevel, which is most of what makes a flat hexagon read
    // as a cardboard tile.
    hex.appendChild(svgEl('polygon', { points: hexPts(c, R - 7), fill: 'none', stroke: 'rgba(0,0,0,.18)', 'stroke-width': 4 }));
    terrainGlyphs(hex, g, c, R);
    hex.addEventListener('mousemove', function (ev) {
      tipShow(ev, ['namespace ' + g.ns, ['terrain', T.name + ' (' + T.res + ')'],
        ['because', g.terrain === 'desert' ? T.what : 'its biggest share of the chain is ' + T.what +
          ': ' + (g.share * 100).toFixed(1) + '%'],
        ['packages', String(g.n) + ' (' + g.live + ' called)'],
        ['calls (' + S.window + ')', window.fmtNum(g.sum.calls)],
        ['imported from outside', window.fmtNum(inbound[g.ns] || 0)],
        ['token', g.token ? g.token + ' (' + g.pips + ' pips)' : 'none: no calls']]);
    });
    hex.addEventListener('mouseleave', tipHide);
    land.appendChild(hex);
  });
  svg.appendChild(land);

  // Roads on shared edges, wherever the two neighbours import each other.
  var roadsG = svgEl('g'), roadN = 0, roadFlow = 0, totalFlow = 0;
  Object.keys(flow).forEach(function (k) { totalFlow += flow[k]; });
  var maxF = 1;
  placed.forEach(function (g) { HEX_DIRS.forEach(function (dd) {
    var h = at[(g.q + dd[0]) + ',' + (g.r + dd[1])]; if (h) maxF = Math.max(maxF, fl(g.ns, h.ns));
  }); });
  placed.forEach(function (g) {
    HEX_DIRS.forEach(function (dd, di) {
      var h = at[(g.q + dd[0]) + ',' + (g.r + dd[1])];
      if (!h || h.ns <= g.ns) return;   // each edge once
      var f = fl(g.ns, h.ns);
      if (!f) return;
      roadN++; roadFlow += f;
      var a = hx(g.q, g.r), b = hx(h.q, h.r);
      // The shared edge is perpendicular to the line between centres, at its
      // midpoint, half a side long each way.
      var mx = (a[0] + b[0]) / 2, my = (a[1] + b[1]) / 2;
      var ux = (b[0] - a[0]) / (R * sq3), uy = (b[1] - a[1]) / (R * sq3);
      var half = R * 0.36;
      var w = 3 + lg(f) / lg(maxF) * 5;
      var road = svgEl('line', { x1: mx - uy * half, y1: my + ux * half, x2: mx + uy * half, y2: my - ux * half,
        stroke: '#3b2716', 'stroke-width': w + 2.4, 'stroke-linecap': 'round' });
      var top = svgEl('line', { x1: mx - uy * half, y1: my + ux * half, x2: mx + uy * half, y2: my - ux * half,
        stroke: '#e0c38c', 'stroke-width': w, 'stroke-linecap': 'round' });
      var rg = svgEl('g');
      rg.appendChild(road); rg.appendChild(top);
      rg.addEventListener('mousemove', function (ev) {
        tipShow(ev, [g.ns + '  ↔  ' + h.ns,
          [g.ns + ' imports ' + h.ns, window.fmtNum(flow[g.ns + '\u0000' + h.ns] || 0)],
          [h.ns + ' imports ' + g.ns, window.fmtNum(flow[h.ns + '\u0000' + g.ns] || 0)]]);
      });
      rg.addEventListener('mouseleave', tipHide);
      roadsG.appendChild(rg);
    });
  });
  svg.appendChild(roadsG);

  // Tokens, names and pieces on top.
  var top = svgEl('g'), cities = 0, settlements = 0;
  placed.forEach(function (g) {
    var c = hx(g.q, g.r);
    if (g.token) {
      var red = g.token === 6 || g.token === 8;
      top.appendChild(svgEl('circle', { cx: c[0], cy: c[1] + 4, r: 15.5, fill: '#f2e6c6', stroke: '#8a7650', 'stroke-width': 1.2 }));
      var t = svgEl('text', { x: c[0], y: c[1] + 8.5, 'text-anchor': 'middle', 'font-size': red ? 15 : 13,
        'font-weight': 'bold', fill: red ? '#b3261e' : '#2b2216', 'font-family': 'Georgia, serif' });
      t.textContent = String(g.token);
      top.appendChild(t);
      for (var p = 0; p < g.pips; p++) {
        top.appendChild(svgEl('circle', { cx: c[0] + (p - (g.pips - 1) / 2) * 3.4, cy: c[1] + 13.5, r: 1.1,
          fill: red ? '#b3261e' : '#2b2216' }));
      }
    }
    var lab = svgEl('text', { x: c[0], y: c[1] - 20, 'text-anchor': 'middle', 'font-size': 10.5,
      fill: '#fff', 'font-family': 'var(--mono)', 'paint-order': 'stroke', stroke: 'rgba(0,0,0,.72)', 'stroke-width': 3 });
    lab.textContent = g.ns.length > 14 ? g.ns.slice(0, 6) + '…' + g.ns.slice(-4) : g.ns;
    top.appendChild(lab);

    var busy = g.nodes.filter(function (n) { return n.is_realm && n.calls > 0; })
      .sort(function (a, b) { return b.calls - a.calls || a.path.localeCompare(b.path); }).slice(0, 6);
    busy.forEach(function (n, i) {
      // Corners in a fixed order, lower ones first because the name runs
      // across the upper two, pulled a little inside the tile so a piece
      // never sits on a neighbour's corner and reads as theirs.
      var v = hexCorner(c[0], c[1], R * 0.8, [1, 3, 2, 5, 0, 4][i]);
      var pg = svgEl('g');
      if (n.calls >= cityAt) {
        cities++;
        pg.appendChild(svgEl('path', { d: 'M' + (v[0] - 7) + ',' + (v[1] + 5) + ' v-7 l3.5,-3.5 l3.5,3.5 v-1 h7 v8 z',
          fill: '#f4f4f4', stroke: '#1b1b1b', 'stroke-width': 1.2, 'stroke-linejoin': 'round' }));
      } else {
        settlements++;
        pg.appendChild(svgEl('path', { d: 'M' + (v[0] - 4.5) + ',' + (v[1] + 4) + ' v-5 l4.5,-4.5 l4.5,4.5 v5 z',
          fill: '#f4f4f4', stroke: '#1b1b1b', 'stroke-width': 1.2, 'stroke-linejoin': 'round' }));
      }
      pg.style.cursor = 'pointer';
      bindNode(pg, n, [['piece', n.calls >= cityAt ? 'city: top tenth of the chain by calls' : 'settlement']]);
      top.appendChild(pg);
    });
  });
  svg.appendChild(top);
  mountSVG(stage, svg);

  var tc = {};
  placed.forEach(function (g) { tc[g.terrain] = (tc[g.terrain] || 0) + 1; });
  var tparts = Object.keys(TERRAIN).filter(function (t) { return tc[t]; }).map(function (t) {
    return tc[t] + ' ' + TERRAIN[t].name + ' (' + TERRAIN[t].what + ')';
  }).join(', ');
  note(below, [
    'One hex per namespace, ', [String(placed.length)], ' of them, all the same size: the board ' +
    'game’s conceit is that the land is equal and what it yields is not. ', [center.ns],
    ' holds the centre because the rest of the chain imports it more than anything else (',
    [window.fmtNum(inbound[center.ns] || 0)], ' import edges from other namespaces); every slot ' +
    'after it, spiralling outward, went to the namespace with the most imports to and from its ' +
    'already-placed neighbours, so who sits next to whom is the import graph’s argument, and the ',
    [String(roadN)], ' roads on shared edges carry ', [String(roadFlow)], ' of the ', [String(totalFlow)],
    ' cross-namespace import edges. The rest run between hexes that are not neighbours and are not ' +
    'drawn. Terrain is the quantity in which a namespace holds its largest share of the chain: ',
    tparts, '. The number disc is the game’s convention laid over a real rank: namespaces ' +
    'ordered by calls in the last ', [S.window], ' get 6 and 8 (red, five pips) at the top and 2 ' +
    'and 12 at the bottom; a hex with no disc took no calls. Pieces are each namespace’s busiest ' +
    'called realms, up to six: ', [String(cities)], ' cities (the chain’s top tenth by calls) ' +
    'and ', [String(settlements)], ' settlements. The sea is the board’s frame and carries nothing.',
  ]);
  legend(below, Object.keys(TERRAIN).filter(function (t) { return tc[t]; }).map(function (t) {
    return [TERRAIN[t].fill, TERRAIN[t].name, '· ' + TERRAIN[t].what];
  }));
}

// terrainGlyphs scatters the tile's picture: wheat, sheep, trees, peaks,
// bricks, dunes. Seeded by the namespace so a tile looks the same on every
// load, kept out of the middle where the disc goes, and pure decoration: the
// fill colour is what carries the terrain, these only make it legible at a
// glance.
function terrainGlyphs(parent, g, c, R) {
  var rng = rngFrom('t' + g.ns);
  var spots = [];
  for (var tries = 0; spots.length < 7 && tries < 60; tries++) {
    var a = rng() * Math.PI * 2, rr = R * (0.38 + rng() * 0.38);
    var x = c[0] + Math.cos(a) * rr, y = c[1] + Math.sin(a) * rr * 0.92;
    if (Math.abs(y - (c[1] - 20)) < 9 && Math.abs(x - c[0]) < 30) continue;   // the name
    if (spots.some(function (s) { return Math.hypot(s[0] - x, s[1] - y) < 15; })) continue;
    spots.push([x, y]);
  }
  spots.forEach(function (s) {
    var x = s[0], y = s[1], gl;
    switch (g.terrain) {
      case 'forest':
        gl = svgEl('path', { d: 'M' + x + ',' + (y - 9) + ' l6,10 h-12 z M' + x + ',' + (y - 4) + ' l7,10 h-14 z',
          fill: '#1d4a29', stroke: '#173d21', 'stroke-width': 0.6 });
        break;
      case 'mountains':
        gl = svgEl('g');
        gl.appendChild(svgEl('path', { d: 'M' + (x - 10) + ',' + (y + 6) + ' l10,-15 l10,15 z', fill: '#5d626b' }));
        gl.appendChild(svgEl('path', { d: 'M' + (x - 3.4) + ',' + (y - 4) + ' l3.4,-5 l3.4,5 l-1.7,1 l-1.7,-1.4 l-1.7,1.4 z', fill: '#e8ecf0' }));
        break;
      case 'hills':
        gl = svgEl('g');
        [[0, 0], [7, 0], [3.5, -4]].forEach(function (o) {
          gl.appendChild(svgEl('rect', { x: x - 6 + o[0], y: y + o[1], width: 6.5, height: 3.6, fill: '#7d3418', stroke: '#e3a07a', 'stroke-width': 0.5 }));
        });
        break;
      case 'pasture':
        gl = svgEl('g');
        gl.appendChild(svgEl('ellipse', { cx: x, cy: y, rx: 5, ry: 3.4, fill: '#f6f6ee' }));
        gl.appendChild(svgEl('circle', { cx: x + 5, cy: y - 1.4, r: 1.8, fill: '#2b2b2b' }));
        break;
      case 'fields':
        gl = svgEl('g');
        for (var k = -1; k <= 1; k++) {
          gl.appendChild(svgEl('line', { x1: x + k * 3, y1: y + 5, x2: x + k * 3, y2: y - 4, stroke: '#8a6a1c', 'stroke-width': 1 }));
          gl.appendChild(svgEl('ellipse', { cx: x + k * 3, cy: y - 5, rx: 1.3, ry: 2.6, fill: '#f0d27a' }));
        }
        break;
      default:
        gl = svgEl('path', { d: 'M' + (x - 9) + ',' + y + ' q4.5,-5 9,0 t9,0', fill: 'none', stroke: '#b9a571', 'stroke-width': 1.4 });
    }
    parent.appendChild(gl);
  });
}

// =============================================================================
// 9. FRONTIER  -- a Travian world map, deployers as players
// =============================================================================
//
// The view about who built the chain and in what order. Ownership here is not
// the namespace but the creator address that signed the deploy, so a team that
// deploys under several namespaces is one player and a namespace someone else
// deployed into is theirs.
//
// Settlement follows the game: the first player holds (0|0), and each new
// player founds a capital at the next free tile of the spiral, one tile clear
// of anyone already there. Every later package that player deploys is a new
// village on the free tile nearest their capital. Deploy order alone produces
// the map, so the oldest players hold the middle and the newcomers the rim.

function drawFrontier(stage, below, d) {
  var met = METRICS[S.metric];
  var order = d.nodes.slice().sort(byDeploy);

  // Offsets sorted by distance, shared by the spiral and the nearest-free
  // search. Ties by a seeded angle so a ring fills in an order that looks
  // organic rather than in raster order.
  var RMAX = Math.ceil(Math.sqrt(order.length * 2.2)) + 6;
  var offs = [];
  for (var x = -RMAX; x <= RMAX; x++) for (var y = -RMAX; y <= RMAX; y++) {
    offs.push([x, y, Math.hypot(x, y) + (hash32('o' + x + ',' + y) % 1000) / 4000]);
  }
  offs.sort(function (a, b) { return a[2] - b[2]; });

  var tile = {}, players = {}, plist = [], fi = 0;
  var key = function (x, y) { return x + '|' + y; };
  var clear = function (x, y, who) {
    for (var dx = -1; dx <= 1; dx++) for (var dy = -1; dy <= 1; dy++) {
      var t = tile[key(x + dx, y + dy)];
      if (t && t.p !== who) return false;
    }
    return true;
  };
  order.forEach(function (n) {
    var who = n.creator || '?';
    var p = players[who];
    var spot = null, i;
    if (!p) {
      p = players[who] = { id: who, nodes: [], idx: plist.length, first: n };
      plist.push(p);
      for (; fi < offs.length; fi++) {
        var o = offs[fi];
        if (!tile[key(o[0], o[1])] && clear(o[0], o[1], who)) { spot = o; break; }
      }
      p.cap = [spot[0], spot[1]];
    } else {
      for (i = 0; i < offs.length; i++) {
        var tx = p.cap[0] + offs[i][0], ty = p.cap[1] + offs[i][1];
        if (!tile[key(tx, ty)]) { spot = [tx, ty]; break; }
      }
    }
    if (!spot) return;
    tile[key(spot[0], spot[1])] = { n: n, p: who, x: spot[0], y: spot[1], cap: !p.nodes.length };
    p.nodes.push(n);
  });

  // A player's name is the namespace they use most that is a name rather than
  // an address, which is how a reader knows them; failing that, the address.
  plist.forEach(function (p) {
    var c = {};
    p.nodes.forEach(function (n) { if (!/^g1[0-9a-z]{30,}$/.test(n.namespace || '')) c[n.namespace] = (c[n.namespace] || 0) + 1; });
    var best = Object.keys(c).sort(function (a, b) { return c[b] - c[a] || a.localeCompare(b); })[0];
    p.name = best || shortAddr(p.id);
    p.hue = nsHue(p.id);
  });
  // Two addresses deploying into one namespace would otherwise be two players
  // with one name, so a repeated name carries its address's tail.
  // The biggest of them keeps the bare name, because that is the one a reader
  // means by it.
  var owner = {};
  plist.forEach(function (p) { var o = owner[p.name]; if (!o || p.nodes.length > o.nodes.length) owner[p.name] = p; });
  plist.forEach(function (p) { if (owner[p.name] !== p) p.name += '·' + p.id.slice(-4); });

  var tiles = Object.keys(tile).map(function (k) { return tile[k]; });
  var bx0 = Infinity, bx1 = -Infinity, by0 = Infinity, by1 = -Infinity;
  tiles.forEach(function (t) { bx0 = Math.min(bx0, t.x); bx1 = Math.max(bx1, t.x); by0 = Math.min(by0, t.y); by1 = Math.max(by1, t.y); });
  bx0 -= 2; bx1 += 2; by0 -= 2; by1 += 2;

  var T = 22;
  // Travian's y grows north; the screen's grows south, hence the minus.
  var sx = function (x) { return x * T; }, sy = function (y) { return -y * T; };
  var vx = sx(bx0) - T, vy = sy(by1) - T, vw = (bx1 - bx0 + 2) * T + T, vh = (by1 - by0 + 2) * T + T;
  var svg = svgEl('svg', { viewBox: vx + ' ' + vy + ' ' + vw + ' ' + vh, preserveAspectRatio: 'xMidYMid meet',
    style: 'max-height:82vh' });

  // Wilderness first: every tile in the frame nobody settled. The mix of
  // grass, woods, hills and water is seeded noise and carries nothing; it is
  // there so settled land reads as settled against something.
  var wild = svgEl('g');
  for (var wx = bx0; wx <= bx1; wx++) for (var wy = by0; wy <= by1; wy++) {
    var X = sx(wx) - T / 2, Y = sy(wy) - T / 2;
    var h = hash32('w' + wx + '|' + wy) % 100;
    var settled = !!tile[key(wx, wy)];
    wild.appendChild(svgEl('rect', { x: X, y: Y, width: T, height: T,
      fill: settled ? '#1f2a1a' : (h < 7 ? '#17283a' : h < 22 ? '#1a2a18' : h < 30 ? '#2a2a1c' : '#1f2a1a'),
      stroke: '#141c11', 'stroke-width': 0.6 }));
    if (!settled && h >= 7 && h < 22) {
      wild.appendChild(svgEl('path', { d: 'M' + (X + T / 2) + ',' + (Y + 5) + ' l4,8 h-8 z', fill: '#24401f' }));
    } else if (!settled && h >= 22 && h < 30) {
      wild.appendChild(svgEl('path', { d: 'M' + (X + 4) + ',' + (Y + T - 6) + ' l6,-8 l6,8 z', fill: '#3b3a28' }));
    }
  }
  svg.appendChild(wild);

  // Territory: each settled tile tinted with its player, and a border on any
  // edge where the neighbour is someone else or no one.
  var terr = svgEl('g'), border = svgEl('g');
  tiles.forEach(function (t) {
    var p = players[t.p], X = sx(t.x) - T / 2, Y = sy(t.y) - T / 2;
    terr.appendChild(svgEl('rect', { x: X, y: Y, width: T, height: T, fill: 'hsl(' + p.hue.toFixed(0) + ',45%,22%)', opacity: 0.85 }));
    [[1, 0, X + T, Y, X + T, Y + T], [-1, 0, X, Y, X, Y + T], [0, 1, X, Y, X + T, Y], [0, -1, X, Y + T, X + T, Y + T]]
      .forEach(function (e) {
        var nb = tile[key(t.x + e[0], t.y + e[1])];
        if (nb && nb.p === t.p) return;
        border.appendChild(svgEl('line', { x1: e[2], y1: e[3], x2: e[4], y2: e[5],
          stroke: 'hsl(' + p.hue.toFixed(0) + ',70%,62%)', 'stroke-width': 1.4, 'stroke-linecap': 'square' }));
      });
  });
  svg.appendChild(terr);
  svg.appendChild(border);

  // The size of a village is the metric, in four tiers like the game's
  // population icons. Tiers are quartiles of the packages that have any of
  // the metric at all, so tier 4 always means "top quarter of what moved".
  var vals = d.nodes.map(function (n) { return met.get(n) || 0; }).filter(function (v) { return v > 0; })
    .sort(function (a, b) { return a - b; });
  var q = function (f) { return vals.length ? vals[Math.min(vals.length - 1, Math.floor(f * vals.length))] : Infinity; };
  var q1 = q(0.25), q2 = q(0.5), q3 = q(0.75);
  var tier = function (n) {
    var v = met.get(n) || 0;
    if (!v || (!n.is_realm && !met.pure)) return 0;
    return v >= q3 ? 4 : v >= q2 ? 3 : v >= q1 ? 2 : 1;
  };

  var vil = svgEl('g');
  var counts = { village: 0, oasis: 0, ruin: 0 };
  tiles.forEach(function (t) {
    var n = t.n, p = players[t.p], cx = sx(t.x), cy = sy(t.y);
    var g = svgEl('g');
    g.appendChild(svgEl('rect', { x: cx - T / 2, y: cy - T / 2, width: T, height: T, fill: 'transparent' }));
    var col = 'hsl(' + p.hue.toFixed(0) + ',55%,64%)';
    if (n.parked) {
      counts.ruin++;
      g.appendChild(svgEl('path', { d: 'M' + (cx - 6) + ',' + (cy + 5) + ' v-6 l2,2 l2,-4 v8 M' + (cx + 1) + ',' + (cy + 5) + ' v-4 l3,-2 v6',
        fill: 'none', stroke: '#9a8f7a', 'stroke-width': 1.3 }));
    } else if (!n.is_realm) {
      // A pure package is an oasis: a resource the villages around it draw on,
      // which is exactly what an imported library is. A bigger pool for more
      // importers, nothing for none.
      counts.oasis++;
      var r = 2.2 + Math.min(4.5, lg(n.importers) * 2.2);
      g.appendChild(svgEl('ellipse', { cx: cx, cy: cy + 1.5, rx: r + 1, ry: r * 0.7 + 0.6, fill: '#2f6f9c' }));
      g.appendChild(svgEl('circle', { cx: cx - r - 0.5, cy: cy - 2.5, r: 2.4, fill: '#3e7d3a' }));
    } else {
      counts.village++;
      var tr = tier(n), houses = [[0, 0]];
      if (tr >= 2) houses.push([-4.5, 3]);
      if (tr >= 3) houses.push([4.5, 3]);
      if (tr >= 4) houses.push([0, -4.5]);
      var s = tr >= 4 ? 1.2 : 1;
      houses.forEach(function (o) {
        var hx2 = cx + o[0], hy = cy + o[1];
        g.appendChild(svgEl('rect', { x: hx2 - 3 * s, y: hy - 1 * s, width: 6 * s, height: 4.2 * s,
          fill: n.calls > 0 ? '#e9d9a8' : '#a99e86', stroke: '#1a1a1a', 'stroke-width': 0.5 }));
        g.appendChild(svgEl('path', { d: 'M' + (hx2 - 3.8 * s) + ',' + (hy - 0.8 * s) + ' l' + (3.8 * s) + ',' + (-3.4 * s) +
          ' l' + (3.8 * s) + ',' + (3.4 * s) + ' z', fill: n.calls > 0 ? '#c4532f' : '#6e4a3a', stroke: '#1a1a1a', 'stroke-width': 0.5 }));
      });
      if (n.calls > 0) g.appendChild(svgEl('circle', { cx: cx + 7, cy: cy - 7, r: 1.6, fill: '#ffd93d' }));
    }
    if (t.cap) {
      // The capital's banner, in the player's colour.
      g.appendChild(svgEl('line', { x1: cx + 6, y1: cy - 1, x2: cx + 6, y2: cy - 11, stroke: '#ddd', 'stroke-width': 0.9 }));
      g.appendChild(svgEl('path', { d: 'M' + (cx + 6) + ',' + (cy - 11) + ' h6 l-2,2 l2,2 h-6 z', fill: col }));
    }
    g.setAttribute('data-tile', t.x + '|' + t.y);
    bindNode(g, n, [['tile', '(' + t.x + '|' + t.y + ')'], ['player', p.name + ' · ' + shortAddr(p.id)],
      ['', t.cap ? 'capital: their first deploy' : 'village #' + (p.nodes.indexOf(n) + 1) + ' of ' + p.nodes.length]]);
    vil.appendChild(g);
  });
  svg.appendChild(vil);

  // Axes: the origin cross and a coordinate every ten tiles, as on the game's
  // map. Drawn last and faint so they sit above the land without hiding it.
  var ax = svgEl('g');
  ax.appendChild(svgEl('line', { x1: sx(bx0) - T / 2, y1: 0, x2: sx(bx1) + T / 2, y2: 0, stroke: 'rgba(255,255,255,.12)', 'stroke-width': 1 }));
  ax.appendChild(svgEl('line', { x1: 0, y1: sy(by0) + T / 2, x2: 0, y2: sy(by1) - T / 2, stroke: 'rgba(255,255,255,.12)', 'stroke-width': 1 }));
  for (var cxv = Math.ceil(bx0 / 10) * 10; cxv <= bx1; cxv += 10) {
    var t1 = svgEl('text', { x: sx(cxv), y: sy(by1) - T / 2 + 9, 'text-anchor': 'middle', fill: 'rgba(255,255,255,.4)', 'font-size': 8, 'font-family': 'var(--mono)' });
    t1.textContent = String(cxv); ax.appendChild(t1);
  }
  for (var cyv = Math.ceil(by0 / 10) * 10; cyv <= by1; cyv += 10) {
    var t2 = svgEl('text', { x: sx(bx0) - T / 2 + 3, y: sy(cyv) + 3, fill: 'rgba(255,255,255,.4)', 'font-size': 8, 'font-family': 'var(--mono)' });
    t2.textContent = String(cyv); ax.appendChild(t2);
  }
  svg.appendChild(ax);

  // Player names at their capitals, for players big enough to have room for
  // one: a name wider than the territory under it lands on a neighbour's and
  // names the wrong player. Every player is named on hover.
  var names = svgEl('g');
  var boxes = [];
  plist.slice().sort(function (a, b) { return b.nodes.length - a.nodes.length; }).forEach(function (p, i) {
    if (p.nodes.length < 5 && i >= 8) return;
    // Biggest first, and a name that would overlap one already placed is
    // left to the tooltip.
    var txt = p.name + ' · ' + p.nodes.length;
    var bw = txt.length * 6.2, bx = sx(p.cap[0]) - bw / 2, by = sy(p.cap[1]) + T * 0.95 - 9;
    if (boxes.some(function (b) { return bx < b[0] + b[2] && b[0] < bx + bw && by < b[1] + 12 && b[1] < by + 12; })) return;
    boxes.push([bx, by, bw]);
    var t = svgEl('text', { x: sx(p.cap[0]), y: sy(p.cap[1]) + T * 0.95, 'text-anchor': 'middle',
      fill: 'hsl(' + p.hue.toFixed(0) + ',80%,78%)', 'font-size': 10, 'font-family': 'var(--mono)',
      'paint-order': 'stroke', stroke: 'rgba(0,0,0,.85)', 'stroke-width': 3, 'pointer-events': 'none' });
    t.textContent = txt;
    names.appendChild(t);
  });
  svg.appendChild(names);
  mountSVG(stage, svg);

  // The caption counts packages, not tiles, so a package that failed to find
  // a tile (or landed on one already taken) shows as a mismatch with the map
  // rather than vanishing from both.
  var first = plist[0];
  var big = plist.slice().sort(function (a, b) { return b.nodes.length - a.nodes.length; })[0];
  note(below, [
    'A world map in which the players are the ', [String(plist.length)], ' addresses that deployed ' +
    'code, not the namespaces it went into, and every one of the ', [String(order.length)],
    ' packages is a tile they settled. Placement is deploy order and nothing else: the first ' +
    'deployer, ', [first.name], ', holds (0|0), each new player founds a capital (the flag) at the ' +
    'next free tile spiralling out from the origin with a tile of clearance from everyone else, and ' +
    'each later deploy becomes a village on the free tile nearest that capital. So the middle is the ' +
    'chain’s founders and the rim its newest arrivals, and a territory’s size is how much ' +
    'that player deployed: ', [big.name], ' holds ', [String(big.nodes.length)], '. ',
    [String(counts.village)], ' villages are realms, sized in four tiers by ', [met.label],
    ' (quartiles of the packages that have any), with warm roofs and a gold light if called in the last ',
    [S.window], '. ', [String(counts.oasis)], ' oases are pure packages, the pool sized by how many ' +
    'packages import them, because a library is a resource the villages around it draw on. ',
    [String(counts.ruin)], ' ruins are parked packages. Which free tile wins a tie, and the wilderness ' +
    'between territories, are seeded and carry nothing.',
  ]);
  legend(below, [
    ['#c4532f', 'village', '· a realm, more houses for more ' + met.label],
    ['#2f6f9c', 'oasis', '· a pure package, pool by importers'],
    ['#ffd93d', 'gold light', '· called in the window'],
    ['', 'flag', '· the player’s capital, their first deploy'],
  ]);
}

// =============================================================================
// 10. OLD TOWN  -- a walled town, grown outward ring by ring
// =============================================================================
//
// The view about how the whole chain grew, rather than one namespace at a time
// as the orbits draw it. European towns grew this way: the oldest streets
// inside the first wall around the market, a new wall for each age of growth,
// the newest houses outside all of them.
//
// Every package is one house, laid down in deploy order on a sunflower spiral:
// the n-th house sits at a radius that keeps the town's density constant, so
// the area inside each wall is exactly proportional to how much was deployed
// in that era, and a wall that encloses a fat ring marks a busy age.

function drawOldTown(stage, below, d) {
  var met = METRICS[S.metric];
  var order = d.nodes.slice().sort(byDeploy);
  var N = order.length;

  // Eras: deploy days merged until each holds at least a tenth of the chain,
  // so there are walls enough to show growth and not one per quiet Tuesday.
  var eras = [], cur = null;
  order.forEach(function (n, i) {
    var day = (n.deployed_at || '').slice(0, 10) || 'unknown';
    if (!cur || (cur.n >= N / 10 && day !== cur.last)) {
      cur = { from: day, last: day, start: i, n: 0 };
      eras.push(cur);
    }
    cur.n++; cur.last = day; cur.end = i + 1;
  });
  // A trailing sliver is folded into the era before it, or the outermost wall
  // would enclose a handful of houses and read as a district.
  if (eras.length > 1 && eras[eras.length - 1].n < N / 25) {
    var tail = eras.pop(), prevE = eras[eras.length - 1];
    prevE.last = tail.last; prevE.end = tail.end; prevE.n += tail.n;
  }

  var R0 = 50, R = 460;
  var rAt = function (i) { return Math.sqrt(R0 * R0 + (R * R - R0 * R0) * (i / N)); };
  var maxM = Math.max(1, d.nodes.reduce(function (m, n) { return Math.max(m, met.get(n) || 0); }, 0));
  var denom = Math.max(1e-9, sc(maxM));
  var ROADS = [0, Math.PI / 2, Math.PI, Math.PI * 1.5].map(function (a) { return a - Math.PI / 4; });

  var houses = order.map(function (n, i) {
    var r = rAt(i + 0.5), a = i * 2.399963 + 0.3;
    // Off the four high streets: a house that would stand on one is slid
    // sideways off it. That shifts it by less than one house width, so the
    // deploy-order radius, the only thing position means, is untouched.
    ROADS.forEach(function (ra) {
      var da = Math.atan2(Math.sin(a - ra), Math.cos(a - ra));
      var lim = 13 / r;
      if (Math.abs(da) < lim) a = ra + (da < 0 ? -lim : lim);
    });
    var v = met.get(n) || 0;
    var rises = n.is_realm || met.pure;
    var f = rises && v > 0 ? sc(v) / denom : 0;
    return { n: n, x: Math.cos(a) * r, y: Math.sin(a) * r, a: a, w: 14 + f * 12, h: 9.5 + f * 5 };
  });

  var ext = R + 46;
  var svg = svgEl('svg', { viewBox: (-ext) + ' ' + (-ext) + ' ' + (ext * 2) + ' ' + (ext * 2),
    preserveAspectRatio: 'xMidYMid meet', style: 'max-height:84vh' });

  // Farmland outside the last wall, in seeded strips. Decoration: it says
  // "this is the edge of town" and nothing else.
  var fields = svgEl('g');
  for (var k = 0; k < 28; k++) {
    var rng = rngFrom('field' + k);
    var a0 = k / 28 * Math.PI * 2, a1 = a0 + Math.PI * 2 / 28 * 0.9;
    var r1 = R + 10, r2 = R + 40;
    fields.appendChild(svgEl('path', { d: 'M' + Math.cos(a0) * r1 + ',' + Math.sin(a0) * r1 +
      ' A' + r1 + ',' + r1 + ' 0 0 1 ' + Math.cos(a1) * r1 + ',' + Math.sin(a1) * r1 +
      ' L' + Math.cos(a1) * r2 + ',' + Math.sin(a1) * r2 +
      ' A' + r2 + ',' + r2 + ' 0 0 0 ' + Math.cos(a0) * r2 + ',' + Math.sin(a0) * r2 + ' Z',
      fill: 'hsl(' + (60 + rng() * 40).toFixed(0) + ',' + (18 + rng() * 14).toFixed(0) + '%,' + (13 + rng() * 6).toFixed(0) + '%)' }));
  }
  svg.appendChild(fields);

  svg.appendChild(svgEl('circle', { cx: 0, cy: 0, r: R + 8, fill: '#1b1814' }));
  // High streets, out through every gate.
  ROADS.forEach(function (a) {
    svg.appendChild(svgEl('line', { x1: Math.cos(a) * R0, y1: Math.sin(a) * R0, x2: Math.cos(a) * (R + 44),
      y2: Math.sin(a) * (R + 44), stroke: '#3d362c', 'stroke-width': 16, 'stroke-linecap': 'round' }));
  });
  // The market square, and its well.
  svg.appendChild(svgEl('circle', { cx: 0, cy: 0, r: R0 - 4, fill: '#3d362c', stroke: '#4a4236', 'stroke-width': 2 }));
  svg.appendChild(svgEl('circle', { cx: 0, cy: 0, r: 9, fill: '#2a4f6e', stroke: '#8c8270', 'stroke-width': 2.5 }));

  // Walls at every era boundary, broken at the four gates, with a tower every
  // so often and the era's dates on the wall itself.
  var walls = svgEl('g');
  eras.forEach(function (e, ei) {
    var rw = rAt(e.end) + 4;
    var gate = 16 / rw;
    ROADS.forEach(function (ra, ri) {
      var a0 = ra + gate, a1 = (ROADS[(ri + 1) % 4] + (ri === 3 ? Math.PI * 2 : 0)) - gate;
      walls.appendChild(svgEl('path', { d: 'M' + Math.cos(a0) * rw + ',' + Math.sin(a0) * rw +
        ' A' + rw + ',' + rw + ' 0 0 1 ' + Math.cos(a1) * rw + ',' + Math.sin(a1) * rw,
        fill: 'none', stroke: '#8c8270', 'stroke-width': 4.5, 'stroke-linecap': 'butt', 'class': 'carto-wall',
        'data-era': String(ei) }));
      // Gate towers either side of the gap.
      [ra + gate, ra - gate].forEach(function (ta) {
        walls.appendChild(svgEl('rect', { x: Math.cos(ta) * rw - 4, y: Math.sin(ta) * rw - 4, width: 8, height: 8,
          fill: '#a49a86', stroke: '#5a5244', 'stroke-width': 1 }));
      });
    });
    var towers = Math.max(4, Math.round(rw * Math.PI * 2 / 120));
    for (var t = 0; t < towers; t++) {
      var ta = (t + 0.5) / towers * Math.PI * 2;
      if (ROADS.some(function (ra) { return Math.abs(Math.atan2(Math.sin(ta - ra), Math.cos(ta - ra))) < gate * 2.2; })) continue;
      walls.appendChild(svgEl('circle', { cx: Math.cos(ta) * rw, cy: Math.sin(ta) * rw, r: 4.2,
        fill: '#a49a86', stroke: '#5a5244', 'stroke-width': 1 }));
    }
    // The wall's dates, at its top, on a dark plate so a house below cannot
    // swallow them.
    var lab = svgEl('text', { x: 0, y: -rw + 3.5, 'text-anchor': 'middle', 'font-size': 10.5, fill: '#e9dfc9',
      'font-family': 'var(--mono)', 'paint-order': 'stroke', stroke: '#1b1814', 'stroke-width': 4 });
    var span = e.from === e.last ? e.from.slice(5) : e.from.slice(5) + '–' + e.last.slice(5);
    lab.textContent = (ei === 0 ? 'first wall ' : '') + span + ' · ' + e.n;
    e.label = span;
    walls.appendChild(lab);
  });

  var town = svgEl('g'), lit = 0;
  houses.forEach(function (hs, i) {
    var n = hs.n, g = svgEl('g');
    var deg = hs.a * 180 / Math.PI + 90;
    g.setAttribute('transform', 'translate(' + hs.x.toFixed(1) + ',' + hs.y.toFixed(1) + ') rotate(' + deg.toFixed(1) + ')');
    var roof = n.parked ? 'none' : n.is_realm ? (n.calls > 0 ? '#c8613a' : '#8e4c34') : '#5d6672';
    g.appendChild(svgEl('rect', { x: -hs.w / 2, y: -hs.h / 2, width: hs.w, height: hs.h, fill: roof,
      stroke: n.parked ? 'var(--amber)' : '#16130f', 'stroke-width': n.parked ? 1 : 0.8,
      'stroke-dasharray': n.parked ? '2 2' : null }));
    // The ridge, which is what makes a rectangle a roof from above.
    g.appendChild(svgEl('line', { x1: -hs.w / 2 + 1, y1: 0, x2: hs.w / 2 - 1, y2: 0,
      stroke: 'rgba(0,0,0,.35)', 'stroke-width': 0.9 }));
    if (n.calls > 0) {
      lit++;
      g.appendChild(svgEl('rect', { x: -1.3, y: hs.h / 2 - 0.6, width: 2.6, height: 2.6, fill: '#ffd93d' }));
    }
    var era = eras.filter(function (e) { return i >= e.start && i < e.end; })[0];
    g.setAttribute('data-order', String(i));
    bindNode(g, n, [['house', '#' + (i + 1) + ' of ' + N + ' in deploy order'], ['era', era ? era.label : '?']]);
    town.appendChild(g);
  });
  svg.appendChild(town);
  svg.appendChild(walls);
  mountSVG(stage, svg);

  var fat = eras.slice().sort(function (a, b) { return b.n - a.n; })[0];
  note(below, [
    'The chain as a town that grew outward. All ', [String(N)], ' packages are houses laid down in ' +
    'deploy order on a sunflower spiral from the market square, at radii chosen so the town’s ' +
    'density is constant: the area between two walls is exactly proportional to how much was ' +
    'deployed in that era. There are ', [String(eras.length)], ' walls, one per era, an era being ' +
    'consecutive deploy days merged until it holds a tenth of the chain; each wall is labelled with ' +
    'its dates and how many houses it closed in. The busiest era was ', [fat.label], ', with ',
    [String(fat.n)], '. House size is ', [met.label], ' on a ', [S.scale], ' scale (pure packages stay ' +
    'small under a metric they do not have, the same rule as the city); a terracotta roof is a ' +
    'realm, slate is a pure package, a dashed outline is parked, and ', [String(lit)],
    ' houses show a lit window because they were called in the last ', [S.window], '. Angle around ' +
    'the square is the golden angle and carries nothing, the four high streets and their gates are ' +
    'there to be streets, and the farmland outside is decoration.',
  ]);
  legend(below, [
    ['#c8613a', 'terracotta', '· realm, called'],
    ['#8e4c34', 'dark roof', '· realm, not called'],
    ['#5d6672', 'slate', '· pure package'],
    ['#8c8270', 'wall', '· the edge of one era of deploys'],
  ]);
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
