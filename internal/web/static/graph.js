// Graph view: draws the neighbourhood from /graph/data as SVG. The list on the
// page is the accessible, keyboard-navigable equivalent and works without this
// script. The layout is computed in one go, never animated, so it is identical
// for people who prefer reduced motion.
(function () {
  var box = document.getElementById("graph-canvas");
  if (!box || !window.d3f) return;
  var NS = "http://www.w3.org/2000/svg";
  var data = null, timer = 0, lastW = 0;

  function el(name, attrs, parent) {
    var n = document.createElementNS(NS, name);
    for (var k in attrs) n.setAttribute(k, attrs[k]);
    if (parent) parent.appendChild(n);
    return n;
  }
  function short(s, n) { return s.length > n ? s.slice(0, n - 1).trim() + "…" : s; }
  function go(id) {
    var p = new URLSearchParams(location.search);
    p.set("center", id);
    p.delete("q"); p.delete("mergeq"); p.delete("batch"); p.delete("restored"); p.delete("renamed"); p.delete("entity");
    location.href = "/graph?" + p.toString();
  }

  function draw() {
    var W = Math.max(280, box.clientWidth), H = W < 520 ? 440 : 540;
    lastW = box.clientWidth;
    var nodes = data.nodes.map(function (n) { return Object.assign({}, n); });
    var byId = {};
    nodes.forEach(function (n) { byId[n.id] = n; });
    var links = data.edges.filter(function (e) { return byId[e.source] && byId[e.target]; })
      .map(function (e) { return { id: e.id, source: e.source, target: e.target, label: e.label }; });
    nodes.forEach(function (n) { if (n.center) { n.fx = W / 2; n.fy = H / 2; } });
    var sim = d3f.forceSimulation(nodes)
      .force("link", d3f.forceLink(links).id(function (d) { return d.id; }).distance(function (l) { return (l.source.center || l.target.center ? 0.38 : 0.24) * Math.min(W, H); }).strength(0.35))
      .force("charge", d3f.forceManyBody().strength(nodes.length > 60 ? -90 : -320))
      .force("collide", d3f.forceCollide(nodes.length > 60 ? 12 : 30))
      .force("center", d3f.forceCenter(W / 2, H / 2))
      .stop();
    for (var i = 0; i < 300; i++) sim.tick();
    // Which nodes carry a visible label: all of them when the map is small,
    // otherwise the centre and the best-connected neighbours. The rest show
    // theirs on hover, focus (via the list) or tap.
    var LABEL_ALL = 12, LABEL_TOP = 8;
    var ranked = nodes.filter(function (n) { return !n.center; })
      .sort(function (a, b) { return (b.rels || 0) - (a.rels || 0); });
    var shown = {};
    nodes.forEach(function (n) { if (n.center || nodes.length <= LABEL_ALL) shown[n.id] = true; });
    if (nodes.length > LABEL_ALL) ranked.slice(0, LABEL_TOP).forEach(function (n) { shown[n.id] = true; });
    function labelOf(n) { return short(n.name, n.center ? 28 : 20); }
    // Fit the viewBox to the nodes, their labels and a margin, so the map
    // fills its frame instead of floating in blank canvas.
    var x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity, m = 24;
    nodes.forEach(function (n) {
      var r = n.center ? 20 : 14, lw = shown[n.id] ? labelOf(n).length * 3.9 : 0, lb = shown[n.id] ? (n.center ? 42 : 34) : 0;
      x0 = Math.min(x0, n.x - Math.max(r, lw)); x1 = Math.max(x1, n.x + Math.max(r, lw));
      y0 = Math.min(y0, n.y - r); y1 = Math.max(y1, n.y + Math.max(r, lb));
    });
    x0 -= m; y0 -= m; x1 += m; y1 += m;
    var minW = 300, minH = 200;
    if (x1 - x0 < minW) { var cx = (x0 + x1) / 2; x0 = cx - minW / 2; x1 = cx + minW / 2; }
    if (y1 - y0 < minH) { var cy = (y0 + y1) / 2; y0 = cy - minH / 2; y1 = cy + minH / 2; }
    var VW = x1 - x0, VH = y1 - y0;

    box.textContent = "";
    var svg = el("svg", { viewBox: x0 + " " + y0 + " " + VW + " " + VH, "aria-hidden": "true", focusable: "false", class: "g-svg" }, box);
    var defs = el("defs", {}, svg);
    var mk = el("marker", { id: "g-arrow", viewBox: "0 0 10 10", refX: "9", refY: "5", markerWidth: "7", markerHeight: "7", orient: "auto-start-reverse" }, defs);
    el("path", { d: "M0 1L9 5L0 9Z", class: "g-arrowhead" }, mk);
    var gl = el("g", {}, svg), gn = el("g", {}, svg);
    var open = null;
    var coarse = window.matchMedia && matchMedia("(hover: none)").matches;
    links.forEach(function (l) {
      var s = l.source, t = l.target, dx = t.x - s.x, dy = t.y - s.y, d = Math.sqrt(dx * dx + dy * dy) || 1;
      var rs = s.center ? 17 : 12, rt = t.center ? 17 : 12;
      var line = el("line", { x1: s.x + dx / d * rs, y1: s.y + dy / d * rs, x2: t.x - dx / d * (rt + 3), y2: t.y - dy / d * (rt + 3),
        class: "g-edge", "marker-end": "url(#g-arrow)" }, gl);
      el("title", {}, line).textContent = s.name + " " + l.label + " " + t.name;
    });
    nodes.forEach(function (n) {
      var g = el("g", { transform: "translate(" + n.x + " " + n.y + ")", class: "g-node" + (n.center ? " is-center" : ""), "data-node": n.id }, gn);
      var sc = n.center ? 1.6 : 1;
      el("path", { d: n.path, "fill-rule": "evenodd", transform: "scale(" + sc + ")", class: "g-shape" }, g);
      el("title", {}, g).textContent = n.name + " (" + n.type_label + ")";
      var t = el("text", { y: n.center ? 32 : 24, "text-anchor": "middle", class: "g-label" + (shown[n.id] ? "" : " g-label-more") }, g);
      t.textContent = labelOf(n);
      if (!n.center) g.addEventListener("click", function () {
        // On touch, a first tap on an unlabelled shape names it; a second opens it.
        if (coarse && !shown[n.id] && open !== g) {
          if (open) open.classList.remove("is-open");
          open = g; g.classList.add("is-open");
          return;
        }
        go(n.id);
      });
    });
    // Moving through the list highlights the same shape on the map.
    document.querySelectorAll("#node-list a[data-node]").forEach(function (a) {
      function set(on) {
        var g = svg.querySelector('[data-node="' + a.getAttribute("data-node") + '"]');
        if (g) g.classList.toggle("is-hot", on);
      }
      a.onmouseenter = a.onfocus = function () { set(true); };
      a.onmouseleave = a.onblur = function () { set(false); };
    });
  }

  fetch(box.getAttribute("data-src"), { credentials: "same-origin", headers: { Accept: "application/json" } })
    .then(function (r) { if (!r.ok) throw new Error(r.status); return r.json(); })
    .then(function (j) { data = j; draw(); box.classList.add("is-ready"); })
    .catch(function () { box.hidden = true; var h = document.querySelector(".graph-hint"); if (h) h.hidden = true; });

  addEventListener("resize", function () {
    clearTimeout(timer);
    timer = setTimeout(function () { if (data && box.clientWidth !== lastW) draw(); }, 150);
  });
})();
