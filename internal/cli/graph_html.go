// The self-contained HTML viewer `mem graph --format html` emits. Everything
// the file needs — CSS, the force-directed layout, the interaction layer and
// the graph payload itself — is inline: no CDN, no fonts, no images, no
// requests of any kind, so the artifact renders offline from file://.
// Generated graphs carry private memory metadata and must never be committed.
package cli

import (
	"encoding/json"
	"fmt"
	"html"
	"sort"
	"strings"
)

// graphLayerColors is the viewer palette: one distinguishable hue per layer
// on the neutral dark background (plus the grey reserved for dangling
// ghosts). The same values appear in the inline JS below.
var graphLayerColors = map[string]string{
	"rule":      "#ff7b72",
	"project":   "#ffa657",
	"lesson":    "#e3b341",
	"decision":  "#7ee787",
	"handoff":   "#79c0ff",
	"knowledge": "#d2a8ff",
}

const graphOtherColor = "#8b949e"    // unknown layers
const graphDanglingColor = "#6e7681" // ghost nodes

// graphEdgeOrder is the legend/edge-kind display order.
var graphEdgeOrder = []string{
	"supersedes", "evidence", "body-ref", "promoted-from", "compacted-into",
	"related-scope", "related-tags",
}

// renderGraphHTML builds the whole viewer document for one payload.
func renderGraphHTML(p *graphPayload) string {
	layerCounts := map[string]int{}
	var layerOrder []string
	for _, n := range p.Nodes {
		if n.Dangling {
			continue
		}
		if _, seen := layerCounts[n.Layer]; !seen {
			layerOrder = append(layerOrder, n.Layer)
		}
		layerCounts[n.Layer]++
	}
	sort.Strings(layerOrder)

	var legend strings.Builder
	for _, l := range layerOrder {
		color := graphLayerColors[l]
		if color == "" {
			color = graphOtherColor
		}
		fmt.Fprintf(&legend,
			`<label class="legend-item"><input type="checkbox" class="layer-toggle" data-layer=%q checked>`+
				`<span class="swatch" style="background:%s"></span>%s <span class="muted">(%d)</span></label>`,
			l, color, html.EscapeString(l), layerCounts[l])
	}
	if p.Counts.Dangling > 0 {
		fmt.Fprintf(&legend,
			`<span class="legend-item"><span class="swatch ghost"></span>dangling <span class="muted">(%d)</span></span>`,
			p.Counts.Dangling)
	}

	edgeKinds := map[string]bool{}
	for _, e := range p.Edges {
		edgeKinds[e.Kind] = true
	}
	var edgeLegend strings.Builder
	for _, k := range graphEdgeOrder {
		if !edgeKinds[k] {
			continue
		}
		fmt.Fprintf(&edgeLegend, `<span class="legend-item"><span class="edge-sw edge-%s"></span>%s</span>`, k, k)
	}

	// Text fallback: counts plus the top 20 highest-degree nodes, so the file
	// stays useful without JavaScript.
	top := append([]graphNode(nil), p.Nodes...)
	sort.SliceStable(top, func(i, j int) bool {
		if top[i].Degree != top[j].Degree {
			return top[i].Degree > top[j].Degree
		}
		return top[i].ID < top[j].ID
	})
	if len(top) > 20 {
		top = top[:20]
	}
	var items strings.Builder
	for _, n := range top {
		fmt.Fprintf(&items, "<li><code>%s</code> — %s/%s — %s (degree %d)</li>\n",
			html.EscapeString(n.ID), html.EscapeString(n.Layer), html.EscapeString(n.Scope),
			html.EscapeString(n.Title), n.Degree)
	}
	if len(top) == 0 {
		items.WriteString("<li>(no nodes)</li>\n")
	}

	// The payload is marshalled with HTML escaping on, so no '<' can ever
	// reach the document and break out of the script element.
	payload, err := json.Marshal(p)
	if err != nil {
		payload = []byte("{}")
	}

	r := strings.NewReplacer(
		"__GENERATED__", html.EscapeString(p.GeneratedAt),
		"__RECORDS__", fmt.Sprint(p.Counts.Records),
		"__EDGES__", fmt.Sprint(p.Counts.Edges),
		"__DANGLING__", fmt.Sprint(p.Counts.Dangling),
		"__LEGEND_LAYERS__", legend.String(),
		"__LEGEND_EDGES__", edgeLegend.String(),
		"__FALLBACK_ITEMS__", items.String(),
		"__PAYLOAD__", string(payload),
	)
	return r.Replace(graphHTMLTemplate)
}

// graphHTMLTemplate is the viewer skeleton. The tokens __X__ are filled by
// renderGraphHTML; the payload lands in the JSON script block the JS parses.
const graphHTMLTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>mem graph — memory connections</title>
<!--
  mem graph viewer (self-contained, offline, no external requests)
  generated: __GENERATED__
  records: __RECORDS__ | edges: __EDGES__ | dangling: __DANGLING__
  WARNING: contains private memory metadata — do not commit or share.
-->
<style>
  :root {
    --bg: #0d1117; --panel: #161b22; --border: #30363d;
    --text: #e6edf3; --muted: #8b949e; --accent: #58a6ff;
    color-scheme: dark;
  }
  * { box-sizing: border-box; }
  html, body { margin: 0; height: 100%; }
  body {
    background: var(--bg); color: var(--text);
    font: 14px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
    display: flex; flex-direction: column; overflow: hidden;
  }
  header { padding: 8px 14px; background: var(--panel); border-bottom: 1px solid var(--border); }
  .row { display: flex; flex-wrap: wrap; align-items: center; gap: 10px 16px; }
  .brand { font-weight: 700; letter-spacing: .02em; }
  .summary { color: var(--muted); }
  .summary b { color: var(--text); font-weight: 600; }
  input[type="search"] {
    background: var(--bg); color: var(--text); border: 1px solid var(--border);
    border-radius: 6px; padding: 4px 8px; width: 230px; outline: none;
  }
  input[type="search"]:focus { border-color: var(--accent); }
  button {
    background: #21262d; color: var(--text); border: 1px solid var(--border);
    border-radius: 6px; padding: 4px 10px; cursor: pointer;
  }
  button:hover { border-color: var(--accent); }
  label { user-select: none; }
  #match-count { min-width: 90px; }
  .legend-item { display: inline-flex; align-items: center; gap: 5px; margin-right: 12px; white-space: nowrap; }
  .swatch { width: 10px; height: 10px; border-radius: 50%; display: inline-block; }
  .swatch.ghost { background: transparent !important; border: 1.5px dashed #6e7681; }
  .edge-sw { width: 18px; height: 0; display: inline-block; margin-right: 2px; vertical-align: middle; }
  .edge-supersedes { border-top: 2px solid #ff7b72; }
  .edge-evidence { border-top: 2px solid #7ee787; }
  .edge-body-ref { border-top: 1px solid #8b949e; }
  .edge-promoted-from { border-top: 2px dashed #ffa657; }
  .edge-compacted-into { border-top: 2px dashed #d2a8ff; }
  .edge-related-scope, .edge-related-tags { border-top: 2px dotted #484f58; }
  .muted { color: var(--muted); }
  main { flex: 1; display: flex; min-height: 0; position: relative; }
  #canvas { flex: 1; display: block; cursor: grab; touch-action: none; }
  #canvas.dragging { cursor: grabbing; }
  #tooltip {
    position: fixed; pointer-events: none; z-index: 10;
    background: #010409; border: 1px solid var(--border); border-radius: 6px;
    padding: 6px 9px; max-width: 420px; font-size: 12px; line-height: 1.4;
    box-shadow: 0 4px 14px rgba(1,4,9,.6);
  }
  #tooltip code { color: var(--accent); }
  #panel {
    width: 340px; overflow-y: auto; background: var(--panel);
    border-left: 1px solid var(--border); padding: 12px 14px;
  }
  #panel h2 { margin: 0 0 6px; font-size: 14px; word-break: break-all; }
  #panel dl { display: grid; grid-template-columns: 84px 1fr; gap: 2px 10px; margin: 8px 0; }
  #panel dt { color: var(--muted); }
  #panel dd { margin: 0; word-break: break-all; }
  #panel h3 { font-size: 12px; text-transform: uppercase; letter-spacing: .06em;
    color: var(--muted); margin: 14px 0 4px; border-bottom: 1px solid var(--border); padding-bottom: 3px; }
  #panel ul { list-style: none; margin: 0; padding: 0; }
  #panel li { margin: 2px 0; }
  #panel .edge-btn {
    background: none; border: none; padding: 2px 4px; margin: 0;
    text-align: left; color: var(--text); font: inherit; cursor: pointer; border-radius: 4px;
  }
  #panel .edge-btn:hover { background: #21262d; color: var(--accent); }
  #panel .edge-btn .kind { color: var(--muted); margin-right: 6px; }
  #fallback { margin: 0; padding: 6px 14px; background: var(--panel); border-top: 1px solid var(--border); }
  #fallback summary { cursor: pointer; color: var(--muted); }
  #fallback ol { margin: 6px 0 4px; padding-left: 22px; }
  code { font: 12px/1.4 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
</style>
</head>
<body>
<header>
  <div class="row">
    <span class="brand">mem graph</span>
    <span class="summary"><b>__RECORDS__</b> records · <b>__EDGES__</b> edges · <b>__DANGLING__</b> dangling · generated __GENERATED__</span>
    <input id="search" type="search" placeholder="filter by id or title…" aria-label="Filter nodes by id or title">
    <span id="match-count" class="muted"></span>
    <label><input type="checkbox" id="toggle-related"> related edges</label>
    <label><input type="checkbox" id="toggle-dangling" checked> dangling</label>
    <button id="reset" type="button">reset view</button>
  </div>
  <div class="row" style="margin-top:6px">
    <span id="legend-layers">__LEGEND_LAYERS__</span>
    <span id="legend-edges">__LEGEND_EDGES__</span>
  </div>
</header>
<main>
  <canvas id="canvas" aria-label="Memory record graph. Scroll to zoom, drag the background to pan, drag a node to rearrange it, click a node for its connections."></canvas>
  <aside id="panel" hidden></aside>
</main>
<div id="tooltip" hidden></div>
<details id="fallback">
  <summary>Text fallback — counts and top 20 nodes by degree (no JavaScript needed)</summary>
  <p class="muted">__RECORDS__ records · __EDGES__ edges · __DANGLING__ dangling · generated __GENERATED__</p>
  <ol>
__FALLBACK_ITEMS__  </ol>
</details>
<script type="application/json" id="graph-data">__PAYLOAD__</script>
<script>
'use strict';
(function () {
  var dataEl = document.getElementById('graph-data');
  var data;
  try { data = JSON.parse(dataEl.textContent); }
  catch (err) { data = { counts: { records: 0, edges: 0, dangling: 0 }, nodes: [], edges: [] }; }
  dataEl.textContent = '';

  var LAYER_COLORS = { rule: '#ff7b72', project: '#ffa657', lesson: '#e3b341',
    decision: '#7ee787', handoff: '#79c0ff', knowledge: '#d2a8ff' };
  var OTHER_COLOR = '#8b949e', DANGLING_COLOR = '#6e7681';
  var EDGE_STYLES = {
    'supersedes':     { color: '#ff7b72', width: 1.6, dash: null,    arrow: true },
    'evidence':       { color: '#7ee787', width: 1.2, dash: null,    arrow: true },
    'body-ref':       { color: '#8b949e', width: 0.7, dash: null,    arrow: false },
    'promoted-from':  { color: '#ffa657', width: 1.2, dash: [6, 3],  arrow: true },
    'compacted-into': { color: '#d2a8ff', width: 1.2, dash: [6, 3],  arrow: true },
    'related-scope':  { color: '#484f58', width: 0.7, dash: [2, 4],  arrow: false },
    'related-tags':   { color: '#484f58', width: 0.7, dash: [2, 4],  arrow: false }
  };
  function isRelated(kind) { return kind === 'related-scope' || kind === 'related-tags'; }

  var nodes = data.nodes.map(function (n) {
    return { id: n.id, layer: n.layer, scope: n.scope, status: n.status, title: n.title,
      file: n.file, decay: n.decay, degree: n.degree, dangling: !!n.dangling,
      x: 0, y: 0, vx: 0, vy: 0, pinned: false };
  });
  var byId = {};
  nodes.forEach(function (n) { byId[n.id] = n; });
  var edges = data.edges.filter(function (e) { return byId[e.from] && byId[e.to]; })
    .map(function (e) { return { from: e.from, to: e.to, kind: e.kind }; });
  var hasRelated = edges.some(function (e) { return isRelated(e.kind); });

  // Deterministic seeded layout start (same file, same picture).
  var seed = 20261113;
  function rnd() { seed = (seed * 1103515245 + 12345) % 2147483648; return seed / 2147483648; }
  var ringR = Math.max(140, 30 * Math.sqrt(nodes.length || 1));
  nodes.forEach(function (n, i) {
    var a = 2 * Math.PI * (i + rnd() * 0.4) / Math.max(1, nodes.length);
    n.x = Math.cos(a) * ringR * (0.5 + rnd() * 0.6);
    n.y = Math.sin(a) * ringR * (0.5 + rnd() * 0.6);
  });

  var canvas = document.getElementById('canvas');
  var ctx = canvas.getContext('2d');
  var dpr = window.devicePixelRatio || 1;
  var view = { k: 1, tx: 0, ty: 0 };   // screen = world * k + t
  var state = { query: '', layers: {}, showRelated: hasRelated, showDangling: true,
    selected: null, hover: null, matches: -1 };

  document.querySelectorAll('.layer-toggle').forEach(function (box) {
    state.layers[box.dataset.layer] = box.checked;
    box.addEventListener('change', function () { state.layers[box.dataset.layer] = box.checked; draw(); });
  });
  var toggleRelated = document.getElementById('toggle-related');
  toggleRelated.checked = hasRelated;
  toggleRelated.addEventListener('change', function () { state.showRelated = toggleRelated.checked; draw(); });
  var toggleDangling = document.getElementById('toggle-dangling');
  toggleDangling.addEventListener('change', function () { state.showDangling = toggleDangling.checked; draw(); });
  document.getElementById('reset').addEventListener('click', function () { fit(); alpha = 0.5; running = true; });

  function nodeVisible(n) {
    if (n.dangling) return state.showDangling;
    if (state.layers[n.layer] === false) return false;
    return true;
  }
  function edgeVisible(e) {
    if (isRelated(e.kind) && !state.showRelated) return false;
    return nodeVisible(byId[e.from]) && nodeVisible(byId[e.to]);
  }
  function matchesQuery(n) {
    if (!state.query) return true;
    var q = state.query.toLowerCase();
    return n.id.toLowerCase().indexOf(q) >= 0 ||
      (n.title || '').toLowerCase().indexOf(q) >= 0;
  }

  function radius(n) {
    var r = 4 + Math.sqrt(n.degree || 0) * 1.9;
    return Math.max(4, Math.min(18, r));
  }
  function nodeColor(n) {
    if (n.dangling) return DANGLING_COLOR;
    return LAYER_COLORS[n.layer] || OTHER_COLOR;
  }
  function nodeAlpha(n) {
    var a = 1;
    if (!n.dangling && n.decay < 0.3) a = 0.4;      // decayed records dim
    if (state.matches >= 0 && !matchesQuery(n)) a = 0.08; // search dims the rest
    return a;
  }

  // ---- force simulation (repulsion + springs + mild gravity) ----
  var alpha = 1, running = nodes.length > 0;
  function step() {
    var i, j, n, m, dx, dy, d2, f, e, a, b, rest;
    for (i = 0; i < nodes.length; i++) {
      n = nodes[i];
      for (j = i + 1; j < nodes.length; j++) {
        m = nodes[j];
        dx = n.x - m.x; dy = n.y - m.y;
        d2 = dx * dx + dy * dy;
        if (d2 < 0.01) { d2 = 0.01; }
        f = 1800 / d2;
        if (f > 12) f = 12;
        var d = Math.sqrt(d2);
        var ux = dx / d, uy = dy / d;
        n.vx += ux * f * alpha; n.vy += uy * f * alpha;
        m.vx -= ux * f * alpha; m.vy -= uy * f * alpha;
      }
    }
    for (i = 0; i < edges.length; i++) {
      e = edges[i];
      a = byId[e.from]; b = byId[e.to];
      rest = isRelated(e.kind) ? 150 : 70;
      dx = b.x - a.x; dy = b.y - a.y;
      d = Math.sqrt(dx * dx + dy * dy) || 1;
      f = (d - rest) * (isRelated(e.kind) ? 0.004 : 0.02) * alpha;
      var ux = dx / d, uy = dy / d;
      a.vx += ux * f; a.vy += uy * f;
      b.vx -= ux * f; b.vy -= uy * f;
    }
    for (i = 0; i < nodes.length; i++) {
      n = nodes[i];
      n.vx -= n.x * 0.002 * alpha;
      n.vy -= n.y * 0.002 * alpha;
      if (n.pinned) { n.vx = 0; n.vy = 0; continue; }
      n.vx *= 0.85; n.vy *= 0.85;
      n.x += Math.max(-30, Math.min(30, n.vx));
      n.y += Math.max(-30, Math.min(30, n.vy));
    }
    alpha *= 0.996;
    if (alpha < 0.02) running = false;
  }

  // ---- rendering ----
  function resize() {
    var w = canvas.clientWidth, h = canvas.clientHeight;
    canvas.width = Math.round(w * dpr); canvas.height = Math.round(h * dpr);
  }
  window.addEventListener('resize', function () { resize(); draw(); });

  function toScreen(x, y) { return { x: x * view.k + view.tx, y: y * view.k + view.ty }; }
  function toWorld(x, y) { return { x: (x - view.tx) / view.k, y: (y - view.ty) / view.k }; }

  function drawArrow(fromPt, toPt, style) {
    var ang = Math.atan2(toPt.y - fromPt.y, toPt.x - fromPt.x);
    var size = Math.max(5, 3 + style.width * 2);
    ctx.beginPath();
    ctx.moveTo(toPt.x, toPt.y);
    ctx.lineTo(toPt.x - size * Math.cos(ang - 0.45), toPt.y - size * Math.sin(ang - 0.45));
    ctx.lineTo(toPt.x - size * Math.cos(ang + 0.45), toPt.y - size * Math.sin(ang + 0.45));
    ctx.closePath();
    ctx.fillStyle = style.color;
    ctx.fill();
  }

  function draw() {
    resize();
    var w = canvas.width / dpr, h = canvas.height / dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = '#0d1117';
    ctx.fillRect(0, 0, w, h);
    if (!nodes.length) {
      ctx.fillStyle = '#8b949e';
      ctx.textAlign = 'center';
      ctx.fillText('no nodes in this graph (empty corpus or every record filtered out)', w / 2, h / 2);
      return;
    }

    // edges first, nodes on top
    edges.forEach(function (e) {
      if (!edgeVisible(e)) return;
      var a = byId[e.from], b = byId[e.to];
      var style = EDGE_STYLES[e.kind] || EDGE_STYLES['body-ref'];
      var pa = toScreen(a.x, a.y), pb = toScreen(b.x, b.y);
      var dim = (state.matches >= 0 && !(matchesQuery(a) || matchesQuery(b))) ? 0.12 : 0.85;
      ctx.strokeStyle = style.color;
      ctx.globalAlpha = dim;
      ctx.lineWidth = style.width;
      ctx.setLineDash(style.dash || []);
      ctx.beginPath();
      var ra = radius(a) * view.k, rb = radius(b) * view.k;
      var dx = pb.x - pa.x, dy = pb.y - pa.y, len = Math.sqrt(dx * dx + dy * dy) || 1;
      var ux = dx / len, uy = dy / len;
      ctx.moveTo(pa.x + ux * ra, pa.y + uy * ra);
      var endX = pb.x - ux * rb, endY = pb.y - uy * rb;
      if (style.arrow) { endX -= ux * 4; endY -= uy * 4; }
      ctx.lineTo(endX, endY);
      ctx.stroke();
      ctx.setLineDash([]);
      if (style.arrow) drawArrow(pa, { x: endX, y: endY }, style);
      ctx.globalAlpha = 1;
    });

    nodes.forEach(function (n) {
      if (!nodeVisible(n)) return;
      var p = toScreen(n.x, n.y);
      var r = radius(n) * view.k;
      var hl = n === state.hover || n === state.selected;
      ctx.globalAlpha = nodeAlpha(n);
      ctx.beginPath();
      ctx.arc(p.x, p.y, Math.max(2, r), 0, 2 * Math.PI);
      if (n.dangling) {
        ctx.fillStyle = '#161b22';
        ctx.fill();
        ctx.setLineDash([3, 3]);
        ctx.strokeStyle = DANGLING_COLOR;
        ctx.lineWidth = 1.4;
        ctx.stroke();
        ctx.setLineDash([]);
      } else {
        ctx.fillStyle = nodeColor(n);
        ctx.fill();
        if (hl) { ctx.strokeStyle = '#e6edf3'; ctx.lineWidth = 1.8; ctx.stroke(); }
      }
      // id label once zoomed in enough, or for the highlighted node
      if (view.k > 1.4 || hl) {
        ctx.globalAlpha = Math.min(1, nodeAlpha(n) + 0.2);
        ctx.fillStyle = hl ? '#e6edf3' : '#8b949e';
        ctx.textAlign = 'center';
        ctx.font = '11px ui-monospace, Menlo, Consolas, monospace';
        ctx.fillText(n.id, p.x, p.y + r + 12);
      }
      ctx.globalAlpha = 1;
    });
  }

  function frame() {
    if (running) step();
    draw();
    window.requestAnimationFrame(frame);
  }

  function fit() {
    if (!nodes.length) { view = { k: 1, tx: 0, ty: 0 }; return; }
    var minX = 1e9, minY = 1e9, maxX = -1e9, maxY = -1e9;
    nodes.forEach(function (n) {
      if (!nodeVisible(n)) return;
      if (n.x < minX) minX = n.x; if (n.x > maxX) maxX = n.x;
      if (n.y < minY) minY = n.y; if (n.y > maxY) maxY = n.y;
    });
    if (minX > maxX) { view = { k: 1, tx: 0, ty: 0 }; return; }
    var w = canvas.clientWidth || 800, h = canvas.clientHeight || 600;
    var pad = 60;
    var kx = (w - pad) / Math.max(1, maxX - minX);
    var ky = (h - pad) / Math.max(1, maxY - minY);
    view.k = Math.min(kx, ky, 2.5);
    view.tx = w / 2 - (minX + maxX) / 2 * view.k;
    view.ty = h / 2 - (minY + maxY) / 2 * view.k;
  }

  function centerOn(n) {
    var w = canvas.clientWidth || 800, h = canvas.clientHeight || 600;
    view.tx = w / 2 - n.x * view.k;
    view.ty = h / 2 - n.y * view.k;
  }

  // ---- pointer interaction: pan, zoom, node drag, hover, select ----
  var drag = null; // {node, moved} | {pan, sx, sy, tx, ty} | null
  function hitNode(mx, my) {
    var best = null, bestD = 1e9;
    nodes.forEach(function (n) {
      if (!nodeVisible(n)) return;
      var p = toScreen(n.x, n.y);
      var d = Math.sqrt((mx - p.x) * (mx - p.x) + (my - p.y) * (my - p.y));
      if (d < radius(n) * view.k + 6 && d < bestD) { best = n; bestD = d; }
    });
    return best;
  }
  function localXY(ev) {
    var rect = canvas.getBoundingClientRect();
    return { x: ev.clientX - rect.left, y: ev.clientY - rect.top };
  }

  canvas.addEventListener('pointerdown', function (ev) {
    canvas.setPointerCapture(ev.pointerId);
    canvas.classList.add('dragging');
    var p = localXY(ev);
    var n = hitNode(p.x, p.y);
    if (n) { n.pinned = true; drag = { node: n, moved: 0, sx: p.x, sy: p.y }; }
    else drag = { pan: true, sx: p.x, sy: p.y, tx: view.tx, ty: view.ty };
  });
  canvas.addEventListener('pointermove', function (ev) {
    var p = localXY(ev);
    if (drag) {
      if (drag.node) {
        var w = toWorld(p.x, p.y);
        drag.node.x = w.x; drag.node.y = w.y;
        drag.moved += Math.abs(p.x - drag.sx) + Math.abs(p.y - drag.sy);
        alpha = Math.max(alpha, 0.3); running = true;
      } else {
        view.tx = drag.tx + (p.x - drag.sx);
        view.ty = drag.ty + (p.y - drag.sy);
      }
      return;
    }
    var n = hitNode(p.x, p.y);
    state.hover = n;
    var tip = document.getElementById('tooltip');
    if (n) {
      tip.hidden = false;
      tip.innerHTML = '';
      var c1 = document.createElement('code'); c1.textContent = n.id;
      tip.appendChild(c1);
      tip.appendChild(document.createTextNode(' ' + (n.title || '(missing record)')));
      var line = document.createElement('div');
      line.className = 'muted';
      line.textContent = (n.layer || 'dangling') + '/' + (n.scope || '-') +
        (n.dangling ? ' · referenced but missing' : ' · degree ' + n.degree);
      tip.appendChild(line);
      var tx = ev.clientX + 14, ty = ev.clientY + 14;
      if (tx + tip.offsetWidth > window.innerWidth - 8) tx = ev.clientX - tip.offsetWidth - 10;
      if (ty + tip.offsetHeight > window.innerHeight - 8) ty = ev.clientY - tip.offsetHeight - 10;
      tip.style.left = tx + 'px'; tip.style.top = ty + 'px';
      canvas.style.cursor = 'pointer';
    } else {
      tip.hidden = true;
      canvas.style.cursor = '';
    }
  });
  function endDrag(ev) {
    if (!drag) return;
    canvas.classList.remove('dragging');
    var p = ev ? localXY(ev) : null;
    if (drag.node) {
      drag.node.pinned = false;
      if (drag.moved < 5 && p) select(drag.node);
    }
    drag = null;
  }
  canvas.addEventListener('pointerup', endDrag);
  canvas.addEventListener('pointercancel', function () { endDrag(null); });
  canvas.addEventListener('pointerleave', function () {
    state.hover = null;
    document.getElementById('tooltip').hidden = true;
  });
  canvas.addEventListener('wheel', function (ev) {
    ev.preventDefault();
    var p = localXY(ev);
    var factor = Math.exp(-ev.deltaY * 0.0015);
    var k2 = Math.max(0.08, Math.min(12, view.k * factor));
    factor = k2 / view.k;
    view.tx = p.x - (p.x - view.tx) * factor;
    view.ty = p.y - (p.y - view.ty) * factor;
    view.k = k2;
  }, { passive: false });

  // ---- selection side panel ----
  function fieldRow(dt, dd) {
    var dtEl = document.createElement('dt'); dtEl.textContent = dt;
    var ddEl = document.createElement('dd'); ddEl.textContent = dd;
    return [dtEl, ddEl];
  }
  function edgeList(list) {
    var ul = document.createElement('ul');
    if (!list.length) {
      var li = document.createElement('li');
      li.className = 'muted';
      li.textContent = '(none)';
      ul.appendChild(li);
      return ul;
    }
    list.forEach(function (e) {
      var other = byId[e.other];
      var li = document.createElement('li');
      var btn = document.createElement('button');
      btn.className = 'edge-btn';
      btn.type = 'button';
      var kind = document.createElement('span');
      kind.className = 'kind';
      kind.textContent = e.kind;
      btn.appendChild(kind);
      btn.appendChild(document.createTextNode(e.other + (other && other.title ? ' — ' + other.title : '')));
      btn.addEventListener('click', function () {
        if (other) { select(other); centerOn(other); }
      });
      li.appendChild(btn);
      ul.appendChild(li);
    });
    return ul;
  }
  function select(n) {
    state.selected = n;
    var panel = document.getElementById('panel');
    panel.hidden = false;
    panel.innerHTML = '';
    var h2 = document.createElement('h2');
    h2.textContent = n.id;
    panel.appendChild(h2);
    if (n.dangling) {
      var warn = document.createElement('p');
      warn.className = 'muted';
      warn.textContent = 'referenced but missing from the corpus';
      panel.appendChild(warn);
    }
    var dl = document.createElement('dl');
    fieldRow('title', n.title || '(missing record)').forEach(function (el) { dl.appendChild(el); });
    fieldRow('layer', n.layer || '-').forEach(function (el) { dl.appendChild(el); });
    fieldRow('scope', n.scope || '-').forEach(function (el) { dl.appendChild(el); });
    fieldRow('status', n.status || '-').forEach(function (el) { dl.appendChild(el); });
    fieldRow('decay', n.dangling ? '-' : String(n.decay)).forEach(function (el) { dl.appendChild(el); });
    fieldRow('file', n.file || '-').forEach(function (el) { dl.appendChild(el); });
    fieldRow('degree', String(n.degree)).forEach(function (el) { dl.appendChild(el); });
    panel.appendChild(dl);

    var out = [], inc = [];
    edges.forEach(function (e) {
      if (e.from === n.id) out.push({ kind: e.kind, other: e.to });
      if (e.to === n.id) inc.push({ kind: e.kind, other: e.from });
    });
    out.sort(function (a, b) { return a.other < b.other ? -1 : a.other > b.other ? 1 : 0; });
    inc.sort(function (a, b) { return a.other < b.other ? -1 : a.other > b.other ? 1 : 0; });
    var h3o = document.createElement('h3'); h3o.textContent = 'outbound (' + out.length + ')';
    panel.appendChild(h3o);
    panel.appendChild(edgeList(out));
    var h3i = document.createElement('h3'); h3i.textContent = 'inbound (' + inc.length + ')';
    panel.appendChild(h3i);
    panel.appendChild(edgeList(inc));
  }

  // ---- search ----
  var searchBox = document.getElementById('search');
  var matchCount = document.getElementById('match-count');
  searchBox.addEventListener('input', function () {
    state.query = searchBox.value.trim();
    state.matches = state.query ? nodes.filter(function (n) {
      return nodeVisible(n) && matchesQuery(n);
    }).length : -1;
    matchCount.textContent = state.query ? state.matches + ' match' + (state.matches === 1 ? '' : 'es') : '';
    draw();
  });

  if (nodes.length) fit();
  frame();
})();
</script>
</body>
</html>
`
