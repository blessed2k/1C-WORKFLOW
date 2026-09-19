# Индексные инструменты: пределы и оговорки

Здесь индексные инструменты называют свои пределы: что знает индекс и что он сознательно не
моделирует. Общий обзор инструментов и порядок подключения есть в
[README.md](../README.md).

## Поверхность: когда инструменты регистрируются

- Индексные инструменты (14: `get_context_for_task`, `index_status`, `reindex`,
  `find_symbol`, `get_symbol`, `get_module_structure`, `find_references`,
  `trace_call_graph`, `find_impact`, `get_object`, `get_form_handlers`,
  `find_queries_using`, `find_register_writes`, `object_graph`) регистрируются
  только когда реестр проектов открыт: сервер запущен с `--projects-root`
  (`MCP_1C_PROJECTS_ROOT`) и `.mcp1c/registry.json` читается. Без реестра их нет
  в `tools/list` вовсе: иначе их определения занимали бы контекст клиента, а
  ответить они могли бы только `no_active_project`. Решение принимает `newServerWithCloser`
  (`cmd/mcp1c/tools.go`, `finishSurface`).
- Профиль `--tools=full|core` (`MCP_1C_TOOLS`, по умолчанию `full`). `core` не
  регистрирует `dump_diff`, `extension_context`, `command_visibility`,
  `bsp_extension_points`, `list_projects`, `get_configuration_info`
  (`coreExcludedTools`). `exchange_audit`, `new_object_checklist`, `object_graph`
  оставлены: на них ведут цепочки `/not-in-exchange`, `/new-object` и подсказка
  пустого `find_register_writes`. Профиль виден в `server_info.profile`.
- Instructions строятся при `initialize` из фактически зарегистрированных
  инструментов (`buildInstructions`): offline не называет live-инструменты и
  наоборот, без реестра нет секции INDEXED, core не называет выключенные. Правило
  «первым» одно: с индексом `get_context_for_task` на задачу, без индекса
  `context_pack` на объект. Лимит 2048 рун проверяется для каждой комбинации.
- `direction`: `trace_call_graph` принимает `callers`\|`callees` и синонимы
  `in` (= callers) \| `out` (= callees); `object_graph` принимает `in`\|`out`\|`both`
  и синонимы `callers` (= in) \| `callees` (= out). Неизвестное значение:
  `invalid_argument` с перечнем.
- `posting_review` удалён: тот же отчёт отдаёт `get_movements review=true` в поле
  `review` (эквивалентность закреплена `TestGetMovementsReviewMatchesPostingReview`).

## Расширения: слои, перехватчики, effective view

Источник архитектурного решения: `docs/architecture-index.md` §13 (ADR-4) и
§20 (raw/effective views).

### Что индекс знает

- Расширение (`kind: extension` в манифесте) индексируется как отдельный
  **слой**: свой `component_id`, свои символы, свои объекты метаданных, свой
  `applyOrder`. Заимствованный (borrowed/adopted) объект — это ОТДЕЛЬНАЯ
  строка `metadata_object` в компоненте расширения, с тем же `mtype+name`,
  что и в базовом слое (identity_key у node уникален per-компонент — раздел
  15 схемы).
- **Перехватчики** (`&Перед`, `&После`, `&Вместо`, `ИзменениеИКонтроль`)
  распознаются по аннотации метода (`internal/parse/bsl` даёт вид
  аннотации — `Method.Annotations`, не хранит аргумент `&Вместо("Имя")`,
  см. `internal/parse/bsl/parse.go:373-380`) и связываются с базовым
  символом **по имени**: перехватчик в модуле расширения, заимствовавшего
  объект, называется так же, как перехватываемый метод — то же
  соглашение, что использовал `internal/source/extension.go:36-45`
  (единственная прежняя реализация, семантика перенесена, файл не
  редактировался).
- **Effective view вычисляется на чтении, не материализуется**
  (архитектурное решение ADR-4: вычисление дешевле поддержки инвалидации).
  Каждый вызов `view=effective` заново читает факты нужных слоёв из store и
  сливает их в `internal/app` (`internal/app/effective.go`,
  `internal/resolve/layer.go`) — отдельной таблицы/факта `intercepts` в
  store НЕТ и не появится в v1 по этой же причине.
- Каждый effective-факт называет свой слой (`layer`/`component`/`applyOrder`
  в выдаче) — ни один факт не «обезличивается» слиянием.
- Конфликт двух и более расширений, перехватывающих один метод через
  `&Вместо`, отдаётся как diagnostic (`Warning{Code: "instead_conflict"}`) с
  ПЕРЕЧИСЛЕНИЕМ ВСЕХ вовлечённых слоёв и `confidence < 1` — не молчаливый
  выбор одного слоя.

### Какие инструменты видят effective view

| Инструмент | `view=effective` даёт |
|---|---|
| `get_object` | Members и Forms слиты по всем применимым слоям (базовый + расширения), каждый — со своим `layer` |
| `get_form_handlers` | параметр принят и валидируется, слияния нет: обработчики форм расширений не сливаются с базовыми (см. предел ниже) |
| `get_symbol` | `intercepts`: перехватчики символа во всех применимых расширениях + diagnostic конфликта |
| `get_module_structure` | то же, что `get_symbol`, но по каждому символу модуля отдельно |
| `find_references`, `trace_call_graph` | Warning о перехватчиках КОРНЕВОГО символа запроса (не по каждой строке выдачи — цена повторного разбора модулей расширений на каждый элемент списка не оправдана) |
| `find_impact` | перехватчики корня как отдельные `ImpactItem` глубины 1, `edgeKind=intercepts` — НЕ обходятся транзитивно (это не ребро `dependency_edge`/`call_edge`, BFS их не видит) |
| `find_queries_using`, `find_register_writes` | параметр принят и валидируется, слияния нет: обе выдачи и так списочные, каждая строка уже несёт свой `component`/`layer` — сливать нечего (в отличие от `get_object`, где raw выбирает ровно одну строку из нескольких слоёв) |
| `index_status` | параметр не добавлен: статус индекса — про свежесть/generation, не про факты конкретного объекта |

`view=raw` (умолчание) не сливает слои:
поведение проверено тестами `internal/app/effective_test.go` и полным
прогоном существующих тестов символьных/графовых/метаданных сервисов.

### Пределы, зафиксированные честно (не долг, решение ADR-4)

- **Runtime-порядок применения расширений из XML не выводится.** Индекс
  знает `applyOrder` из манифеста проекта (`1c-project.json`), но НЕ знает
  порядок, в котором платформа реально применяет расширения к базе —
  конфигуратор может переупорядочить расширения независимо от порядка их
  перечисления где-либо вне `Configuration.xml` конфигурации. Два и более
  `&Вместо` на одном методе поэтому не разрешаются в «победителя» — только
  diagnostic с перечислением слоёв.
- **«На замке» (locked/support) и safe mode заимствования не моделируются.**
  Индекс не знает, разрешено ли расширению фактически изменять
  заимствованный объект (уровень поддержки конфигурации), и не проверяет,
  находится ли расширение в безопасном режиме (нет привилегий, файлов,
  интернета). Effective view
  показывает, ЧТО написано в исходниках слоёв, а не что реально применится
  на живой базе.
- **`find_queries_using`/`find_register_writes` не расширяют фильтр объекта
  на несколько слоёв.** Когда объект передан как `type+name`, фильтр строится
  по ID ровно одной строки `metadata_object` (см. `pickObjectRow`) — запросы
  и доступы, которые резолвер связал со строкой РАСШИРЕНИЯ того же объекта,
  под этим фильтром не найдутся. Требует либо расширения
  `store.QueryReferenceFilter`/`RegisterAccessFilter` до множества id (правка
  `internal/store`), либо подтверждения, что резолвер
  всегда сводит такие ссылки к одной канонической строке — не проверено.
- **`find_references`/`trace_call_graph`/`find_impact` не платят цену
  повторного разбора модулей расширений за каждый элемент списка/обхода** —
  только за корневой символ запроса (см. таблицу выше). Символ,
  встретившийся В ГЛУБИНЕ обхода/списка и сам являющийся перехватываемым, не
  получает пометку — упрощение по стоимости, не архитектурный предел.
- **Формы расширений** (изменённые/добавленные элементы и команды формы) НЕ
  сливаются: `get_form_handlers` только валидирует `view`, слияния нет. Семантика конфликтов формы
  (`ИзменитьРеквизиты`, `ДобавитьЭлемент`, ...) уже есть в
  `internal/source/formconflicts.go`, но не перенесена на индексный слой —
  открытый пункт.

## Инструменты по отдельности

Ниже по разделу на инструмент. `get_symbol` описан вместе с `find_symbol`, `reindex` вместе
с `index_status`. `object_graph` описан коротко в конце.

### Общее для всех

- **Обёртка ответа** (все успешные вызовы): `{generation, stale, warnings[],
  items[], totalCount, nextCursor}`. `nextCursor` отсутствует, если страниц
  больше нет. `stale=true` → `warnings` обязателен.
  `totalCount` при нуле не приходит вовсе: поле общего конверта помечено
  `omitempty`, отсутствие означает 0 (сделать его обязательным значило бы
  сменить контракт всех индексных инструментов). Пустой список `items`
  приходит как `[]` (в том числе у `find_register_writes`).
- **Ошибка** — `internal/app.Error`, наружу уходит ТОЛЬКО как текст
  `err.Error()` (не JSON-поле `structuredContent` — ограничение
  `go-sdk@v1.6.1`):
  `[code] message (подсказка: hint) [проект: P, generation: G]`. Код — в
  фиксированной позиции в начале строки, парсибелен текстовым разбором,
  не JSON-парсингом.
- **Семь кодов**, каждый реально возвращается хотя бы одним инструментом:
  `no_active_project` (нет активного индексного проекта: `reindex
  projectRoot=<каталог с 1c-project.json>` регистрирует и активирует его;
  `set_dump` активирует проект только тогда, когда выгрузка описана
  компонентом манифеста уже зарегистрированного проекта, иначе подсказка
  называет выгрузку и снова ведёт к `reindex projectRoot`),
  `not_found` (пустой обязательный параметр, неизвестное
  имя/uid, неизвестное значение перечисления вроде
  `kind`/`view`; сообщение называет ближайшие имена, когда они
  есть; неизвестный `direction` отдаёт `invalid_argument` с перечнем
  допустимых значений), `cursor_expired` (курсор с предыдущей страницы построен для
  другого generation или других параметров фильтра), `resource_expired`
  (resource-ссылка на усечённый ответ построена для поколения, которое
  сменилось, или blob вычищен по TTL), `index_not_fresh` (только
  `get_context_for_task` с `freshness=require-fresh`),
  `path_outside_workspace` (валидация путей workspace, не специфична для
  одного инструмента), `component_not_registered` (параметр `component` не
  входит в манифест активного проекта).
- **`view=raw|effective`** принят JSON-схемой у всех десяти инструментов
  кроме `index_status`/`reindex`. Что именно меняет
  `effective` у каждого — таблица в разделе «Расширения: слои, перехватчики,
  effective view» выше; здесь эта информация не дублируется, только
  ссылка. **Исключение**: `find_symbol` принимает `view` в JSON-схеме, но
  нигде его не валидирует (`view=любаяЧушь` проходит молча, без ошибки) —
  асимметрично с остальными девятью, где `parseView` хотя бы валидирует
  формат там, где эффекта нет (открытый дефект:
  `cmd/mcp1c/idx_symbol.go`, `internal/app/symbol.go:FindSymbol`).
- **Пагинация**: `cursor`/`limit` — у `find_symbol`, `find_references`,
  `trace_call_graph`, `find_queries_using`, `find_register_writes`,
  `find_impact`. Курсор непрозрачный (`app.EncodeCursor`/`DecodeCursor`),
  несёт generation и хэш параметров фильтра — смена generation ИЛИ смена
  параметров фильтра между страницами даёт `cursor_expired`, а не молчаливо
  неверную вторую страницу. У `get_object`/`get_form_handlers` пагинации нет
  по дизайну; поведение на объекте с очень большим числом ролей и подписок
  тестом не проверено.

### `find_symbol` / `get_symbol`

**Назначение**: найти BSL-символ (процедуру/функцию/переменную модуля) по
имени или подстроке имени, не читая ни одного модуля; затем прочитать один
найденный символ точно — сигнатуру, параметры со значениями по умолчанию,
директиву, при необходимости тело.

**Когда звать**: `find_symbol` — первый шаг, когда неизвестен точный uid или
модуль; `get_symbol` — после того как `find_symbol` (или `find_references`)
сузил кандидатов до одного uid, либо когда module+name уже известны.

**Вход** `find_symbol`: `name` (обязателен, точное имя или подстрока,
регистронезависимо), `kind` (`procedure`\|`function`\|`variable`), `module`,
`component`, `view`, `limit` (default 50, max 200), `cursor`.
**Вход** `get_symbol`: `uid` ИЛИ (`module`+`name`), `component` (снимает
неоднозначность при заимствовании в несколько компонентов), `view`
(`effective` заполняет `intercepts` + `instead_conflict` diagnostic),
`includeBody` (тело режется из blob индекса, не из файла на диске).

**Выход** `find_symbol` — `SymbolItem[]`: `uid, component, module, kind,
name, signature, span, export, async, directive, intercepts?`. **Выход**
`get_symbol` — `SymbolDetail` (items[0]): то же плюс `parameters[]` (`name,
byVal, hasDefault, default`), `body`, `bodyTruncated`, `bodyResourceUri`,
`staleAgainstDisk` (текст файла на диске разошёлся с blob, по которому
построен ответ — честный сигнал, не подгон).

**Коды ошибок**: `no_active_project`, `not_found` (`name` пуст; `kind`
неизвестен; `get_symbol` без ни uid, ни module+name; символ/модуль не
найден — с ближайшими именами), `component_not_registered`,
`cursor_expired` (только `find_symbol`).

**Пример вызова** `find_symbol`:
```json
{"name": "ПолучитьОстаток", "kind": "function", "limit": 20}
```
**Пример ответа** (усечён):
```json
{
  "generation": "e1.g42", "stale": false, "totalCount": 3,
  "items": [
    {"uid": "cfg:CommonModules/УтилитыОстатков/ПолучитьОстаток",
     "component": "cfg", "module": "CommonModules/УтилитыОстатков/Ext/Module.bsl",
     "kind": "function", "name": "ПолучитьОстаток",
     "signature": "Функция ПолучитьОстаток(Товар, Склад = Неопределено) Экспорт",
     "span": {"startByte": 512, "endByte": 890, "startLine": 14, "endLine": 22},
     "export": true, "directive": ""}
  ]
}
```

**Известные ограничения**: `find_symbol` не заполняет `intercepts` ни у
одной строки (цена повторного разбора модулей расширений на каждый элемент
списка не оправдана) — за перехватчиками конкретного символа нужен
`get_symbol`/`get_module_structure` с `view=effective`; `view` не
валидируется (см. «Общее для всех»); p50 на реальной выгрузке — 22.1мс
против бюджета 20мс (**превышение ~10%, известный долг**, p95 28.1мс, с
запасом против 50мс, `docs/benchmarks.md`).

### `get_module_structure`

**Назначение**: структура модуля целиком — все символы с сигнатурами,
переменные, счётчики, регионы, БЕЗ текста модуля (модуль целиком не
возвращается никогда).

**Когда звать**: перед тем как читать что-либо из конкретного модуля —
увидеть, что в нём вообще есть, прежде чем звать `get_symbol` на нужный.

**Вход**: `module` (обязателен, путь относительно корня компонента),
`component`, `view` (`effective` — перехватчики по каждому символу модуля
отдельно + `instead_conflict`).

**Выход** — `ModuleStructureItem` (items[0]): `module, component, kind,
symbols[] (SymbolItem), variables[], regions[], symbolCount, variableCount`.

**Коды ошибок**: `no_active_project`, `not_found` (`module` пуст; модуль не
найден — с ближайшими путями), `component_not_registered`.

**Пример вызова**:
```json
{"module": "CommonModules/УтилитыОстатков/Ext/Module.bsl"}
```
**Пример ответа** (усечён):
```json
{
  "items": [{
    "module": "CommonModules/УтилитыОстатков/Ext/Module.bsl", "component": "cfg",
    "kind": "common-module",
    "symbols": [{"uid": "...", "name": "ПолучитьОстаток", "kind": "function", "export": true, "signature": "..."}],
    "symbolCount": 6, "variableCount": 0
  }],
  "warnings": [{"code": "regions_not_indexed", "message": "regions не заполняются публикацией (internal/index/publish.go не передаёт Region)"}]
}
```

**Известные ограничения**: `regions` **честно пуст всегда** —
`internal/index/publish.go` не передаёт `Region` в `store.Symbol` (упрощение
пайплайна), и ответ несёт явный warning
`regions_not_indexed`, а не молчаливо пустой список, выданный за «регионов
нет».

### `find_references`

**Назначение**: все ссылки на символ, сгруппированные по модулю, каждая — с
`resolution` (`resolved`\|`ambiguous`\|`unresolved`\|`dynamic`),
`confidence`, и для `ambiguous` ранжированные кандидаты с причиной.

**Когда звать**: перед изменением сигнатуры или переименованием символа —
увидеть каждое место вызова first.

**Вход**: `uid` (обязателен), `kinds` (фильтр по видам ссылок), `component`,
`view` (`effective` — warning о перехватчиках ЦЕЛЕВОГО символа, не по каждой
строке выдачи), `limit` (default 50, max 200), `cursor`.

**Выход** — `ReferenceGroup[]`: `module, component, references[]
(ReferenceItem: kind, span, resolution, confidence, targetClass, targetUid,
platformKey, fromSymbolUid, candidates[])`. Большой результат усекается с
resource link `onec://references/{project}/{uid}{?gen}` на полный список.

**Коды ошибок**: `no_active_project`, `not_found` (`uid` пуст; символ не
найден), `component_not_registered`, `cursor_expired`.

**Пример вызова**:
```json
{"uid": "cfg:CommonModules/УтилитыОстатков/ПолучитьОстаток", "limit": 50}
```
**Пример ответа** (усечён):
```json
{
  "items": [{
    "module": "Documents/РеализацияТоваров/Ext/ObjectModule.bsl", "component": "cfg",
    "references": [{
      "kind": "call", "span": {"startByte": 2201, "endByte": 2231},
      "resolution": "resolved", "confidence": 1.0,
      "targetClass": "symbol", "targetUid": "cfg:CommonModules/УтилитыОстатков/ПолучитьОстаток"
    }]
  }],
  "totalCount": 47
}
```

**Известные ограничения**: `view=effective` предупреждает только про
перехватчики целевого символа запроса — ссылка, найденная В ГЛУБИНЕ списка
и сама являющаяся перехватываемым методом, метку не получает (цена
повторного разбора модулей расширений на каждую строку не оправдана —
раздел «Расширения» выше). p50/p95 на реальной выгрузке — 63мкс/279мкс
против бюджета 50/200мс, с большим запасом.

### `trace_call_graph`

**Назначение**: обход графа вызовов от символа — `callees` (что он
вызывает) или `callers` (кто вызывает его) — на ограниченную глубину,
cycle-safe, с объяснённым путём на каждый узел.

**Когда звать**: понять blast radius или поток управления вокруг символа, не
читая файлы по цепочке вручную.

**Вход**: `uid` (обязателен), `direction` (`callees` default \| `callers`;
синонимы объектного графа `out` = `callees`, `in` = `callers`),
`depth` (default 2, потолок 8), `expandAmbiguous` (продолжать обход через
ambiguous по кандидатам, а не останавливаться), `view` (warning про
перехватчики КОРНЕВОГО символа — сам BFS перехватчики не пересекает),
`limit` (default 100, max 500), `cursor`.

**Выход** — `CallGraphNodeItem[]`: `uid, name, kind (symbol\|platform\|
ambiguous\|dynamic\|unresolved), depth, path[] (CallGraphStep: fromUid,
toUid, kind, resolution, confidence, platformKey, module, span,
ambiguous)`. Платформенные вызовы: свой `kind=platform`, не тупик.

**Коды ошибок**: `no_active_project`, `not_found` (`uid` пуст; символ не
найден), `invalid_argument` (`direction` неизвестен, подсказка перечисляет
допустимые), `cursor_expired`.

**Пример вызова**:
```json
{"uid": "cfg:CommonModules/УтилитыОстатков/ПолучитьОстаток", "direction": "callers", "depth": 2}
```
**Пример ответа** (усечён):
```json
{
  "items": [{
    "uid": "cfg:Documents/РеализацияТоваров/ПриПроведении", "name": "ПриПроведении",
    "kind": "symbol", "depth": 1,
    "path": [{"fromUid": "cfg:Documents/РеализацияТоваров/ПриПроведении",
              "toUid": "cfg:CommonModules/УтилитыОстатков/ПолучитьОстаток",
              "kind": "common-module", "resolution": "resolved", "confidence": 1.0}]
  }]
}
```

**Известные ограничения**: та же оговорка про перехватчики, что у
`find_references` — только корневой символ, не каждый узел обхода.
Пагинация курсором чинилась дважды (`NextCursor` изначально кодировал
константу `limit` вместо накопленного offset, вторая страница повторяла
первую; исправлено `269b3bc`, регрессионный тест есть) — сейчас корректна,
упомянуто как прецедент для тех, кто пишет новый курсор. p50/p95 на
реальной выгрузке — 98.7мкс/163мкс против бюджета 100/400мс.

### `get_object`

**Назначение**: структура объекта метаданных из индекса, не из XML —
реквизиты/ТЧ/измерения/ресурсы, uuid, формы с командами, подписки, чей
источник — этот объект, регламентные задания (когда объект сам —
`ScheduledJob`), права ролей.

**Когда звать**: вместо чтения XML объекта, когда индекс доступен (быстрее,
честные `layer`/`provenance`). Без индекса или в live-режиме нужен
`get_object_structure`: он читает выгрузку или живую базу.

**Вход**: `type` (обязателен, тот же словарь, что у `get_object_structure`),
`name` (обязателен, без префикса типа), `component` (снимает
неоднозначность при заимствовании), `parts` (csv:
`members`\|`forms`\|`subscriptions`\|`jobs`\|`roles`, default — всё), `view`
(`effective` без `component` сливает Members/Forms по всем применимым слоям
в `applyOrder`, каждая строка — со своим `layer`).

**Выход** — `ObjectItem` (items[0]): `type, name, uuid, synonym, component,
layer, members[] (kind, name, types, indexed, parent, layer),
forms[] (name, commands[], layer), subscriptions[] (name, sourceKind,
event, handler, resolution), scheduledJobs[] (name, method, use, predefined,
resolution), roleRights[] (role, right, value, rls, setForNewObjects)`.

**Коды ошибок**: `no_active_project`, `not_found` (`type`/`name` пусты;
объект не найден), `component_not_registered`.

**Пример вызова**:
```json
{"type": "Catalog", "name": "Номенклатура", "parts": "members,forms"}
```
**Пример ответа** (усечён):
```json
{
  "items": [{
    "type": "Catalog", "name": "Номенклатура", "component": "cfg", "layer": "cfg",
    "members": [{"kind": "attribute", "name": "Артикул", "types": ["String"], "indexed": true}],
    "forms": [{"name": "ФормаЭлемента", "commands": ["ЗаполнитьПоШаблону"]}]
  }]
}
```

**Известные ограничения**: `roleRights` — сырые факты, включая
`value=false`; ИЛИ-агрегация по `setForNewObjects` — дело читающего
(`resolve.EffectiveRoleObjectRights`), `get_object` их не агрегирует.
`scheduledJobs` заполняется, только когда сам запрошенный объект —
`ScheduledJob` (схема не даёт связать произвольный объект с заданием, его
использующим). Пагинации нет — большое число ролей/подписок не проверено
тестом (открытый пункт спецификации).

### `get_form_handlers`

**Назначение**: привязки обработчиков формы (событие → имя обработчика),
каждая разрешена до символа BSL со span, когда найдена.

**Когда звать**: перед изменением модуля формы — увидеть, что форма
реально подключает.

**Вход**: `type`+`name` (владелец формы, обязательны), `form` (сузить до
одной формы; default — все формы владельца), `component`, `view`
(принимается и валидируется, слияния нет — см. ниже).

**Выход** — `FormHandlerItem[]`: событие, объявленное имя обработчика,
резолюция до символа. Необъявленный в модуле, но заявленный в форме
обработчик отдаётся с `resolution=unresolved` и diagnostic, называющим
событие и модуль, а не пустым списком.

**Коды ошибок**: `no_active_project`, `not_found` (`type`/`name` пусты;
владелец не найден; `form` не найдена среди форм владельца),
`component_not_registered`.

**Пример вызова**:
```json
{"type": "Catalog", "name": "Номенклатура", "form": "ФормаЭлемента"}
```

**Известные ограничения**: `view=effective` валидируется, но **не сливает**
обработчики форм расширений с базовыми (`internal/app` не содержит
`FormService`), см. раздел «Расширения»
выше, «Формы расширений».

### `find_queries_using`

**Назначение**: запросы, использующие объект (любое поле/таблица) или
конкретное поле — найденные по `query_reference`, не сканированием текста
запроса на подстроку.

**Когда звать**: перед переименованием поля или изменением структуры
регистра — увидеть, что реально его читает в запросах.

**Вход**: `type`+`name` (объект) и/или `field`, `component`, `view`
(валидируется, слияния нет — каждая строка уже несёт свой
`component`/`layer`), `limit` (default 50, max 200), `cursor`.

**Выход** — `QueryUsageItem[]`: `kind (table\|field\|parameter\|temp-table),
name, object, field, owner (uid, module, name, span), querySpan (в файле),
spanInQueryText (внутри текста запроса), staticity
(static\|partial\|dynamic), confidence`.

**Коды ошибок**: `no_active_project`, `not_found` (ни type+name, ни field не
заданы; объект не найден), `component_not_registered`, `cursor_expired`.

**Пример вызова**:
```json
{"type": "InformationRegister", "name": "ОстаткиТоваров", "field": "Количество"}
```

**Известные ограничения — самое существенное во всём файле**:
`query_reference` публикуется **только для `staticity=static`** (98.6%
реальных текстов запроса на выгрузке). Partial/dynamic литералы (склеенные
через `bsl.QueryLiteral`/`query.GapMarker`) **не публикуются вовсе** — не
«отдаются с `confidence<1`», а отсутствуют в выдаче полностью. Известный
долг (`internal/resolve/queries.go`, `internal/index/publishderive.go`);
возможное закрытие: публиковать для partial/dynamic строки без разрешения. `find_queries_using`/`find_register_writes` не
расширяют фильтр объекта на несколько слоёв заимствования (см. раздел
«Расширения» выше).

### `find_register_writes`

**Назначение**: доступы к регистру — кто пишет (default), и через `modes` —
кто читает, проводит движения, очищает.

**Когда звать**: перед изменением структуры регистра — увидеть каждого
писателя и читателя, не только уже известную процедуру проведения.

**Вход**: `register` (обязателен, без префикса типа), `modes` (csv:
`write`\|`read`\|`movement`\|`clear`, default `write`), `component`,
`symbol` (сузить до владельца по uid), `minConfidence`, `view`
(валидируется, слияния нет), `limit` (default 50, max 200), `cursor`.

**Выход** — `RegisterAccessItem[]`: `register, mode, component, file, owner
(QueryOwnerRef), inTransaction, static, confidence, span`. Пустой результат
отдаётся как `items: []`, не `null` (`totalCount` при нуле опущен: поле общего
конверта `omitempty`, сделать его обязательным значило бы сломать контракт всех
индексных инструментов). Если кодовых записей нет (первая страница, `modes`
включает `write`, без `symbol`), а документы объявляют движения в регистр
(рёбра `writes-declared` объектного графа), приходит предупреждение
`register_writes_declared_only`: «кодовых записей нет (modes=write), документы
проводятся механизмом; объявленные движения: N объектов», подсказка
`object_graph objectType=... objectName=... direction=in`.

**Коды ошибок**: `no_active_project`, `not_found` (`register` пуст;
`modes` содержит неизвестное значение; регистр/символ не найден),
`component_not_registered`, `cursor_expired`.

**Пример вызова**:
```json
{"register": "ОстаткиТоваров", "modes": "write,movement"}
```

**Известные ограничения**: тот же предел на фильтр по объекту через
несколько слоёв, что у `find_queries_using` (раздел «Расширения» выше).
Сверено с существующим `get_movements` (тот же набор режимов).

### `find_impact`

**Назначение**: обратный BFS от символа или объекта метаданных — что от
него зависит, ранжировано по расстоянию и confidence, с полным путём
типизированных рёбер.

**Когда звать**: перед изменением сигнатуры символа или структуры объекта —
увидеть каллеров, обработчики форм, доступы к регистрам, права ролей и
field-typed-by зависимости, которые сломаются.

**Вход**: `symbolUid` ИЛИ (`objectType`+`objectName`) — взаимоисключающие,
`component` (сужает объект при заимствовании), `kinds` (`call_edge`\|
`reference`\|`handler_binding`\|`register_access`\|`role_right`\|
`dependency_edge`; пусто — все), `depth` (default 3, потолок 8), `budget`
(default 500, потолок 5000 — исчерпание честно усекает с warning, не
молчит), `view` (`effective` не реализован, откат на raw с warning),
`cursor`.

**Выход** — `ImpactItem[]`: `nodeKind, component, display, depth, edgeKind,
detail, resolution, confidence, layer, path[] (ImpactPathStep — тот же
набор полей на каждом шаге до цели)`.

**Коды ошибок**: `no_active_project`, `not_found` (ни цель не задана, ни
обе половины objectType+objectName; цель не найдена), `component_not_registered`,
`cursor_expired`.

**Пример вызова**:
```json
{"objectType": "InformationRegister", "objectName": "ОстаткиТоваров", "depth": 3}
```

**Известные ограничения** (в `Description` инструмента названы
пользовательскими словами, здесь с именами кода): `query_reference` публикуется индексом, но
**не подключён как обходимое ребро** (`store.ImpactEdgeKinds`/
`normalizeImpactKinds` его не включают) — никогда не появляется как
`edgeKind`. `dependency_edge` сегодня несёт только `field-typed-by` — три
других вида (subsystem-contains, exchange-plan-contains,
test-references-symbol, epf-uses-object) не собраны. `reference` в
метаданный объект как цель не находится — `publishReference` не заполняет
`target_object_id`. Сверка с существующим `find_dependency_paths` — 3/3 на
реальной выгрузке, `find_impact` даёт надмножество.

### `get_context_for_task`

**Назначение**: ключевой инструмент индексного слоя: задача словами → минимальный, но
достаточный контекст: определение и место правки первым фактом, дальше
только доказанно релевантное по классифицированному intent, упакованное в
жёсткий бюджет символов, с честным отчётом о покрытии.

**Когда звать**: первым на любую нетривиальную задачу 1С, до точечных
`find_symbol`/`get_object`/`find_impact` — они остаются полезны для
доследования того, что назвал `get_context_for_task`.

**Вход**: `task` (обязателен, словами), `project` (сверяется с активным),
`componentHints[]`, `focusHints[]` (точные имена, пробуются раньше разбора
`task`), `budgetChars` (default 16000), `budgetTokens` (alias, `chars =
tokens * 3`), `view` (`effective` частично реализован, см. «Известные ограничения» ниже; неизвестное значение даёт ошибку, а не молчаливый откат),
`maxDepth` (default 2, потолок 6), `includeCode`
(`signatures`\|`bodies`\|`none`), `freshness` (`allow-stale` default \|
`require-fresh`).

**Выход** — `retrieve.Result` (items[0]): `intent, anchors[], facts[],
signatures[], snippets[], metadataSummaries[], relations[],
requiredCoverage[] (category, status: complete_inline\|
complete_via_resource\|complete_empty\|partial\|missing, returnedCount, totalCount),
sufficiencyStatus (sufficient_inline\|requires_resource_fetch\|
insufficient), missingRequired[], ambiguities[], budget
(usedChars<=normalizedBudgetChars — инвариант), excludedHighScoring[],
suggestedNextTools[]`.

**Коды ошибок**: `no_active_project`, `not_found` (`task` пуст; `project`
не совпадает с активным), `index_not_fresh` (`require-fresh`, дедлайн
исчерпан — тело ответа несёт прогресс индексации).

**Пример вызова**:
```json
{"task": "поменять сигнатуру ПолучитьОстаток — добавить параметр ДатаНаКоторуюСчитать", "budgetChars": 12000}
```

**Известные ограничения**: `view=effective` накладывает расширения,
применяющиеся к компоненту анкера (`internal/effective`, порядок по
`applyOrder`), каждый факт несёт свой слой (`component`).
`bugfix`/`unknown` и `signature-change` получают категорию `interceptors`
(перехватчики базового символа) как точный факт из `resolve.DeriveIntercepts`
(не эвристику по имени), `form` получает `handler_intercepts` на обработчике
формы, `posting` получает `posting_handler_intercepts` и движения самих
перехватчиков (ADR-029, ADR-034). По ADR-035: `register` получает
необязательную `writer_intercepts` (перехваченные писатели регистра и записи,
сделанные самими перехватчиками; запись перехватчика в `writes_movements`
называет перехватываемый метод), `query` получает необязательную
`query_intercepts` и тексты запросов перехватчиков в `query_text`/`schema`/
`tables_fields` со слоем расширения (для `&ИзменениеИКонтроль` исполняется
текст расширения), `add-attribute` и `rights` получают заимствования
объекта-анкера в расширениях: структуру с реквизитами расширения, формы,
роли расширения с правами и RLS. Под `effective` категория `forms`
(`add-attribute`) и `rls` (`rights`) заявляются собранными и при честной
пустоте дают `complete_empty`. Конфликт двух `&Вместо` от разных расширений
даёт warning `instead_conflict` с обоими слоями. `raw` для всех intent прежний.
`effective_view_partial_coverage` остаётся только для intent вне
`effectiveAwareIntent` (`exchange`/`extension`). Опечатка в `view` даёт ошибку, а не молчаливый
откат на raw. Задержка на крупной конфигурации: см.
`docs/benchmarks.md`, после исправления `precheckChangedCount` p50 921 мс и p95 1.73 с при
бюджете 1 с и 2.5 с. Причина найдена точно, не гипотеза: не сам `retrieve.Build`
(изолированно — p50 553мкс, p95 892мкс, `TestBuildIsolatedLatency`, на три
порядка меньше бюджета), а `internal/index.Service.EnsureFresh` →
`precheckChangedCount`, который стат-сканирует ВСЕ файлы выгрузки на КАЖДЫЙ
вызов, даже read-only через `retrieve.Run`. Долг `internal/index`,
не `internal/retrieve`, см. `docs/benchmarks.md`.

### `index_status` / `reindex`

**Назначение**: наблюдаемое состояние индекса активного проекта — фаза,
generation, возраст, ETA перестроения, последние diagnostics, размеры
эпохи/WAL, кандидаты autodiscovery (`index_status`); принудительная
перестройка индекса — инкрементальная или полная (`reindex`).

**Когда звать**: `index_status` — перед любым другим индексным
инструментом, когда важно знать свежесть, или чтобы понять, чем занят
индекс и что пошло не так; `reindex` — после массового изменения, которое
сервер не увидел сам (внешний `git pull`, правка через конфигуратор),
когда не хочется ждать debounce.

**Вход** `index_status`: `project` (сверяется с активным),
`includeAllDiagnostics`. **Вход** `reindex`: `mode` (`incremental` default \| `full`;
для нового `projectRoot` по умолчанию `full`), `component` (сужает до одного компонента
манифеста; пусто: все), `projectRoot` (каталог с `1c-project.json`: регистрирует и
активирует проект, единственный способ зарегистрировать его), `includeAllDiagnostics`
(полный список диагностик вместо первых десяти и сводки `diagnosticsDigest`).

**Выход** `index_status` — `StatusItem` (items[0]): `phase, project,
generation, epoch, rebuildInProgress, needsFullRebuild, validatedAt,
ageSeconds, etaSeconds, lastFullSeconds, lastIncrementalSeconds, epochBytes,
walBytes, diagnostics[], candidates[] (workspace.Candidate — компоненты,
найденные на диске, но не в манифесте), lastReindexCounts`. **Выход**
`reindex` — `ReindexResultItem` (items[0]): `mode, generation, durationMs,
components[] (component, filesChanged, filesRemoved, symbols,
diagnostics[]), diagnostics[]`.

**Коды ошибок**: `no_active_project`, `not_found` (`project` не совпадает с
активным — `index_status`), `component_not_registered` (`reindex` с
неизвестным `component`).

**Пример вызова** `index_status`: `{}`. **Пример ответа** (усечён):
```json
{
  "items": [{
    "phase": "idle", "project": "utdemo", "generation": "e1.g42", "epoch": 1,
    "rebuildInProgress": false, "needsFullRebuild": false,
    "ageSeconds": 12.4, "epochBytes": 187342848, "walBytes": 4096
  }]
}
```
**Пример вызова** `reindex`: `{"mode": "incremental"}`.

**Известные ограничения**: `reindex` — единственный мутирующий индексный
инструмент (`ReadOnlyHint=false`); `index_status` не принимает `component`
— статус про свежесть/generation проекта в целом, не про конкретный объект
(та же оговорка, что в разделе «Расширения» выше, «Какие инструменты видят
effective view»). `lastReindexCounts`/`lastFullSeconds`/
`lastIncrementalSeconds` считаются только по вызовам ЭТОГО процесса
(`IndexStatusService` держит их в памяти, не в store) — рестарт сервера их
обнуляет, это не история индекса, а счётчики текущей сессии.

**Известный предел `reindex`**: инкрементальный режим на конфигурации около 50 тыс. файлов
падает на лимите числа параметров SQLite (`ObjectDataEdgesDependingOnFiles`); полный режим
работает.

### `object_graph`

**Назначение**: окрестность одного объекта метаданных по данным одним вызовом: рёбра
`writes-register` (запись в регистр из кода) и `writes-declared` (движения, объявленные в
`RegisterRecords`), глубина до 2, у каждого ребра тип и имя обоих концов.

**Когда звать**: когда нужна вся картина «кто куда пишет» вокруг объекта, вместо цепочки
`find_register_writes` и `find_references`. Пустой `find_register_writes` подсказывает
именно его: документ может только объявлять движения без кода записи.

**Вход**: `objectId` или `objectType`+`objectName`, `component`, `direction` (`in`,
`out`, `both` по умолчанию; синонимы `callers` и `callees`), `kinds`, `depth` (по умолчанию 2,
больше 2 даёт ошибку), `minConfidence`, `project` (корень другого зарегистрированного
проекта, только для этого вызова), `limit` (по умолчанию 50, не больше 200), `cursor`.

**Известные ограничения**: уверенность рёбер из кода снижается на длинных цепочках и на
цепочках через процедуры-хабы (флаги `--graph-*`, по умолчанию глубина 6, хаб от 50
вызывающих); ребро при этом не пропадает. Та же модель лежит под картой `mcp1c graph`
(`-project` = корень workspace с `.mcp1c`; на карте объект ищется по части имени через
`GET /api/search?q=`).

## Справка по платформе вне индекса

### `bsl_syntax`

Не индексный инструмент: читает файл индекса синтаксиса платформы (`cmd/syntaxgen`), работает
без выгрузки и без `--projects-root`. Покрывает то же, что отдельный MCP-справочник по API
платформы: поиск по имени, члены типа, конструкторы.

**Вход**: `query` (имя по-русски или по-английски: точное, префикс, подстрока), `queries`
(несколько имён за вызов), `owner` (тип-владелец по-русски, без учёта регистра), `limit`.
Форма `query=Тип.Член` (`ТаблицаЗначений.Свернуть`) равна `owner`+`query`; точки в имени
владельца (`ВедущиеВидыРасчета.<Имя плана видов расчета>`) не мешают: делится по той точке,
левее которой стоит существующий владелец, иначе ищется как раньше.

**Режимы ответа**:

- `query` без владельца: `matches` с полными записями, как до появления `owner`. Если `query`
  совпадает с именем типа, добавляется `type`: число членов, конструкторы в компактной форме и
  подсказка, как получить члены. Члены сами не отдаются, чтобы не раздувать ответ на частый
  вопрос «как создать объект».
- `owner`+`query` (или `Тип.Член`): `matches` только среди членов этого владельца, полные записи.
- `owner` без `query`: `members` в компактной форме (`nameRu`, `nameEn`, `kind`, `signature`,
  `type` одной строкой, `summary` из первой строки описания), порядок: конструкторы, методы,
  свойства, события, внутри по имени. Лимит по умолчанию 150: целиком помещается любой тип,
  кроме глобального контекста (~630) и библиотеки картинок (~300); при обрезке `truncated` и
  `total`. На реальном индексе `ТаблицаЗначений` (22 члена) это около 4,5 тыс. символов.
- Неизвестный `owner`: пустой ответ с `note` и до пяти похожих имён владельцев.

**Известные ограничения**: английских имён владельцев в индексе нет, `owner` понимает только
русское имя. Английское имя конструктора в справке служебное (`ctor182`), в компактной форме
оно опускается.
