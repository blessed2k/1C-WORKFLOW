// SPA поверх cytoscape.js (vendored, /assets/cytoscape.min.js) и HTTP API
// раздела 8.1 (internal/graphweb/handler.go). Прогрессивное раскрытие:
// точка входа: поиск по имени (GET /api/search), выбор объекта открывает
// узел и сразу раскрывает его соседей. Клик по узлу показывает карточку,
// двойной клик догружает соседей (GET /api/neighbors/{id}) страницами.
// GET /api/radius не вызывается никогда: это инструмент MCP, не SPA (spec §7).
//
// Режим слоёв (веха В3): карточки, соседи и god-node запрашиваются с
// view=raw|effective|diff. Узлы в режимах канонические (строка базы у
// заимствованного объекта), поэтому смена режима перерисовывает те же узлы:
// карта очищается и заново раскрывает то, что было раскрыто.
//
// Раскладка: кольца вокруг фокусного узла (последний раскрытый) по
// BFS-расстоянию от него, подробности у runLayout. cose на тех же данных
// давал перекрытия длинных подписей и каждый раз новую картинку.
(function () {
  "use strict";

  // Страница соседей за одно раскрытие зависит от площади холста: 30 узлов
  // (три кольца) читаются без зума на экране 1280, на ширине 800 столько же
  // давали масштаб 0.46, там страница около 13. Остальное: «Догрузить ещё».
  function neighborPage() {
    var area = cy.width() * cy.height();
    return Math.max(12, Math.min(30, Math.round(area / 21000)));
  }

  var KIND_INFO = {
    "writes-register": { color: "#4da3ff", verb: "пишет в", title: "запись в регистр из кода", read: false },
    "writes-declared": { color: "#e0a72e", verb: "делает движения по", title: "движения, объявленные в метаданных", read: false },
    "reads-register": { color: "#5bd67d", verb: "читает", title: "чтение регистра из кода", read: true },
    "reads-query": { color: "#b085f5", verb: "читает запросом", title: "чтение запросом", read: true },
    // В2 (ADR-039): HTTP-вызов сервиса другой базы, приходит из /api/crosslinks.
    "http-call": { color: "#7dcfff", verb: "вызывает по HTTP", title: "HTTP-вызов сервиса (сшивка по пути и маппингу хостов)", read: false }
  };

  // mtype -> [полное русское имя, короткая подпись на узле, цвет].
  var MTYPE_INFO = {
    Document: ["Документ", "Документ", "#4da3ff"],
    AccumulationRegister: ["Регистр накопления", "РН", "#f0883e"],
    InformationRegister: ["Регистр сведений", "РС", "#5bd67d"],
    AccountingRegister: ["Регистр бухгалтерии", "РБ", "#e5c07b"],
    CalculationRegister: ["Регистр расчёта", "РР", "#d19a66"],
    Catalog: ["Справочник", "Справочник", "#c678dd"],
    DataProcessor: ["Обработка", "Обработка", "#e5c07b"],
    Report: ["Отчёт", "Отчёт", "#ef6f9a"],
    CommonModule: ["Общий модуль", "ОбщМодуль", "#56b6c2"],
    CommonForm: ["Общая форма", "ОбщФорма", "#9aa5b1"],
    ExchangePlan: ["План обмена", "ПланОбмена", "#ff6b6b"],
    BusinessProcess: ["Бизнес-процесс", "БизнесПроц", "#a3be8c"],
    Task: ["Задача", "Задача", "#a3be8c"],
    DocumentJournal: ["Журнал документов", "Журнал", "#7aa2f7"],
    ChartOfCharacteristicTypes: ["План видов характеристик", "ПВХ", "#bb9af7"],
    ChartOfAccounts: ["План счетов", "ПланСчетов", "#bb9af7"],
    ChartOfCalculationTypes: ["План видов расчёта", "ПВР", "#bb9af7"],
    HTTPService: ["HTTP-сервис", "HTTP-сервис", "#7dcfff"],
    WebService: ["Web-сервис", "Web-сервис", "#7dcfff"],
    Enum: ["Перечисление", "Перечисл.", "#8b929c"],
    Constant: ["Константа", "Константа", "#8b929c"],
    ScheduledJob: ["Регламентное задание", "РеглЗадание", "#8b929c"],
    CommonCommand: ["Общая команда", "ОбщКоманда", "#8b929c"],
    Role: ["Роль", "Роль", "#8b929c"],
    Subsystem: ["Подсистема", "Подсистема", "#8b929c"],
    FilterCriterion: ["Критерий отбора", "Критерий", "#8b929c"],
    SettingsStorage: ["Хранилище настроек", "Хранилище", "#8b929c"],
    IntegrationService: ["Сервис интеграции", "Интеграция", "#7dcfff"]
  };
  var MTYPE_DEFAULT_COLOR = "#8b929c";

  function mtypeFull(m) { return (MTYPE_INFO[m] || [m || "?"])[0]; }
  function mtypeShort(m) { var i = MTYPE_INFO[m]; return i ? i[1] : (m || "?"); }
  function mtypeColor(m) { var i = MTYPE_INFO[m]; return i ? i[2] : MTYPE_DEFAULT_COLOR; }

  var BADGE_SYMBOL = {
    "has-dynamic": "⚡",              // молния: динамика не даёт ребра
    "attribution-truncated": "✂",     // ножницы: обход упёрся в потолок
    "attribution-stale": "⏳",         // песочные часы: могло устареть
    "has-dynamic-http": "⚡H"          // HTTP-вызов с вычисляемым адресом
  };
  // Точная формулировка из interfaces.md: attribution-stale значит «связь
  // могла устареть», НЕ «связь потеряна»; подписывать этими словами.
  var BADGE_TEXT = {
    "has-dynamic": "есть динамические обращения, статически не отслежены",
    "attribution-truncated": "обход атрибуции упёрся в потолок глубины, ребро могло быть",
    "attribution-stale": "связь могла устареть (снимается полной пересборкой)",
    "has-dynamic-http": "HTTP-вызовы с вычисляемым адресом: ребра нет, адресат статически не выводится"
  };

  var VIEWS = ["raw", "effective", "diff"];

  var state = {
    project: "",
    view: "effective", // raw|effective|diff, см. setView
    // gen: поколение карты. resetMap его увеличивает, и ответ, запрошенный
    // в прошлом поколении (до смены режима или проекта), отбрасывается:
    // иначе запоздавшие соседи и карточки старого режима дописывались бы
    // в новую карту.
    gen: 0,
    kinds: Object.keys(KIND_INFO).reduce(function (acc, k) { acc[k] = true; return acc; }, {}),
    minConfidence: 0,
    cards: {},        // objectId -> полная карточка /api/node (badges, fanIn, fanOut)
    cardPending: {},  // objectId -> Promise, чтобы не просить одну карточку дважды
    expanded: {},     // objectId -> {nextCursor, total, loaded, limit, busy}
    focus: null,      // objectId фокусного узла: центр раскладки
    selected: null,
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

  function fetchJSON(path, params, signal) {
    return fetch(apiURL(path, params), { signal: signal }).then(function (resp) {
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

  function escapeHTML(s) {
    return String(s == null ? "" : s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  // Имена 1С пишутся слитно (ПрочиеРасходы): cytoscape переносит строку
  // только по пробелу или U+200B, поэтому на стыках слов ставится U+200B.
  function breakCamel(s) {
    return String(s || "").replace(/([a-zа-яё0-9])([A-ZА-ЯЁ])/g, "$1\u200b$2");
  }

  // ---- cytoscape ----------------------------------------------------
  var css = getComputedStyle(document.documentElement);
  var COLOR_BG = css.getPropertyValue("--bg").trim() || "#1b1e23";
  var COLOR_TEXT = css.getPropertyValue("--text").trim() || "#d7dbe0";
  var COLOR_EXT = css.getPropertyValue("--ext").trim() || "#2ee6c5";

  var cy = cytoscape({
    container: document.getElementById("cy"),
    minZoom: 0.1,
    maxZoom: 3,
    wheelSensitivity: 0.3,
    boxSelectionEnabled: false,
    style: [
      {
        selector: "node",
        style: {
          "shape": "ellipse",
          "width": 18,
          "height": 18,
          "background-color": "data(color)",
          "border-width": 0,
          "label": "data(label)",
          "color": COLOR_TEXT,
          "font-size": 11,
          "line-height": 1.2,
          "text-wrap": "wrap",
          "text-max-width": "120px",
          "text-valign": "bottom",
          "text-halign": "center",
          "text-margin-y": 4,
          "text-outline-color": COLOR_BG,
          "text-outline-width": 2,
          "transition-property": "opacity",
          "transition-duration": "120ms"
        }
      },
      {
        selector: "node.expanded",
        style: { "width": 22, "height": 22, "border-width": 3, "border-color": "#ffffff", "border-opacity": 0.35 }
      },
      {
        selector: "node.focus",
        style: { "width": 30, "height": 30, "border-width": 3, "border-color": "#ffffff", "border-opacity": 0.9, "font-weight": "bold" }
      },
      {
        selector: "node:selected",
        style: { "border-width": 3, "border-color": "#4da3ff", "border-opacity": 1 }
      },
      {
        selector: "edge",
        style: {
          "width": 1.6,
          "line-color": "data(color)",
          "target-arrow-color": "data(color)",
          "source-arrow-color": "data(color)",
          "target-arrow-shape": "triangle",
          "source-arrow-shape": "none",
          "arrow-scale": 0.9,
          "curve-style": "bezier",
          "opacity": "data(opacity)",
          "transition-property": "opacity",
          "transition-duration": "120ms"
        }
      },
      {
        // Чтение: данные идут из регистра (to) к читателю (from), стрелка
        // у читателя. source/target ребра остаются from/to API.
        selector: "edge.read",
        style: { "line-style": "dashed", "line-dash-pattern": [6, 4], "target-arrow-shape": "none", "source-arrow-shape": "triangle" }
      },
      {
        // Ребро расширения: цвет линии остаётся цветом вида связи, слой
        // показан подсветкой вокруг линии. Ярче: связь, которой нет в raw.
        selector: "edge.ext",
        style: { "underlay-color": COLOR_EXT, "underlay-padding": 3, "underlay-opacity": 0.22 }
      },
      { selector: "edge.added", style: { "underlay-opacity": 0.6, "width": 2.4 } },
      // В2: кросс-базовое ребро и узлы другой базы или внешнего адресата.
      { selector: "edge.http", style: { "line-style": "dashed", "line-dash-pattern": [2, 3], "width": 2 } },
      { selector: "node.foreign", style: { "shape": "round-rectangle", "border-width": 2, "border-style": "dashed", "border-color": "#7dcfff" } },
      { selector: "node.external", style: { "shape": "diamond", "background-color": "#8b929c" } },
      { selector: "edge.hl", style: { "width": 3, "opacity": 1, "z-index": 10 } },
      { selector: "node.hl", style: { "font-weight": "bold", "z-index": 10 } },
      { selector: ".faded", style: { "opacity": 0.12 } },
      { selector: ".hidden-filter", style: { "display": "none" } }
    ]
  });

  var emptyEl = document.getElementById("empty");
  function updateEmpty() { emptyEl.hidden = cy.nodes().nonempty(); }

  // ---- узлы и рёбра --------------------------------------------------
  function badgeText(badges) {
    if (!badges || !badges.length) return "";
    return badges.map(function (b) { return (BADGE_SYMBOL[b.badge] || "?") + b.count; }).join(" ");
  }

  // Подпись узла: имя с переносами на стыках слов и вторая строка с видом,
  // счётчиком «показано/всего» (если на карте не всё) и бейджами.
  function refreshLabel(el) {
    var d = el.data();
    var card = state.cards[d.objectId];
    var parts = [mtypeShort(d.mtype)];
    if (card) {
      var total = (card.fanIn || 0) + (card.fanOut || 0);
      var shown = el.connectedEdges().length;
      if (total > shown) parts.push(shown + "/" + total);
      var b = badgeText(card.badges);
      if (b) parts.push(b);
    }
    if (d.httpDynamic) parts.push(BADGE_SYMBOL["has-dynamic-http"] + d.httpDynamic);
    el.data("label", breakCamel(d.nameDisplay || ("#" + d.objectId)) + "\n" + parts.join(" "));
  }

  function ensureNode(objectId, mtype, display, near) {
    var id = String(objectId);
    var el = cy.getElementById(id);
    if (el.nonempty()) return el;
    var pos = near && near.nonempty() ? { x: near.position("x"), y: near.position("y") } : { x: 0, y: 0 };
    el = cy.add({
      group: "nodes",
      data: { id: id, objectId: objectId, mtype: mtype, nameDisplay: display, color: mtypeColor(mtype), label: "" },
      position: pos
    });
    refreshLabel(el);
    return el;
  }

  function applyCard(item) {
    state.cards[item.objectId] = item;
    var el = cy.getElementById(String(item.objectId));
    if (el.nonempty()) {
      el.data({ mtype: item.mtype, nameDisplay: item.nameDisplay, color: mtypeColor(item.mtype) });
      refreshLabel(el);
    }
  }

  function edgeOpacity(confidence) {
    return 0.4 + 0.6 * Math.max(0, Math.min(1, confidence));
  }

  function addEdge(item) {
    var id = "e:" + item.id;
    if (cy.getElementById(id).nonempty()) return;
    var info = KIND_INFO[item.kind];
    cy.add({
      group: "edges",
      data: {
        id: id, edgeId: item.id, source: String(item.fromObjectId), target: String(item.toObjectId),
        kind: item.kind, confidence: item.confidence, provenance: item.provenance, mode: item.mode,
        layer: item.layer || "", layers: item.layers || [], diff: item.diff || "",
        color: info ? info.color : "#888", opacity: edgeOpacity(item.confidence)
      },
      classes: [info && info.read ? "read" : "", (item.layers || [item.layer]).some(isExtLayer) ? "ext" : "", item.diff === "added" ? "added" : ""].join(" ").trim()
    });
  }

  function isExtLayer(layer) { return !!layer && layer !== "base"; }

  // Слой ребра словами: для подсказки и панели evidence.
  function layerText(d) {
    var layers = d.layers && d.layers.length ? d.layers : [d.layer || "base"];
    var ext = layers.filter(isExtLayer);
    if (!ext.length) return "слой base";
    if (d.diff === "added") return "расширение " + ext.join(", ") + ": новая связь (в raw её нет)";
    return "слои " + layers.join(", ") + ": связь есть в базе, расширение её повторяет";
  }

  function applyFilters() {
    cy.edges().forEach(function (e) {
      var d = e.data();
      e.toggleClass("hidden-filter", !state.kinds[d.kind] || d.confidence < state.minConfidence);
    });
  }

  // ---- карточка узла -------------------------------------------------
  // Карточки соседей грузятся очередью не больше CARD_PARALLEL за раз:
  // каждый HTTP-запрос открывает индекс заново (doc.go), и пачка из 30
  // одновременных открытий рядом с работающим MCP-сервером ловила
  // SQLITE_BUSY. Сбой повторяется один раз; фоновая карточка без бейджей
  // не повод для красной строки статуса.
  var CARD_PARALLEL = 4;
  var cardQueue = [], cardActive = 0;

  function pumpCards() {
    while (cardActive < CARD_PARALLEL && cardQueue.length) {
      var job = cardQueue.shift();
      cardActive++;
      job().then(function () { cardActive--; pumpCards(); });
    }
  }

  function fetchCard(objectId, attempt) {
    return fetchJSON("/api/node/" + objectId, { view: state.view }).catch(function (err) {
      if (attempt > 0) throw err;
      return new Promise(function (r) { setTimeout(r, 400); }).then(function () { return fetchCard(objectId, 1); });
    });
  }

  function loadCard(objectId, quiet) {
    if (state.cards[objectId]) return Promise.resolve(state.cards[objectId]);
    if (state.cardPending[objectId]) return state.cardPending[objectId];
    var gen = state.gen;
    var p = new Promise(function (resolve) {
      // Карточка, которую ждёт человек (клик, поиск), встаёт в начало очереди.
      cardQueue[quiet ? "push" : "unshift"](function () {
        if (gen !== state.gen) { resolve(undefined); return Promise.resolve(); }
        return fetchCard(objectId, 0).then(function (resp) {
          var item = resp.items && resp.items[0];
          if (gen !== state.gen) { resolve(undefined); return; }
          if (item) applyCard(item);
          resolve(item);
        }).catch(function (err) {
          if (gen === state.gen) {
            if (quiet) console.warn("карточка узла " + objectId + ": " + err.message);
            else setStatus("узел " + objectId + ": " + err.message, true);
          }
          resolve(undefined);
        }).then(function () { if (gen === state.gen) delete state.cardPending[objectId]; });
      });
      pumpCards();
    });
    state.cardPending[objectId] = p;
    return p;
  }

  function renderNodePanel(objectId) {
    var el = document.getElementById("nodePanel");
    var node = cy.getElementById(String(objectId));
    if (node.empty()) return;
    var d = node.data();
    var card = state.cards[objectId];
    var html = "<h2>Узел</h2>";
    html += "<div class='card-name'>" + escapeHTML(d.nameDisplay) + "</div>";
    html += "<div class='card-sub'><span style='color:" + mtypeColor(d.mtype) + "'>●</span> " + escapeHTML(mtypeFull(d.mtype)) +
      (card && card.synonym ? " · " + escapeHTML(card.synonym) : "") + "</div>";
    html += "<dl class='kv'>";
    if (card) {
      html += "<dt>Входящих</dt><dd>" + card.fanIn + "</dd>";
      html += "<dt>Исходящих</dt><dd>" + card.fanOut + "</dd>";
    }
    html += "<dt>На карте</dt><dd>" + node.connectedEdges().length + " связей</dd>";
    if (card) {
      var comps = card.components && card.components.length ? card.components.join(", ") : card.component;
      html += "<dt>Компонент</dt><dd>" + escapeHTML(comps) + "</dd>";
    }
    html += "<dt>id</dt><dd>" + objectId + "</dd>";
    html += "</dl>";

    var ex = state.expanded[objectId];
    var actions = "<div class='card-actions'>";
    if (!ex) {
      actions += "<button class='primary' data-act='expand'>Раскрыть соседей</button>";
    } else if (ex.nextCursor) {
      actions += "<button class='primary' data-act='expand'>Догрузить ещё (" + ex.loaded + " из " + ex.total + ")</button>";
    } else {
      actions += "<span class='hint'>Соседи раскрыты: " + ex.loaded + " из " + ex.total + ".</span>";
    }
    actions += "<button data-act='focus'>В центр</button></div>";
    html += actions;

    if (card && card.extensionEdges && card.extensionEdges.length) {
      html += "<div class='ext-list'><div class='muted'>Связи от расширений" + (state.view === "diff" ? " (новые)" : "") + ":</div>";
      card.extensionEdges.forEach(function (x, i) {
        var info = KIND_INFO[x.kind];
        var verb = info ? info.verb : x.kind;
        var text = x.direction === "out"
          ? verb + " " + mtypeShort(x.otherMType) + " " + x.otherDisplay
          : mtypeShort(x.otherMType) + " " + x.otherDisplay + " " + verb + " этот объект";
        html += "<div class='ext-row' data-ext='" + i + "' title='открыть " + escapeHTML(x.otherDisplay) + "'><span class='tag'>" +
          escapeHTML(x.layer) + "</span><span>" + escapeHTML(text) + (x.diff === "added" ? " <b>+</b>" : "") + "</span></div>";
      });
      html += "</div>";
    }
    if (card && card.badges && card.badges.length) {
      html += "<div style='margin-top:10px;'>";
      card.badges.forEach(function (b) {
        html += "<div class='badge-row'><span>" + (BADGE_SYMBOL[b.badge] || "?") + "</span><span>" +
          escapeHTML(b.badge) + " ×" + b.count + ": " + escapeHTML(BADGE_TEXT[b.badge] || "") + "</span></div>";
      });
      html += "</div>";
    }
    el.innerHTML = html;
    el.querySelectorAll("[data-ext]").forEach(function (row) {
      row.onclick = function () {
        var x = card.extensionEdges[Number(row.getAttribute("data-ext"))];
        if (x) openAndExpand(x.otherObjectId);
      };
    });
    var btnExpand = el.querySelector("[data-act=expand]");
    if (btnExpand) btnExpand.onclick = function () { expand(objectId); };
    el.querySelector("[data-act=focus]").onclick = function () { state.focus = objectId; markFocus(); runLayout(); };
  }

  function selectNode(objectId) {
    state.selected = objectId;
    cy.$(":selected").unselect();
    var el = cy.getElementById(String(objectId));
    if (el.nonempty()) el.select();
    renderNodePanel(objectId);
    loadCard(objectId).then(function () {
      if (state.selected === objectId) renderNodePanel(objectId);
    });
  }

  function markFocus() {
    cy.nodes(".focus").removeClass("focus");
    if (state.focus != null) cy.getElementById(String(state.focus)).addClass("focus");
  }

  // ---- раскрытие соседей ---------------------------------------------
  // Первый вызов берёт первую страницу, следующие догружают по курсору.
  // Когда всё загружено, запроса нет: узел только становится фокусом.
  function expand(objectId) {
    var ex = state.expanded[objectId];
    state.focus = objectId;
    markFocus();
    if (ex && (ex.busy || !ex.nextCursor)) {
      runLayout();
      if (state.selected === objectId) renderNodePanel(objectId);
      return Promise.resolve();
    }
    // limit закреплён за узлом с первой страницы: курсор API привязан к нему,
    // а neighborPage() меняется вместе с размером окна.
    ex = state.expanded[objectId] = ex || { nextCursor: "", total: 0, loaded: 0, limit: neighborPage() };
    var params = { dir: "both", limit: String(ex.limit), view: state.view };
    if (ex.nextCursor) params.cursor = ex.nextCursor;
    ex.busy = true;
    setStatus("загружаю соседей…");
    var gen = state.gen;
    return fetchJSON("/api/neighbors/" + objectId, params).then(function (resp) {
      if (gen !== state.gen) return;
      var center = cy.getElementById(String(objectId));
      var touched = {};
      (resp.items || []).forEach(function (e) {
        ensureNode(e.fromObjectId, e.fromMType, e.fromDisplay, center);
        ensureNode(e.toObjectId, e.toMType, e.toDisplay, center);
        addEdge(e);
        touched[e.fromObjectId] = true;
        touched[e.toObjectId] = true;
      });
      ex.loaded += (resp.items || []).length;
      ex.total = resp.totalCount;
      ex.nextCursor = resp.nextCursor || "";
      center.addClass("expanded");
      applyFilters();
      Object.keys(touched).forEach(function (id) { refreshLabel(cy.getElementById(id)); });
      updateEmpty();
      runLayout();
      setStatus(ex.nextCursor ? "показано " + ex.loaded + " из " + ex.total + " связей узла, остальные: «Догрузить ещё» в карточке" : viewNote(resp, ex.total));
      if (state.selected === objectId) renderNodePanel(objectId);
      // Карточки новых узлов (бейджи и степени) догружаются по одной на узел
      // и только один раз за сессию карты.
      Object.keys(touched).forEach(function (id) { loadCard(Number(id), true); });
    }).catch(function (err) {
      if (gen === state.gen) setStatus("соседи узла " + objectId + ": " + err.message, true);
    }).then(function () {
      ex.busy = false;
    });
  }

  // Строка статуса о режиме: без расширений effective и diff честно
  // совпадают с raw или пусты, это надо сказать, а не показать пустоту.
  function viewNote(resp, total) {
    var noExt = (resp.warnings || []).filter(function (w) { return w.code === "no_extensions"; })[0];
    if (noExt) return noExt.message;
    if (!total && state.view === "diff") return "расширения не добавили этому узлу связей";
    return "";
  }

  // Открыть объект по id: карточка, узел на карте и сразу соседи.
  function openAndExpand(objectId) {
    setStatus("");
    var gen = state.gen;
    return loadCard(objectId).then(function (item) {
      if (!item || gen !== state.gen) return;
      ensureNode(item.objectId, item.mtype, item.nameDisplay);
      applyCard(item);
      updateEmpty();
      selectNode(item.objectId);
      return expand(item.objectId);
    });
  }

  // ---- раскладка -----------------------------------------------------
  // Каждый раскрытый узел («центр») получает свои кольца соседей; фокус
  // (последний раскрытый) стоит в начале координат. Кто чей: обход в ширину
  // от фокуса, сосед принадлежит центру, через который до него дошли первым.
  // Любое ребро карты касается раскрытого узла (рёбра приходят только из
  // раскрытия), поэтому у каждого нераскрытого узла такой центр есть.
  // Дочерние центры выносятся за кольца родителя веером, дальше от фокуса,
  // а общие соседи двух центров встают на сторону, обращённую к другому.
  //
  // Кольца считаются здесь и применяются встроенной раскладкой preset, а не
  // concentric: concentric берёт квадратную ячейку max(ширина, высота), а
  // подпись узла вдвое шире, чем выше, и на 30 соседях вписанный масштаб
  // падал до 0.68 (шрифт около 7 px). Здесь кольца строятся в нормированных
  // координатах (ячейка = ширина x высота подписи) и растягиваются под
  // пропорции холста: растяжение только увеличивает зазоры. Один общий
  // concentric по расстоянию от фокуса проверен тоже: соседи второго
  // раскрытого узла разлетались по всему кругу, и его рёбра шли через центр.
  function runLayout() {
    var nodes = cy.nodes();
    if (nodes.empty()) return;
    var focus = state.focus != null ? cy.getElementById(String(state.focus)) : cy.collection();
    if (focus.empty()) focus = nodes[0];
    var fid = focus.id();
    function isHub(id) { return id === fid || !!state.expanded[id]; }

    // Обход в ширину по видимым рёбрам от корня; сразу раздаёт соседей
    // центрам. Корень: фокус, затем каждый раскрытый узел, до которого от
    // уже разложенных не дойти (открыт отдельно через поиск или god-node).
    var parent = {}, dirKey = {}, seen = {}, owner = {}, leaves = {}, kids = {}, compOf = {};
    function grow(root) {
      var bfs = [];
      seen[root] = true;
      var queue = [cy.getElementById(root)];
      while (queue.length) {
        var n = queue.shift();
        var nid = n.id();
        bfs.push(nid);
        n.connectedEdges().not(".hidden-filter").forEach(function (e) {
          var other = e.source().id() === nid ? e.target() : e.source();
          var oid = other.id();
          if (seen[oid]) return;
          seen[oid] = true;
          parent[oid] = nid;
          // 0: ребро входит в родителя (сосед пишет или читает его), 1: наоборот.
          dirKey[oid] = e.target().id() === nid ? 0 : 1;
          queue.push(other);
        });
      }
      bfs.forEach(function (id) {
        compOf[id] = root;
        if (id === root || isHub(id)) {
          owner[id] = id;
          leaves[id] = [];
          kids[id] = [];
          if (id !== root) kids[owner[parent[id]]].push(id);
        } else {
          owner[id] = owner[parent[id]];
          leaves[owner[id]].push(id);
        }
      });
      return root;
    }
    var roots = [grow(fid)];
    Object.keys(state.expanded).forEach(function (id) {
      if (!seen[id] && cy.getElementById(id).nonempty()) roots.push(grow(id));
    });

    // Ячейка по 80-му перцентилю размеров, а не по максимуму: одна подпись
    // в пять строк иначе раздувает все кольца и масштаб падает для всех.
    // Редкий высокий узел может задеть соседа, на это есть подсветка при
    // наведении и зум.
    var spacing = 14;
    var ws = [], hs = [];
    nodes.forEach(function (n) {
      var dim = n.layoutDimensions({ nodeDimensionsIncludeLabels: true });
      ws.push(dim.w);
      hs.push(dim.h);
    });
    function pct(arr) { arr.sort(function (a, b) { return a - b; }); return arr[Math.floor((arr.length - 1) * 0.8)]; }
    var cellW = pct(ws) + spacing;
    var cellH = pct(hs) + spacing;
    // Шаг в нормированных координатах больше 1: две соседние по кольцу
    // ячейки под углом 45° иначе задевают друг друга углами.
    var STEP = 1.1;

    function rings(count) {
      var out = [], radius = 0, placed = 0;
      while (placed < count) {
        radius += STEP;
        var cap = Math.max(6, Math.floor(2 * Math.PI * radius / STEP));
        out.push({ radius: radius, cap: cap });
        placed += cap;
      }
      return out;
    }
    var ringsOf = {}, radiusOf = {};
    Object.keys(leaves).forEach(function (h) {
      ringsOf[h] = rings(leaves[h].length);
      radiusOf[h] = ringsOf[h].length ? ringsOf[h][ringsOf[h].length - 1].radius : 0;
    });

    var pos = {};   // нормированные координаты
    var byId = function (id) { return cy.getElementById(id); };
    function cmpLeaves(a, b) {
      var da = dirKey[a] || 0, db = dirKey[b] || 0;
      if (da !== db) return da - db;
      var ma = byId(a).data("mtype") || "", mb = byId(b).data("mtype") || "";
      if (ma !== mb) return ma < mb ? -1 : 1;
      var na = byId(a).data("nameDisplay") || "", nb = byId(b).data("nameDisplay") || "";
      return na < nb ? -1 : na > nb ? 1 : 0;
    }
    function angleTo(from, to) { return Math.atan2(pos[to].y - pos[from].y, pos[to].x - pos[from].x); }

    // Шаг 1: только центры. Дочерние центры веером за кольцами родителя.
    function placeHubs(h, dirIn) {
      var c = pos[h];
      var ks = kids[h], k = ks.length;
      if (!k) return;
      var step, base;
      // От фокуса первый дочерний центр идёт вниз: кольца из широких
      // подписей вытянуты по горизонтали, стопкой они вписываются крупнее.
      if (dirIn === null) { step = 2 * Math.PI / k; base = Math.PI / 2; }
      else { step = k > 1 ? Math.min(Math.PI, (k - 1) * Math.PI / 3) / (k - 1) : 0; base = dirIn - step * (k - 1) / 2; }
      var maxKid = 0;
      ks.forEach(function (kid) { maxKid = Math.max(maxKid, radiusOf[kid]); });
      var dist = radiusOf[h] + maxKid + 1.5 * STEP;
      if (k > 1 && step > 0) dist = Math.max(dist, (2 * maxKid + 1.5 * STEP) / (2 * Math.sin(step / 2)));
      ks.forEach(function (kid, j) {
        var a = base + step * j;
        pos[kid] = { x: c.x + dist * Math.cos(a), y: c.y + dist * Math.sin(a) };
        placeHubs(kid, a);
      });
    }

    // Шаг 2: разведение. Веера разных веток могут наложиться (у god-node
    // много общих соседей); пересекающиеся круги цветков раздвигаются
    // попарно, первый корень (фокус) стоит на месте.
    function separate(hubs) {
      var gap = STEP;
      for (var iter = 0; iter < 200; iter++) {
        var moved = false;
        for (var i = 0; i < hubs.length; i++) {
          for (var j = i + 1; j < hubs.length; j++) {
            var a = pos[hubs[i]], b = pos[hubs[j]];
            var need = radiusOf[hubs[i]] + radiusOf[hubs[j]] + gap;
            var dx = b.x - a.x, dy = b.y - a.y, d = Math.hypot(dx, dy);
            if (d >= need) continue;
            if (d < 1e-6) { dx = 1; dy = 0; d = 1; }
            var push = (need - d) / d;
            var fa = hubs[i] === fid ? 0 : (hubs[j] === fid ? 1 : 0.5);
            a.x -= dx * push * fa; a.y -= dy * push * fa;
            b.x += dx * push * (1 - fa); b.y += dy * push * (1 - fa);
            moved = true;
          }
        }
        if (!moved) break;
      }
    }

    // Шаг 3: соседи кольцами вокруг итоговых центров. Сосед, связанный ещё
    // с одним центром, встаёт на сторону, обращённую к нему.
    function placeLeaves(h) {
      var c = pos[h];
      var pull = {};
      leaves[h].forEach(function (id) {
        byId(id).connectedEdges().not(".hidden-filter").forEach(function (e) {
          var o = e.source().id() === id ? e.target().id() : e.source().id();
          if (o !== h && isHub(o) && pos[o] && pull[id] === undefined) pull[id] = angleTo(h, o);
        });
      });
      var shared = leaves[h].filter(function (id) { return pull[id] !== undefined; });
      var rest = leaves[h].filter(function (id) { return pull[id] === undefined; }).sort(cmpLeaves);
      shared.sort(function (a, b) { return pull[a] - pull[b]; });
      var list = shared.concat(rest);
      var start = -Math.PI / 2;
      var rs = ringsOf[h];
      if (shared.length && rs.length) {
        var s = 0, co = 0;
        shared.forEach(function (id) { s += Math.sin(pull[id]); co += Math.cos(pull[id]); });
        var first = Math.min(shared.length, rs[0].cap, list.length);
        start = Math.atan2(s, co) - (first - 1) / 2 * (2 * Math.PI / Math.min(rs[0].cap, list.length));
      }
      var i = 0;
      rs.forEach(function (r) {
        var chunk = list.slice(i, i + r.cap);
        chunk.forEach(function (id, j) {
          var a = start + 2 * Math.PI * j / chunk.length;
          pos[id] = { x: c.x + r.radius * Math.cos(a), y: c.y + r.radius * Math.sin(a) };
        });
        i += r.cap;
      });
    }

    // Несвязанные куски встают правее предыдущих, по центру по вертикали.
    var hubs = Object.keys(leaves);
    var rightEdge = null;
    roots.forEach(function (root) {
      pos[root] = { x: 0, y: 0 };
      placeHubs(root, null);
      var e = { x1: Infinity, x2: -Infinity, y1: Infinity, y2: -Infinity };
      hubs.forEach(function (h) {
        if (compOf[h] !== root) return;
        e.x1 = Math.min(e.x1, pos[h].x - radiusOf[h]); e.x2 = Math.max(e.x2, pos[h].x + radiusOf[h]);
        e.y1 = Math.min(e.y1, pos[h].y - radiusOf[h]); e.y2 = Math.max(e.y2, pos[h].y + radiusOf[h]);
      });
      if (rightEdge !== null) {
        var dx = rightEdge + 2 * STEP - e.x1, dy = -(e.y1 + e.y2) / 2;
        hubs.forEach(function (h) {
          if (compOf[h] === root) { pos[h].x += dx; pos[h].y += dy; }
        });
        e.x2 += dx;
      }
      rightEdge = e.x2;
    });
    separate(hubs);
    hubs.forEach(placeLeaves);

    // Узлы вне связи с фокусом (все их рёбра скрыты фильтром): внешнее кольцо.
    var lost = [];
    nodes.forEach(function (n) { if (!pos[n.id()]) lost.push(n.id()); });
    if (lost.length) {
      var far = 0;
      Object.keys(pos).forEach(function (id) { far = Math.max(far, Math.hypot(pos[id].x, pos[id].y)); });
      var i = 0, radius = far;
      while (i < lost.length) {
        radius += STEP;
        var cap = Math.max(6, Math.floor(2 * Math.PI * radius / STEP));
        var chunk = lost.slice(i, i + cap);
        chunk.forEach(function (id, j) {
          var a = -Math.PI / 2 + 2 * Math.PI * j / chunk.length;
          pos[id] = { x: radius * Math.cos(a), y: radius * Math.sin(a) };
        });
        i += cap;
      }
    }

    // Растяжение под пропорции холста по габариту всей раскладки: только
    // увеличивает зазоры, зато вписанный масштаб больше.
    var x1 = Infinity, x2 = -Infinity, y1 = Infinity, y2 = -Infinity;
    Object.keys(pos).forEach(function (id) {
      x1 = Math.min(x1, pos[id].x); x2 = Math.max(x2, pos[id].x);
      y1 = Math.min(y1, pos[id].y); y2 = Math.max(y2, pos[id].y);
    });
    var bw = (x2 - x1 + 1) * cellW, bh = (y2 - y1 + 1) * cellH;
    var aspect = cy.width() / Math.max(1, cy.height());
    var sx = cellW, sy = cellH;
    if (bw / bh < aspect) sx = cellW * aspect * bh / bw; else sy = cellH * bw / (aspect * bh);
    var mx = (x1 + x2) / 2, my = (y1 + y2) / 2;
    var ext = cy.extent();
    var cx = (ext.x1 + ext.x2) / 2, cyy = (ext.y1 + ext.y2) / 2;
    nodes.layout({
      name: "preset",
      positions: function (n) { var p = pos[n.id()]; return { x: cx + (p.x - mx) * sx, y: cyy + (p.y - my) * sy }; },
      animate: nodes.length <= 400,
      animationDuration: 250,
      fit: false,
      stop: function () { fitView(false); }
    }).run();
  }

  // Вписать видимое в экран, но не раздувать одинокий узел до максимума.
  function fitView(animate) {
    var eles = cy.elements().not(".hidden-filter");
    if (eles.empty()) return;
    var opts = { fit: { eles: eles, padding: 40 } };
    if (!animate) {
      cy.fit(eles, 40);
      if (cy.zoom() > 1.4) { cy.zoom(1.4); cy.center(eles); }
      return;
    }
    cy.animate(opts, {
      duration: 200,
      complete: function () { if (cy.zoom() > 1.4) cy.animate({ zoom: 1.4, center: { eles: eles } }, { duration: 120 }); }
    });
  }

  // ---- подсветка и подсказки ----------------------------------------
  var tipEl = document.getElementById("tip");
  function showTip(html, evt) {
    tipEl.innerHTML = html;
    tipEl.hidden = false;
    var t = evt.target;
    var p = evt.renderedPosition || (t.isNode() ? t.renderedPosition() : t.renderedMidpoint());
    var wrap = document.getElementById("cyWrap").getBoundingClientRect();
    var x = p.x + 14, y = p.y + 14;
    var w = tipEl.offsetWidth, h = tipEl.offsetHeight;
    if (x + w > wrap.width - 8) x = Math.max(8, p.x - w - 14);
    if (y + h > wrap.height - 8) y = Math.max(8, p.y - h - 14);
    tipEl.style.left = x + "px";
    tipEl.style.top = y + "px";
  }
  function hideTip() { tipEl.hidden = true; }

  function nodeName(id) {
    var n = cy.getElementById(String(id));
    return n.nonempty() ? n.data("nameDisplay") : "#" + id;
  }

  function edgeSentence(d) {
    var info = KIND_INFO[d.kind];
    var src = cy.getElementById(d.source), dst = cy.getElementById(d.target);
    return escapeHTML(mtypeShort(src.data("mtype")) + " " + src.data("nameDisplay")) + " <b>" +
      escapeHTML(info ? info.verb : d.kind) + "</b> " + escapeHTML(mtypeShort(dst.data("mtype")) + " " + dst.data("nameDisplay"));
  }

  cy.on("mouseover", "node", function (evt) {
    var n = evt.target;
    var hood = n.closedNeighborhood();
    cy.elements().not(hood).addClass("faded");
    hood.nodes().addClass("hl");
    n.connectedEdges().addClass("hl");
    var d = n.data();
    var card = state.cards[d.objectId];
    var html = "<div class='t-title'>" + escapeHTML(d.nameDisplay) + "</div><div class='t-meta'>" + escapeHTML(mtypeFull(d.mtype)) +
      (card && card.synonym ? " · " + escapeHTML(card.synonym) : "") + "</div>";
    if (card) html += "<div class='t-meta'>входящих " + card.fanIn + ", исходящих " + card.fanOut + ", на карте " + n.connectedEdges().length + "</div>";
    html += "<div class='t-meta'>" + (state.expanded[d.objectId] ? "клик: карточка" : "двойной клик: раскрыть соседей") + "</div>";
    showTip(html, evt);
  });
  cy.on("mouseout", "node", function () {
    cy.elements().removeClass("faded hl");
    hideTip();
  });
  cy.on("mouseover", "edge", function (evt) {
    var e = evt.target;
    e.addClass("hl");
    var d = e.data();
    var info = KIND_INFO[d.kind];
    showTip("<div>" + edgeSentence(d) + "</div><div class='t-meta'>" + escapeHTML(d.kind) + (info ? ": " + escapeHTML(info.title) : "") +
      "</div><div class='t-meta'>confidence " + Number(d.confidence).toFixed(2) + " · " + escapeHTML(d.provenance) +
      (d.mode ? " · " + escapeHTML(d.mode) : "") + "</div><div class='t-meta'>" + escapeHTML(layerText(d)) +
      "</div><div class='t-meta'>клик: evidence</div>", evt);
  });
  cy.on("mouseout", "edge", function (evt) {
    evt.target.removeClass("hl");
    hideTip();
  });
  cy.on("viewport", hideTip);

  cy.on("tap", "node", function (evt) {
    var d = evt.target.data();
    if (d.foreign || d.external) { renderHTTPNodePanel(evt.target); return; }
    selectNode(d.objectId);
  });
  cy.on("dbltap", "node", function (evt) {
    hideTip();
    var d = evt.target.data();
    if (d.foreign || d.external) return; // соседей чужой базы карта этого проекта не раскрывает
    expand(d.objectId);
  });
  cy.on("tap", "edge", function (evt) {
    var d = evt.target.data();
    if (d.kind === "http-call") { showHTTPEdge(d); return; }
    showEdgeEvidence(d);
  });

  // ---- HTTP-связи между базами (веха В2, ADR-039) ------------------
  // /api/crosslinks сшивает HTTP-вызовы открытых проектов с их HTTP-сервисами.
  // Узел своего проекта встаёт на карту своим id, узел другой базы и внешний
  // адресат получают составной id: пространства id у баз разные.
  function httpNode(ref, near) {
    if (ref.project === state.project) return ensureNode(ref.id, ref.type, ref.name, near);
    var id = "p:" + ref.project + ":" + ref.id;
    var el = cy.getElementById(id);
    if (el.nonempty()) return el;
    el = cy.add({ group: "nodes", classes: "foreign",
      data: { id: id, objectId: null, foreign: true, project: ref.project, mtype: ref.type,
        nameDisplay: "[" + ref.project + "] " + ref.name, color: mtypeColor(ref.type), label: "" },
      position: near && near.nonempty() ? { x: near.position("x") + 40, y: near.position("y") + 40 } : { x: 0, y: 0 } });
    refreshLabel(el);
    return el;
  }

  function externalNode(host, near) {
    var id = "ext:" + (host || "?");
    var el = cy.getElementById(id);
    if (el.nonempty()) return el;
    el = cy.add({ group: "nodes", classes: "external",
      data: { id: id, objectId: null, external: true, mtype: "",
        nameDisplay: host ? "внешний HTTP: " + host : "внешний HTTP (адресат не найден)", color: "#8b929c", label: "" },
      position: near && near.nonempty() ? { x: near.position("x") - 40, y: near.position("y") + 40 } : { x: 0, y: 0 } });
    el.data("label", el.data("nameDisplay"));
    return el;
  }

  function loadHTTPLinks() {
    setStatus("загружаю HTTP-связи…");
    var gen = state.gen;
    return fetchJSON("/api/crosslinks").then(function (resp) {
      if (gen !== state.gen) return;
      var item = resp.items && resp.items[0];
      if (!item) return;
      (item.links || []).forEach(function (l) {
        var from = httpNode(l.from);
        var to = l.to ? httpNode(l.to, from) : externalNode(l.externalHost, from);
        var id = "h:" + l.id;
        if (cy.getElementById(id).nonempty()) return;
        cy.add({ group: "edges", classes: "http",
          data: { id: id, source: from.id(), target: to.id(), kind: "http-call", confidence: l.confidence || 0,
            provenance: "code", layer: "", layers: [], diff: "", http: l,
            color: l.external ? "#8b929c" : KIND_INFO["http-call"].color, opacity: edgeOpacity(l.confidence || 0.3) } });
      });
      (item.badges || []).forEach(function (b) {
        var el = httpNode(b.node);
        el.data("httpDynamic", b.count);
        el.data("httpBadge", b);
        refreshLabel(el);
      });
      applyFilters();
      runLayout();
      var ext = (item.links || []).filter(function (l) { return l.external; }).length;
      setStatus("HTTP-связей " + (item.links || []).length + " (внешних " + ext + "), объектов с вычисляемым адресом " +
        (item.badges || []).length + "; проекты: " + (item.projects || []).join(", ") +
        ((resp.warnings || []).length ? "; " + resp.warnings.map(function (w) { return w.message; }).join("; ") : ""));
    }).catch(function (err) { setStatus("HTTP-связи: " + err.message, true); });
  }

  function callSitesHTML(calls) {
    return (calls || []).map(function (c) {
      return "<div class='evidence-chain-step'>" + escapeHTML(c.file + ":" + c.line) + (c.symbol ? " " + escapeHTML(c.symbol) : "") +
        (c.verb ? " " + escapeHTML(c.verb) : "") + (c.host ? " " + escapeHTML(c.host) : "") +
        (c.path ? " " + escapeHTML(c.path) : "") + " <span class='muted'>(" + escapeHTML(c.pathKind) + (c.attributed ? "" : ", вызов в модуле без вызывающих объектов") + ")</span></div>";
    }).join("");
  }

  function showHTTPEdge(d) {
    var l = d.http;
    var panel = document.getElementById("edgePanel");
    var html = "<div><b>" + escapeHTML(mtypeShort(l.from.type) + " " + l.from.name) + "</b> [" + escapeHTML(l.from.project) + "] вызывает по HTTP " +
      (l.to ? "<b>" + escapeHTML(mtypeShort(l.to.type) + " " + l.to.name) + "</b> [" + escapeHTML(l.to.project) + "]"
        : "<b>внешний адресат</b> " + escapeHTML(l.externalHost || "(адрес вычисляется)")) + "</div>";
    html += "<div class='muted' style='margin-top:4px;'>http-call · причина " + escapeHTML(l.reason) + (l.to ? " · confidence " + Number(l.confidence).toFixed(2) : "") + "</div>";
    if (l.endpoints && l.endpoints.length) {
      html += "<div style='margin-top:8px;'>Обработчики:</div>" + l.endpoints.map(function (e) {
        return "<div class='evidence-chain-step'>" + escapeHTML((e.httpMethod || "?") + " " + e.template + " → " + (e.handler || "(без обработчика)")) + "</div>";
      }).join("");
    }
    html += "<div style='margin-top:8px;'>Места вызова:</div>" + callSitesHTML(l.calls);
    document.getElementById("edgeContent").innerHTML = html;
    panel.hidden = false;
  }

  function renderHTTPNodePanel(node) {
    var d = node.data();
    var html = "<h2>Узел</h2><div class='card-name'>" + escapeHTML(d.nameDisplay) + "</div>";
    html += "<div class='card-sub'>" + (d.external ? "адресат вне открытых проектов" : escapeHTML(mtypeFull(d.mtype)) + ", проект " + escapeHTML(d.project)) + "</div>";
    html += "<div class='hint'>Узел другой базы: выберите её проект вверху, чтобы раскрыть соседей.</div>";
    document.getElementById("nodePanel").innerHTML = html;
  }

  document.getElementById("httpBtn").addEventListener("click", loadHTTPLinks);

  // ---- поиск ---------------------------------------------------------
  var searchEl = document.getElementById("search");
  var resultsEl = document.getElementById("results");
  var search = { timer: null, ctrl: null, items: [], active: -1 };

  function closeResults() { resultsEl.hidden = true; search.active = -1; }

  function renderResults(items, note) {
    search.items = items;
    search.active = items.length ? 0 : -1;
    if (!items.length) {
      resultsEl.innerHTML = "<div class='result empty'>" + escapeHTML(note || "ничего не найдено") + "</div>";
      resultsEl.hidden = false;
      return;
    }
    var html = "";
    items.forEach(function (it, i) {
      var deg = it.fanIn != null ? it.fanIn + " вх · " + it.fanOut + " исх" : "";
      html += "<div class='result" + (i === 0 ? " active" : "") + "' data-i='" + i + "'>" +
        "<span class='dot' style='background:" + mtypeColor(it.mtype) + "'></span>" +
        "<div><div class='name'>" + escapeHTML(it.nameDisplay) + "</div><div class='meta'>" +
        (it.mtype ? escapeHTML(mtypeFull(it.mtype)) : "по числовому id") + (it.synonym ? " · " + escapeHTML(it.synonym) : "") +
        (it.component && it.component !== "cfg" ? " · " + escapeHTML(it.component) : "") + "</div></div>" +
        "<span class='deg' title='входящих и исходящих связей'>" + deg + "</span></div>";
    });
    if (note) html += "<div class='result empty'>" + escapeHTML(note) + "</div>";
    resultsEl.innerHTML = html;
    resultsEl.hidden = false;
  }

  function setActive(i) {
    var rows = resultsEl.querySelectorAll(".result[data-i]");
    if (!rows.length) return;
    search.active = (i + rows.length) % rows.length;
    rows.forEach(function (r, k) { r.classList.toggle("active", k === search.active); });
    rows[search.active].scrollIntoView({ block: "nearest" });
  }

  function choose(i) {
    var it = search.items[i];
    if (!it) return;
    closeResults();
    searchEl.value = it.nameDisplay;
    searchEl.blur();
    openAndExpand(it.objectId);
  }

  function runSearch() {
    var q = searchEl.value.trim();
    if (search.ctrl) search.ctrl.abort();
    if (!q) { closeResults(); return; }
    var idm = q.match(/^#?(\d+)$/);
    if (idm) {
      renderResults([{ objectId: Number(idm[1]), nameDisplay: "Открыть узел #" + idm[1], mtype: "" }]);
      return;
    }
    search.ctrl = typeof AbortController === "function" ? new AbortController() : null;
    fetchJSON("/api/search", { q: q, limit: "20" }, search.ctrl && search.ctrl.signal).then(function (resp) {
      if (searchEl.value.trim() !== q) return;
      var note = resp.warnings && resp.warnings.length ? "показаны первые " + (resp.items || []).length + ", уточните запрос" : "";
      renderResults(resp.items || [], note);
    }).catch(function (err) {
      if (err.name === "AbortError") return;
      renderResults([], "ошибка поиска: " + err.message);
    });
  }

  searchEl.addEventListener("input", function () {
    clearTimeout(search.timer);
    search.timer = setTimeout(runSearch, 250);
  });
  searchEl.addEventListener("keydown", function (e) {
    if (e.key === "ArrowDown") { e.preventDefault(); if (resultsEl.hidden) runSearch(); else setActive(search.active + 1); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setActive(search.active - 1); }
    else if (e.key === "Enter") {
      e.preventDefault();
      if (!resultsEl.hidden && search.active >= 0) { choose(search.active); return; }
      clearTimeout(search.timer);
      runSearch();
    }
    else if (e.key === "Escape") { closeResults(); }
  });
  searchEl.addEventListener("focus", function () { if (search.items.length && searchEl.value.trim()) resultsEl.hidden = false; });
  resultsEl.addEventListener("mousedown", function (e) {
    var row = e.target.closest(".result[data-i]");
    if (!row) return;
    e.preventDefault();
    choose(Number(row.getAttribute("data-i")));
  });
  document.addEventListener("mousedown", function (e) {
    if (!document.getElementById("searchBox").contains(e.target)) closeResults();
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "/" && document.activeElement !== searchEl && !/INPUT|SELECT|TEXTAREA/.test(document.activeElement.tagName)) {
      e.preventDefault();
      searchEl.focus();
      searchEl.select();
    }
  });

  // ---- кнопки --------------------------------------------------------
  document.getElementById("fitBtn").addEventListener("click", function () { fitView(true); });
  document.getElementById("layoutBtn").addEventListener("click", runLayout);

  function resetMap() {
    state.gen++;
    state.reopenPlan = null;
    cy.elements().remove();
    state.cards = {};
    state.cardPending = {};
    cardQueue = [];
    state.expanded = {};
    state.focus = null;
    state.selected = null;
    setStatus("");
    hideTip();
    updateEmpty();
    renderLegend();
    document.getElementById("nodePanel").innerHTML = "<h2>Узел</h2><div class='hint'>Кликните узел, чтобы увидеть карточку. Двойной клик раскрывает соседей.</div>";
    document.getElementById("edgePanel").hidden = true;
  }
  document.getElementById("resetBtn").addEventListener("click", resetMap);

  // ---- режим слоёв ---------------------------------------------------
  // Смена режима: карточки и соседи других режимов не годятся (степени и
  // набор рёбер другие), поэтому карта очищается и заново раскрывает фокус,
  // затем остальные раскрытые узлы в прежнем порядке.
  var viewButtons = document.querySelectorAll(".view-switch [data-view]");
  function markView() {
    viewButtons.forEach(function (b) { b.classList.toggle("active", b.getAttribute("data-view") === state.view); });
  }
  function setView(v) {
    if (VIEWS.indexOf(v) < 0 || v === state.view) return;
    var reopen = Object.keys(state.expanded).map(Number);
    var focus = state.focus;
    // Прошлое переключение ещё не успело раскрыть карту заново (быстрый
    // двойной клик по режимам): берём его план, иначе узлы потерялись бы.
    if (!reopen.length && focus == null && state.reopenPlan) {
      reopen = state.reopenPlan.ids;
      focus = state.reopenPlan.focus;
    }
    state.view = v;
    markView();
    try {
      var u = new URL(window.location.href);
      u.searchParams.set("view", v);
      window.history.replaceState(null, "", u.toString());
    } catch (e) { /* адрес не обязателен для работы карты */ }
    resetMap();
    if (!godPanel.hidden) loadGodNodes();
    if (focus != null) reopen = [focus].concat(reopen.filter(function (id) { return id !== focus; }));
    var gen = state.gen;
    state.reopenPlan = { ids: reopen, focus: focus };
    var chain = Promise.resolve();
    reopen.forEach(function (id, i) {
      chain = chain.then(function () {
        if (gen !== state.gen) return;
        if (i === 0) return openAndExpand(id);
        if (cy.getElementById(String(id)).nonempty()) return expand(id);
      });
    });
    chain.then(function () {
      if (gen !== state.gen) return;
      state.reopenPlan = null;
      if (focus != null && cy.getElementById(String(focus)).nonempty()) {
        state.focus = focus;
        markFocus();
        runLayout();
      }
    });
  }
  viewButtons.forEach(function (b) {
    b.addEventListener("click", function () { setView(b.getAttribute("data-view")); });
  });

  // ---- фильтры -------------------------------------------------------
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

  // ---- легенда видов -------------------------------------------------
  // Показывает виды, которые сейчас на карте; пустая карта: основные виды.
  function renderLegend() {
    var present = {};
    cy.nodes().forEach(function (n) { present[n.data("mtype")] = true; });
    var list = Object.keys(present);
    if (!list.length) list = ["Document", "AccumulationRegister", "InformationRegister", "Catalog", "DataProcessor", "Report"];
    list.sort(function (a, b) { return mtypeFull(a) < mtypeFull(b) ? -1 : 1; });
    document.getElementById("legendTypes").innerHTML = list.map(function (m) {
      return "<div class='legend-item' title='" + escapeHTML(m) + "'><span class='dot' style='background:" + mtypeColor(m) + "'></span><span>" +
        escapeHTML(mtypeFull(m)) + (MTYPE_INFO[m] && MTYPE_INFO[m][1] !== MTYPE_INFO[m][0] ? " (" + escapeHTML(MTYPE_INFO[m][1]) + ")" : "") + "</span></div>";
    }).join("");
  }
  var legendTimer = null;
  cy.on("add remove", "node", function () {
    clearTimeout(legendTimer);
    legendTimer = setTimeout(renderLegend, 50);
  });

  // ---- god-node ------------------------------------------------------
  var godPanel = document.getElementById("godPanel");
  document.getElementById("godBtn").addEventListener("click", function () {
    godPanel.hidden = !godPanel.hidden;
    document.getElementById("godBtn").classList.toggle("active", !godPanel.hidden);
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
    var gen = state.gen;
    fetchJSON("/api/godnodes", { metric: state.godMetric, top: "20", view: state.view }).then(function (resp) {
      if (gen !== state.gen) return;
      var body = document.getElementById("godBody");
      body.innerHTML = "";
      if (!(resp.items || []).length) {
        body.innerHTML = "<tr><td colspan='3' class='muted'>" + escapeHTML(viewNote(resp, 0) || "нет узлов") + "</td></tr>";
      }
      (resp.items || []).forEach(function (it) {
        var tr = document.createElement("tr");
        tr.className = "clickable";
        tr.title = mtypeFull(it.mtype) + ": открыть и раскрыть соседей";
        tr.innerHTML = "<td class='name'><span style='color:" + mtypeColor(it.mtype) + "'>●</span> " + escapeHTML(it.nameDisplay) +
          " <span class='muted'>" + escapeHTML(mtypeShort(it.mtype)) + "</span></td><td class='num'>" + it.fanIn + "</td><td class='num'>" + it.fanOut + "</td>";
        tr.addEventListener("click", function () { openAndExpand(it.objectId); });
        body.appendChild(tr);
      });
    }).catch(function (err) {
      setStatus("god-node: " + err.message, true);
    });
  }

  // ---- evidence ребра ------------------------------------------------
  function showEdgeEvidence(d) {
    var panel = document.getElementById("edgePanel");
    var content = document.getElementById("edgeContent");
    var info = KIND_INFO[d.kind];
    var head = "<div>" + edgeSentence(d) + "</div><div class='muted' style='margin-top:4px;'>" + escapeHTML(d.kind) +
      (info ? ": " + escapeHTML(info.title) : "") + " · confidence " + Number(d.confidence).toFixed(2) + "</div>" +
      "<div class='muted'>" + escapeHTML(layerText(d)) + "</div>";
    content.innerHTML = head + "<div class='hint' style='margin-top:8px;'>загружаю evidence…</div>";
    panel.hidden = false;
    fetchJSON("/api/edge/" + d.edgeId + "/evidence").then(function (resp) {
      var item = resp.items && resp.items[0];
      if (!item) { content.innerHTML = head + "<div class='hint'>evidence не найдено</div>"; return; }
      var html = head + "<div class='muted'>" + escapeHTML(item.provenance) + "</div>";
      if (item.provenance === "metadata-declared") {
        html += "<div style='margin-top:8px;'>Объявлено в XML: <strong>" + escapeHTML(item.xmlFile || "?") + "</strong></div>";
        if (item.declaredRegister) html += "<div>Регистр: " + escapeHTML(item.declaredRegister) + "</div>";
      } else if (item.chain && item.chain.length) {
        html += "<div style='margin-top:8px;'>Цепочка атрибуции:</div>";
        item.chain.forEach(function (step, i) {
          html += "<div class='evidence-chain-step'>" + (i + 1) + ". " + escapeHTML(step.symbolName || step.symbolUid || "?") +
            (step.module ? " (" + escapeHTML(step.module) + ")" : "") +
            (step.confidence ? ", confidence " + step.confidence.toFixed(2) : "") + "</div>";
        });
      } else {
        html += "<div class='hint' style='margin-top:8px;'>цепочка пуста</div>";
      }
      if (item.files && item.files.length) {
        html += "<div style='margin-top:8px;' class='muted'>Файлы: " +
          item.files.map(function (f) { return escapeHTML(f.relPath); }).join(", ") + "</div>";
      }
      content.innerHTML = html;
    }).catch(function (err) {
      content.innerHTML = head;
      setStatus("evidence ребра " + d.edgeId + ": " + err.message, true);
    });
  }

  // ---- проекты и deep-link -------------------------------------------
  function loadProjects() {
    return fetchJSON("/api/projects").then(function (resp) {
      var projects = resp.items || [];
      var sel = document.getElementById("project");
      sel.innerHTML = "";
      projects.forEach(function (p) {
        var opt = document.createElement("option");
        opt.value = p.project;
        opt.textContent = p.project + (p.needsFullRebuild ? " (нужна пересборка)" : "");
        sel.appendChild(opt);
      });
      sel.hidden = projects.length < 2;
      if (projects.length) {
        state.project = projects[0].project;
        sel.value = state.project;
      }
      sel.addEventListener("change", function () {
        state.project = sel.value;
        resetMap();
        if (!godPanel.hidden) loadGodNodes();
      });
      if (!projects.length) setStatus("нет доступных проектов: запустите mcp1c graph -project <корень workspace>", true);
    }).catch(function (err) {
      setStatus("список проектов: " + err.message, true);
    });
  }

  renderLegend();
  updateEmpty();
  var params = new URLSearchParams(window.location.search);
  if (VIEWS.indexOf(params.get("view")) >= 0) state.view = params.get("view");
  markView();
  loadProjects().then(function () {
    if (params.get("project")) {
      state.project = params.get("project");
      document.getElementById("project").value = state.project;
    }
    var startNode = Number(params.get("node"));
    if (startNode > 0) {
      openAndExpand(startNode);
    } else {
      searchEl.focus();
    }
  });
})();
