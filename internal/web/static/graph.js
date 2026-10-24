// Graph view: draws the neighbourhood from /graph/data as SVG. The list on the
// page is the accessible, keyboard-navigable equivalent and works without this
// script. The layout is computed in one go, never animated, so it is identical
// for people who prefer reduced motion.
(function () {
  var box = document.getElementById("graph-canvas");
  if (!box || !window.d3f) return;
  var NS = "http://www.w3.org/2000/svg";
  var data = null, timer = 0, lastW = 0, open = null;
  function isCoarse() { return !!(window.matchMedia && matchMedia("(hover: none)").matches); }
  function closeOpen() { if (open) open.classList.remove("is-open"); open = null; }
  // A tapped label closes on a tap elsewhere or on Escape.
  document.addEventListener("click", closeOpen);
  document.addEventListener("keydown", function (e) { if (e.key === "Escape") closeOpen(); });
  // On phones the side panel starts folded so the map is within reach; a fold
  // holding an error or an open task stays open.
  if (window.matchMedia && matchMedia("(max-width:560px)").matches)
    document.querySelectorAll("details.fold:not([data-keep])").forEach(function (d) { d.open = false; });

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
    // The frame height follows the node count so a handful of nodes get a
    // small map rather than a big empty one.
    var count = data.nodes.length, W = Math.max(280, box.clientWidth);
    var H = count < 5 ? 220 : count < 10 ? 340 : (W < 520 ? 440 : 540);
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
    // Phones get a taller frame, and the layout is squeezed to fit the frame
    // width so the map is not scaled down below readable size.
    var pad = 36, bw = box.clientWidth || W;
    (function squeeze() {
      var ax = Infinity, bx = -Infinity, ay = Infinity, by = -Infinity;
      nodes.forEach(function (n) { ax = Math.min(ax, n.x); bx = Math.max(bx, n.x); ay = Math.min(ay, n.y); by = Math.max(by, n.y); });
      var fx = Math.min(1, (W - 2 * pad) / Math.max(1, bx - ax)), fy = Math.min(1, (H - 2 * pad) / Math.max(1, by - ay));
      if (fx < 1 || fy < 1) nodes.forEach(function (n) { n.x = W / 2 + (n.x - W / 2) * fx; n.y = H / 2 + (n.y - H / 2) * fy; });
    })();
    // Nodes never sit closer than SEP, so shapes and their labels cannot
    // overlap even with two nodes. The centre stays put; others are pushed apart.
    (function separate() {
      var SEP = 96, it, a, b, i, j;
      for (it = 0; it < 60; it++) {
        var moved = false;
        for (i = 0; i < nodes.length; i++) for (j = i + 1; j < nodes.length; j++) {
          a = nodes[i]; b = nodes[j];
          var dx = b.x - a.x, dy = b.y - a.y, d = Math.sqrt(dx * dx + dy * dy);
          if (d >= SEP) continue;
          if (d < 0.01) { dx = 1; dy = 0; d = 1; }
          var push = SEP - d, ux = dx / d, uy = dy / d;
          if (a.center) { b.x += ux * push; b.y += uy * push; }
          else if (b.center) { a.x -= ux * push; a.y -= uy * push; }
          else { a.x -= ux * push / 2; a.y -= uy * push / 2; b.x += ux * push / 2; b.y += uy * push / 2; }
          moved = true;
        }
        if (!moved) break;
      }
    })();
    // Every node gets a label. They are placed greedily, centre first and then
    // the best-connected neighbours. A label that collides is nudged to another
    // side or farther out, then shortened (the full name shows on hover and focus and stays
    // in the title), and as a last resort may touch a shape but not a label.
    // Sizes are in viewBox units, so they depend on the final scale k
    // (rendered pixels per unit); font and shapes are compensated by 1/k so
    // text stays about 14px and shapes and tap targets stay usable.
    var order = nodes.filter(function (n) { return n.center; }).concat(
      nodes.filter(function (n) { return !n.center; }).sort(function (a, b) { return (b.rels || 0) - (a.rels || 0); }));
    function labelOf(n, max) { return short(n.name, max || (n.center ? (bw < 520 ? 20 : 28) : (bw < 520 ? 14 : 20))); }
    function plan(k) {
      var u = 1 / Math.min(1, k), ss = Math.min(1.6, u), fs = 14 * (1 / k);
      var info = {}, rects = [], x0 = Infinity, y0 = Infinity, x1 = -Infinity, y1 = -Infinity;
      nodes.forEach(function (n) {
        var r = (n.center ? 20 : 14) * ss, hit = Math.max(r, 12 / k);
        info[n.id] = { r: r, hit: hit, sc: (n.center ? 1.6 : 1) * ss, label: null };
        x0 = Math.min(x0, n.x - hit); x1 = Math.max(x1, n.x + hit); y0 = Math.min(y0, n.y - hit); y1 = Math.max(y1, n.y + hit);
      });
      function hits(rc, self, lax) {
        var i;
        for (i = 0; i < rects.length; i++) if (rc.l < rects[i].r && rc.r > rects[i].l && rc.t < rects[i].b && rc.b > rects[i].t) return true;
        if (lax) return false;
        for (i = 0; i < nodes.length; i++) {
          var o = nodes[i], q = info[o.id].r + 3;
          if (o !== self && rc.l < o.x + q && rc.r > o.x - q && rc.t < o.y + q && rc.b > o.y - q) return true;
        }
        return false;
      }
      // Eight spots around the shape, then the same eight pushed out by a
      // shape radius, so a crowded neighbourhood still finds room.
      function spots(n, w, h, r, gap) {
        var out = [], rings = [0, r * 0.9], ri;
        for (ri = 0; ri < rings.length; ri++) {
          var g = gap + rings[ri];
          out.push(
            { a: "middle", x: 0, y: r + g + fs * 0.9, l: n.x - w / 2, t: n.y + r + g },
            { a: "middle", x: 0, y: -(r + g + fs * 0.25), l: n.x - w / 2, t: n.y - r - g - h },
            { a: "start", x: r + g, y: fs * 0.35, l: n.x + r + g, t: n.y - h / 2 },
            { a: "end", x: -(r + g), y: fs * 0.35, l: n.x - r - g - w, t: n.y - h / 2 },
            { a: "start", x: r + g, y: r + g + fs * 0.9, l: n.x + r * 0.7 + g, t: n.y + r * 0.7 + g },
            { a: "end", x: -(r + g), y: r + g + fs * 0.9, l: n.x - r * 0.7 - g - w, t: n.y + r * 0.7 + g },
            { a: "start", x: r + g, y: -(r + g), l: n.x + r * 0.7 + g, t: n.y - r * 0.7 - g - h },
            { a: "end", x: -(r + g), y: -(r + g), l: n.x - r * 0.7 - g - w, t: n.y - r * 0.7 - g - h });
        }
        return out;
      }
      order.forEach(function (n) {
        var r = info[n.id].r, full = n.center ? (bw < 520 ? 20 : 28) : (bw < 520 ? 14 : 20), h = fs * 1.3;
        var lens = n.center ? [full] : [full, 12, 8, 5];
        for (var li = 0; li < lens.length && !info[n.id].label; li++) {
          var text = labelOf(n, lens[li]), w = text.length * 0.56 * fs + 6;
          var cands = spots(n, w, h, r, 4);
          for (var i = 0; i < cands.length; i++) {
            var c = cands[i], rc = { l: c.l, t: c.t, r: c.l + w, b: c.t + h };
            if (rc.l < 4 || rc.r > W - 4 || rc.t < 4 || rc.b > H - 4 || hits(rc, n, li === lens.length - 1 && lens.length > 1)) continue;
            rects.push(rc);
            info[n.id].label = { text: text, a: c.a, x: c.x, y: c.y, fs: fs };
            x0 = Math.min(x0, rc.l); x1 = Math.max(x1, rc.r); y0 = Math.min(y0, rc.t); y1 = Math.max(y1, rc.b);
            break;
          }
        }
      });
      return { info: info, x0: x0, y0: y0, x1: x1, y1: y1, fs: fs };
    }
    var k = Math.min(1, bw / W), pl = plan(k), m = 16, VW, VH, vx, vy;
    for (var pass = 0; pass < 3; pass++) {
      VW = Math.max(300, pl.x1 - pl.x0 + 2 * m); VH = Math.max(160, pl.y1 - pl.y0 + 2 * m);
      var nk = bw / VW;
      if (Math.abs(nk - k) < 0.02) break;
      k = nk; pl = plan(k);
    }
    VW = Math.max(300, pl.x1 - pl.x0 + 2 * m); VH = Math.max(160, pl.y1 - pl.y0 + 2 * m);
    var x0 = (pl.x0 + pl.x1) / 2 - VW / 2, y0 = (pl.y0 + pl.y1) / 2 - VH / 2;
    var fs0 = pl.fs;

    box.textContent = "";
    var svg = el("svg", { viewBox: x0 + " " + y0 + " " + VW + " " + VH, "aria-hidden": "true", focusable: "false", class: "g-svg", width: Math.round(Math.min(bw, VW)) }, box);
    var defs = el("defs", {}, svg);
    var mk = el("marker", { id: "g-arrow", viewBox: "0 0 10 10", refX: "9", refY: "5", markerWidth: "7", markerHeight: "7", orient: "auto-start-reverse" }, defs);
    el("path", { d: "M0 1L9 5L0 9Z", class: "g-arrowhead" }, mk);
    var gl = el("g", {}, svg), gn = el("g", {}, svg);
    closeOpen();
    var hint = document.querySelector(".graph-hint");
    if (hint && isCoarse()) hint.textContent = "Tap a shape to see its name, then tap it again to move the map to it. The list below holds the same entities.";
    links.forEach(function (l) {
      var s = l.source, t = l.target, dx = t.x - s.x, dy = t.y - s.y, d = Math.sqrt(dx * dx + dy * dy) || 1;
      var rs = pl.info[s.id].sc * 10.5, rt = pl.info[t.id].sc * 10.5;
      var line = el("line", { x1: s.x + dx / d * rs, y1: s.y + dy / d * rs, x2: t.x - dx / d * (rt + 3), y2: t.y - dy / d * (rt + 3),
        class: "g-edge", "marker-end": "url(#g-arrow)" }, gl);
      el("title", {}, line).textContent = s.name + " " + l.label + " " + t.name;
    });
    nodes.forEach(function (n) {
      var inf = pl.info[n.id];
      var g = el("g", { transform: "translate(" + n.x + " " + n.y + ")", class: "g-node" + (n.center ? " is-center" : ""), "data-node": n.id }, gn);
      el("circle", { r: inf.hit, class: "g-hit" }, g);
      el("path", { d: n.path, "fill-rule": "evenodd", transform: "scale(" + inf.sc + ")", class: "g-shape" }, g);
      el("title", {}, g).textContent = n.name + " (" + n.type_label + ")";
      var lb = inf.label, shown = !!lb;
      lb = lb || { text: labelOf(n), a: "middle", x: 0, y: inf.r + 4 + fs0 * 0.9, fs: fs0 };
      var t = el("text", { x: lb.x, y: lb.y, "font-size": lb.fs, "text-anchor": lb.a, class: "g-label" + (shown ? "" : " g-label-more") }, g);
      t.textContent = lb.text;
      n.show = function (on) { t.textContent = on ? n.name : lb.text; };
      g.addEventListener("mouseenter", function () { n.show(true); });
      g.addEventListener("mouseleave", function () { n.show(false); });
      if (!n.center) g.addEventListener("click", function (ev) {
        // On touch, a first tap on an unlabelled shape names it; a second opens it.
        if (isCoarse() && !shown && open !== g) {
          if (open) open.classList.remove("is-open");
          open = g; g.classList.add("is-open"); n.show(true);
          ev.stopPropagation();
          return;
        }
        go(n.id);
      });
    });
    var nodeById = byId;
    // Moving through the list highlights the same shape on the map.
    document.querySelectorAll("#node-list a[data-node]").forEach(function (a) {
      function set(on) {
        var g = svg.querySelector('[data-node="' + a.getAttribute("data-node") + '"]');
        if (g) g.classList.toggle("is-hot", on);
        if (nodeById[a.getAttribute("data-node")]) nodeById[a.getAttribute("data-node")].show(on);
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
