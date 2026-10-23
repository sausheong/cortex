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
    var pad = 44;
    nodes.forEach(function (n) { n.x = Math.max(pad, Math.min(W - pad, n.x)); n.y = Math.max(pad, Math.min(H - pad, n.y)); });

    box.textContent = "";
    var svg = el("svg", { viewBox: "0 0 " + W + " " + H, width: W, height: H, "aria-hidden": "true", focusable: "false", class: "g-svg" }, box);
    var defs = el("defs", {}, svg);
    var mk = el("marker", { id: "g-arrow", viewBox: "0 0 10 10", refX: "9", refY: "5", markerWidth: "7", markerHeight: "7", orient: "auto-start-reverse" }, defs);
    el("path", { d: "M0 1L9 5L0 9Z", class: "g-arrowhead" }, mk);
    var gl = el("g", {}, svg), gn = el("g", {}, svg);
    var showAll = nodes.length <= 60 && W >= 520;
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
      if (showAll || n.center) {
        var t = el("text", { y: n.center ? 32 : 24, "text-anchor": "middle", class: "g-label" }, g);
        t.textContent = short(n.name, n.center ? 28 : 20);
      }
      if (!n.center) g.addEventListener("click", function () { go(n.id); });
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
