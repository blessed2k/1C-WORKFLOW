// SPA поверх cytoscape.js (vendored, /assets/cytoscape.min.js) и HTTP API
// раздела 8.1 (internal/graphweb/handler.go). Прогрессивное раскрытие:
// стартовый экран запрашивает РОВНО один узел (кнопка «Открыть», только
// GET /api/node/{id}), каждый клик по узлу на канве отдельным запросом
// догружает GET /api/node/{id} (обновить карточку/бейджи) и
// GET /api/neighbors/{id} (раскрыть соседей). GET /api/radius не
// вызывается никогда — это инструмент MCP, не SPA (spec §7).
(function () {
  "use strict";

  var KIND_COLOR = {
    "writes-register": "#4da3ff",
    "reads-register": "#5bd67d",
    "writes-declared": "#e0a72e",
    "reads-query": "#b085f5"
  };
  var BADGE_SYMBOL = {
    "has-dynamic": "⚡",              // молния — динамика не даёт ребра
    "attribution-truncated": "✂",     // ножницы — обход упёрся в потолок
    "attribution-stale": "⏳"          // песочные часы — могло устареть
  };
  // Точная формулировка из interfaces.md: attribution-stale значит «связь
  // могла устареть», НЕ «связь потеряна» — подписывать этими словами.
  var BADGE_TEXT = {
    "has-dynamic": "есть динамические обращения, статически не отслежены",
    "attribution-truncated": "обход атрибуции упёрся в потолок глубины, ребро могло быть",
    "attribution-stale": "связь могла устареть (снимается полной пересборкой)"
  };

  var state = {
    project: "",
    projects: [],
    kinds: Object.keys(KIND_COLOR).reduce(function (acc, k) { acc[k] = true; return acc; }, {}),
    minConfidence: 0,
    fetchedNodes: {},   // objectId -> true, когда карточка узла загружена
    godMetric: "fan-in"
  };

  var statusEl = document.getElementById("status");
  function setStatus(text, isError) {
    statusEl.textContent = text || "";
    statusEl.className = isError ? "error" : "";
  }

  function apiURL(path, params) {
    var q = new URLSearchParams(params || {});
    if (state.project) q.set("project", state.project);
    var qs = q.toString();
    return path + (qs ? "?" + qs : "");
  }

  function fetchJSON(path, params) {
    return fetch(apiURL(path, params)).then(function (resp) {
      return resp.json().then(function (body) {
        if (!resp.ok) {
          var msg = (body && body.message) || ("HTTP " + resp.status);
          var hint = body && body.hint ? " (" + body.hint + ")" : "";
          throw new Error(msg + hint);
        }
        return body;
      });
    });
  }

  // ---- cytoscape ----------------------------------------------------
  var cy = cytoscape({
    container: document.getElementById("cy"),
    layout: { name: "cose", animate: false },
    style: [
      {
        selector: "node.group",
        style: {
          "shape": "round-rectangle",
          "background-opacity": 0.08,
          "background-color": "#4da3ff",
          "border-width": 1,
          "border-style": "dashed",
          "border-color": "#4da3ff",
          "label": "data(label)",
          "text-valign": "top",
          "text-halign": "center",
          "color": "#8b929c",
          "font-size": 10,
          "padding": "18px"
        }
      },
      {
        selector: "node.obj",
        style: {
          "shape": "round-rectangle",
          "background-color": "#2c313a",
          "border-width": 2,
          "border-color": "#4da3ff",
          "label": "data(label)",
          "color": "#d7dbe0",
          "font-size": 11,
          "text-wrap": "wrap",
          "text-max-width": "140px",
          "width": "label",
          "height": "label",
          "padding": "8px",
          "text-valign": "center",
          "text-halign": "center"
        }
      },
      {
        selector: "node.obj:selected",
        style: { "border-color": "#ffffff", "border-width": 3 }
      },
      {
        selector: "edge",
        style: {
          "width": 2,
          "line-color": "data(color)",
          "target-arrow-color": "data(color)",
          "target-arrow-shape": "triangle",
          "curve-style": "bezier",
          "opacity": "data(opacity)",
          "label": "data(kind)",
          "font-size": 8,
          "color": "#8b929c",
          "text-rotation": "autorotate",
          "text-margin-y": -6
        }
      },
      { selector: ".hidden-filter", style: { "display": "none" } }
    ]
  });

  function groupId(mtype) { return "grp:" + mtype; }

  function ensureGroup(mtype) {
    var id = groupId(mtype);
    if (cy.getElementById(id).nonempty()) return id;
    cy.add({ group: "nodes", data: { id: id, label: mtype }, classes: "group" });
    return id;
  }

  function badgeSuffix(badges) {
    if (!badges || !badges.length) return "";
    return " " + badges.map(function (b) {
      return (BADGE_SYMBOL[b.badge] || "?") + b.count;
    }).join(" ");
  }

  function nodeLabel(item) {
    return (item.nameDisplay || ("#" + item.objectId)) + badgeSuffix(item.badges);
  }

  // Добавляет/обновляет узел из полной карточки (GET /api/node/{id}):
  // несёт badges, поэтому суффикс бейджей на узле обновляется отсюда.
  function upsertFullNode(item) {
    var parent = ensureGroup(item.mtype || "?");
    var id = String(item.objectId);
    var el = cy.getElementById(id);
    var data = {
      id: id, objectId: item.objectId, mtype: item.mtype,
      nameDisplay: item.nameDisplay, component: item.component, layer: item.layer,
      badges: item.badges || [], label: nodeLabel(item), parent: parent
    };
    if (el.nonempty()) {
      el.data(data);
    } else {
      cy.add({ group: "nodes", data: data, classes: "obj" });
    }
    state.fetchedNodes[item.objectId] = true;
    return cy.getElementById(id);
  }

  // Добавляет узел-заглушку по денормализованным полям ребра (без бейджей —
  // они появятся, когда узел будет открыт /api/node/{id}, см. ensureNodeCard).
  function upsertStubNode(objectId, mtype, display) {
    var id = String(objectId);
    if (cy.getElementById(id).nonempty()) return cy.getElementById(id);
    var parent = ensureGroup(mtype || "?");
    return cy.add({
      group: "nodes",
      data: { id: id, objectId: objectId, mtype: mtype, nameDisplay: display, badges: [], label: display, parent: parent },
      classes: "obj"
    });
  }

  function edgeOpacity(confidence) {
    return 0.35 + 0.65 * Math.max(0, Math.min(1, confidence));
  }

  function upsertEdge(item) {
    var id = "e:" + item.id;
    if (cy.getElementById(id).nonempty()) return;
    cy.add({
      group: "edges",
      data: {
        id: id, edgeId: item.id, source: String(item.fromObjectId), target: String(item.toObjectId),
        kind: item.kind, confidence: item.confidence, provenance: item.provenance,
        color: KIND_COLOR[item.kind] || "#888", opacity: edgeOpacity(item.confidence)
      }
    });
  }

  function applyFilters() {
    cy.edges().forEach(function (e) {
      var d = e.data();
      var hide = !state.kinds[d.kind] || d.confidence < state.minConfidence;
      e.toggleClass("hidden-filter", hide);
    });
  }

  // ---- карточка узла (badges) ---------------------------------------
  function ensureNodeCard(objectId) {
    if (state.fetchedNodes[objectId]) return Promise.resolve();
    return fetchJSON("/api/node/" + objectId).then(function (resp) {
      if (resp.items && resp.items[0]) upsertFullNode(resp.items[0]);
    }).catch(function (err) {
      setStatus("узел " + objectId + ": " + err.message, true);
    });
  }

  function renderNodePanel(item) {
    var el = document.getElementById("nodePanel");
    var html = "<h2>Узел</h2>";
    html += "<div><strong>" + escapeHTML(item.nameDisplay) + "</strong></div>";
    html += "<div class='muted'>" + escapeHTML(item.mtype) + " · " + escapeHTML(item.component) + " · слой " + escapeHTML(item.layer) + "</div>";
    if (item.badges && item.badges.length) {
      html += "<div style='margin-top:8px;'>";
      item.badges.forEach(function (b) {
        html += "<div class='badge-row'><span class='badge-dot " + b.badge + "'></span><span>" +
          escapeHTML(b.badge) + " ×" + b.count + " — " + escapeHTML(BADGE_TEXT[b.badge] || "") + "</span></div>";
      });
      html += "</div>";
    } else {
      html += "<div class='muted' style='margin-top:8px;'>бейджей нет</div>";
    }
    html += "<div id='neighborsMore' class='muted' style='margin-top:8px;'></div>";
    el.innerHTML = html;
  }

  function escapeHTML(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  // ---- раскрытие соседей ---------------------------------------------
  function currentKindsParam() {
    return Object.keys(state.kinds).filter(function (k) { return state.kinds[k]; }).join(",");
  }

  function expandNeighbors(objectId, cursor) {
    var params = { dir: "both", kinds: currentKindsParam(), minConfidence: String(state.minConfidence) };
    if (cursor) params.cursor = cursor;
    return fetchJSON("/api/neighbors/" + objectId, params).then(function (resp) {
      var newIds = {};
      (resp.items || []).forEach(function (e) {
        upsertStubNode(e.fromObjectId, e.fromMType, e.fromDisplay);
        upsertStubNode(e.toObjectId, e.toMType, e.toDisplay);
        upsertEdge(e);
        if (e.fromObjectId !== objectId) newIds[e.fromObjectId] = true;
        if (e.toObjectId !== objectId) newIds[e.toObjectId] = true;
      });
      applyFilters();
      cy.layout({ name: "cose", animate: false }).run();
      var more = document.getElementById("neighborsMore");
      if (more) {
        if (resp.nextCursor) {
          more.innerHTML = "";
          var btn = document.createElement("button");
          btn.className = "more-btn";
          btn.textContent = "Догрузить ещё соседей (" + (resp.items || []).length + " из " + resp.totalCount + ")";
          btn.onclick = function () { expandNeighbors(objectId, resp.nextCursor); };
          more.appendChild(btn);
        } else {
          more.textContent = "соседей показано: " + (resp.items || []).length + " из " + resp.totalCount;
        }
      }
      // Бейджи новых узлов подтягиваются по факту раскрытия — ограничено
      // страницей соседей, а не всем графом (весь граф не грузится никогда).
      return Promise.all(Object.keys(newIds).map(function (id) { return ensureNodeCard(Number(id)); }));
    }).catch(function (err) {
      setStatus("соседи узла " + objectId + ": " + err.message, true);
    });
  }

  function onNodeClicked(objectId) {
    setStatus("");
    ensureNodeCard(objectId).then(function () {
      var el = cy.getElementById(String(objectId));
      if (el.nonempty()) renderNodePanel(el.data());
    });
    expandNeighbors(objectId);
  }

  cy.on("tap", "node.obj", function (evt) {
    onNodeClicked(evt.target.data("objectId"));
  });

  cy.on("tap", "edge", function (evt) {
    showEdgeEvidence(evt.target.data("edgeId"));
  });

  // ---- открыть узел (стартовый экран) --------------------------------
  function openNode(id) {
    setStatus("");
    fetchJSON("/api/node/" + id).then(function (resp) {
      if (!resp.items || !resp.items[0]) {
        setStatus("узел " + id + " не найден", true);
        return;
      }
      var el = upsertFullNode(resp.items[0]);
      cy.layout({ name: "cose", animate: false }).run();
      cy.center(el);
      renderNodePanel(resp.items[0]);
    }).catch(function (err) {
      setStatus("узел " + id + ": " + err.message, true);
    });
  }

  document.getElementById("openBtn").addEventListener("click", function () {
    var v = Number(document.getElementById("nodeId").value);
    if (v > 0) openNode(v);
  });
  document.getElementById("nodeId").addEventListener("keydown", function (e) {
    if (e.key === "Enter") document.getElementById("openBtn").click();
  });

  document.getElementById("resetBtn").addEventListener("click", function () {
    cy.elements().remove();
    state.fetchedNodes = {};
    setStatus("");
    document.getElementById("nodePanel").innerHTML = "<h2>Узел</h2><div class='muted'>Кликните узел, чтобы увидеть карточку и раскрыть соседей.</div>";
    document.getElementById("edgePanel").hidden = true;
  });

  // ---- фильтры ---------------------------------------------------------
  document.querySelectorAll("#filters input[type=checkbox]").forEach(function (cb) {
    cb.addEventListener("change", function () {
      state.kinds[cb.getAttribute("data-kind")] = cb.checked;
      applyFilters();
    });
  });
  var minConfEl = document.getElementById("minConfidence");
  var minConfLabel = document.getElementById("minConfidenceLabel");
  minConfEl.addEventListener("input", function () {
    state.minConfidence = Number(minConfEl.value);
    minConfLabel.textContent = state.minConfidence.toFixed(2);
    applyFilters();
  });

  // ---- god-node ----------------------------------------------------
  var godPanel = document.getElementById("godPanel");
  document.getElementById("godBtn").addEventListener("click", function () {
    godPanel.hidden = !godPanel.hidden;
    if (!godPanel.hidden) loadGodNodes();
  });
  document.getElementById("godFanIn").addEventListener("click", function () { setGodMetric("fan-in"); });
  document.getElementById("godFanOut").addEventListener("click", function () { setGodMetric("fan-out"); });

  function setGodMetric(m) {
    state.godMetric = m;
    document.getElementById("godFanIn").classList.toggle("active", m === "fan-in");
    document.getElementById("godFanOut").classList.toggle("active", m === "fan-out");
    loadGodNodes();
  }

  function loadGodNodes() {
    fetchJSON("/api/godnodes", { metric: state.godMetric, top: "20" }).then(function (resp) {
      var body = document.getElementById("godBody");
      body.innerHTML = "";
      (resp.items || []).forEach(function (it) {
        var tr = document.createElement("tr");
        tr.className = "clickable";
        tr.innerHTML = "<td>" + escapeHTML(it.nameDisplay) + "</td><td>" + it.fanIn + "</td><td>" + it.fanOut + "</td>";
        tr.addEventListener("click", function () {
          document.getElementById("nodeId").value = it.objectId;
          openNode(it.objectId);
        });
        body.appendChild(tr);
      });
    }).catch(function (err) {
      setStatus("god-node: " + err.message, true);
    });
  }

  // ---- evidence ребра ------------------------------------------------
  function showEdgeEvidence(edgeId) {
    fetchJSON("/api/edge/" + edgeId + "/evidence").then(function (resp) {
      var item = resp.items && resp.items[0];
      var panel = document.getElementById("edgePanel");
      var content = document.getElementById("edgeContent");
      if (!item) { content.textContent = "evidence не найдено"; panel.hidden = false; return; }
      var html = "<div class='muted'>" + escapeHTML(item.kind) + " · " + escapeHTML(item.provenance) +
        " · confidence " + item.confidence.toFixed(2) + "</div>";
      if (item.provenance === "metadata-declared") {
        html += "<div style='margin-top:8px;'>Декларировано в XML: <strong>" + escapeHTML(item.xmlFile || "?") + "</strong></div>";
        if (item.declaredRegister) html += "<div>Регистр: " + escapeHTML(item.declaredRegister) + "</div>";
      } else if (item.chain && item.chain.length) {
        html += "<div style='margin-top:8px;'>Цепочка атрибуции:</div>";
        item.chain.forEach(function (step, i) {
          html += "<div class='evidence-chain-step'>" + (i + 1) + ". " + escapeHTML(step.symbolName || step.symbolUid || "?") +
            (step.module ? " (" + escapeHTML(step.module) + ")" : "") +
            (step.confidence ? " — confidence " + step.confidence.toFixed(2) : "") + "</div>";
        });
      } else {
        html += "<div class='muted' style='margin-top:8px;'>цепочка пуста</div>";
      }
      if (item.files && item.files.length) {
        html += "<div style='margin-top:8px;' class='muted'>Файлы: " +
          item.files.map(function (f) { return escapeHTML(f.relPath); }).join(", ") + "</div>";
      }
      content.innerHTML = html;
      panel.hidden = false;
    }).catch(function (err) {
      setStatus("evidence ребра " + edgeId + ": " + err.message, true);
    });
  }

  // ---- проекты + deep-link -------------------------------------------
  function loadProjects() {
    return fetchJSON("/api/projects").then(function (resp) {
      state.projects = resp.items || [];
      var sel = document.getElementById("project");
      sel.innerHTML = "";
      state.projects.forEach(function (p) {
        var opt = document.createElement("option");
        opt.value = p.project; opt.textContent = p.project + (p.needsFullRebuild ? " (нужна пересборка)" : "");
        sel.appendChild(opt);
      });
      if (state.projects.length) {
        state.project = state.projects[0].project;
        sel.value = state.project;
      }
      sel.addEventListener("change", function () { state.project = sel.value; });
      if (!state.projects.length) setStatus("нет доступных проектов — запустите mcp1c graph --project <корень>", true);
    }).catch(function (err) {
      setStatus("список проектов: " + err.message, true);
    });
  }

  var params = new URLSearchParams(window.location.search);
  loadProjects().then(function () {
    if (params.get("project")) {
      state.project = params.get("project");
      document.getElementById("project").value = state.project;
    }
    var startNode = Number(params.get("node"));
    if (startNode > 0) {
      document.getElementById("nodeId").value = startNode;
      openNode(startNode);
    }
  });
})();
