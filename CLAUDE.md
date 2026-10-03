# 1c-workflow

MCP-сервер для разработки 1С (BSL): один stdio-процесс на Go для ИИ-агентов (Claude Code,
Codex и другие). Два слоя инструментов в одном `newServer`: старый читает XML-выгрузку или
live HTTP-коннектор по требованию на каждый вызов; новый индексный — персистентный SQLite-индекс
символов, ссылок, call graph, метаданных, запросов и регистров с инкрементальным обновлением
(может заменить Serena в повседневной работе над выгрузкой 1С).

## Команды

Все — из корня репозитория. Команды верны на Windows, macOS и Linux; платформенное —
только shell и кросс-компиляция ниже.

| Команда | Что делает |
|---|---|
| `go build ./...` | Собрать всё (включая `cmd/syntaxgen`, `evals/runner`, `evals/findapi`) |
| `go vet ./...` | Статический анализ, вывод пуст |
| `go test ./...` | Юнит- и fixture-тесты, без реальной выгрузки и без индекса синтаксиса платформы |
| `go test -count=1 ./...` | То же без кэша; самый долгий пакет `cmd/mcp1c` ~25 с |
| `go test ./internal/source/...` | Один пакет |
| `go test -run TestName ./internal/index/...` | Один тест |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...` | Кросс-сборка — проходит |
| `CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...` | Кросс-сборка — проходит |
| `CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...` | Кросс-сборка — проходит |
| `gofmt -l <файлы>` | Только по своим файлам: в легаси-слое есть исторический неформатированный долг, а при `core.autocrlf=true` список — почти весь репозиторий. См. «Подводные камни» |
| `go test -race ./...` | Race-детектор, ~2.5 мин |
| `go run ./cmd/syntaxgen <платформа>/shcntx_ru.hbk <платформа>/shlang_ru.hbk <платформа>/shquery_ru.hbk` | Собрать индекс синтаксиса платформы в `<UserConfigDir>/mcp1c/syntax-index.json.gz` (нужен `bsl_syntax`, `validate_bsl` и real-dump тестам) |
| `go build -o <tmp>/mcp1c ./cmd/mcp1c` + `python3 tools/measure_cache_rss.py --binary <tmp>/mcp1c --dump <выгрузка> --ttl 25s --settle 10 --wait 95` | Замер RSS и кэша через живой stdio-диалог с сервером; печатает JSON с тремя блоками памяти. Требует бинарник и реальную выгрузку; `--wait` должен быть больше TTL **и** больше минуты (период сброса). На Windows блок RSS не отдаётся вовсе |

`ONEC_DUMP=<путь к каталогу с реальной 1С-выгрузкой>` (или `MCP1C_SPIKE_DUMP`) включает
дополнительные тесты на реальных данных в `internal/index`, `internal/resolve`,
`internal/retrieve`, `internal/app`, `internal/parse/*`, `cmd/mcp1c` — без
переменной они честно `t.Skip`. Им же нужен настоящий индекс синтаксиса платформы
(`MCP_1C_SYNTAX_INDEX` или путь по умолчанию, `syntaxtest.RealOrSkip`); без него они тоже
пропускаются. Каталог должен быть корнем выгрузки конфигурации (тем, что
видит `internal/workspace.DetectKind`), не произвольной папкой; конкретный путь машинный, не
хардкодить и не коммитить.

**Выгрузки не взаимозаменяемы.** Большая часть real-dump тестов написана под `ut_demo`
(`internal/index/realworld_test.go`, `internal/retrieve/realdump_test.go`). Тесты перехватчиков
проведения (`internal/retrieve/realdump_posting_test.go`,
`internal/app/effective_realworld_test.go`) требуют выгрузку со слоями расширений в
`1c-project.json` и пары из окружения:
`ONEC_POSTING_INTERCEPTS="Документ:Вид:слой:ИмяПерехватчика;..."` и
`ONEC_POSTING_NOBASE="Документ:Вид:слой:ИмяПерехватчика"` (документ без базового модуля
объекта). Без переменных они пропускаются. Подставив не ту выгрузку, получишь красное там,
где дефекта нет.

Прогонять пакеты по одному через `-run`, не всё разом с `ONEC_DUMP=... go test ./...`: полный
холодный индекс уходит на реальных данных за границы 10-минутного дефолтного `-test.timeout` при
параллельном запуске нескольких пакетов (конкуренция за CPU), см. «Подводные камни».

## Структура

- `cmd/mcp1c` — точка входа MCP, регистрация всех инструментов (легаси + индексных), настройки процесса, блок памяти `server_info`
- `cmd/syntaxgen` — генератор индекса синтаксиса платформы (`internal/syntax`) из `.hbk` установленной платформы; индекс в репозиторий не входит
- `internal/domain` — сущности и инварианты индекса, ноль зависимостей кроме stdlib
- `internal/store`: SQLite, схема (38 таблиц), эпохи, WAL, reader pool, единственный writer
- `internal/parse/{bsl,meta,query}` — три независимых парсера: BSL (свой tolerant, не tree-sitter — ADR-3), XML метаданных, текст запроса 1С
- `internal/resolve` — разрешение имён, call graph, вывод обращений к регистрам/запросам/обработчикам форм
- `internal/index` — пайплайн индексации: discover→fingerprint→parse→normalize→resolve→derive→validate→publish
- `internal/app` — бизнес-логика инструментов как сервисы, DTO на вход/выход
- `internal/retrieve` — движок `get_context_for_task` (intent, scoring, упаковка в бюджет)
- `internal/workspace` — манифест проекта (`1c-project.json`), локальный registry (`.mcp1c/`) и раскладка XML-выгрузки (`dumplayout.go`)
- `internal/graphweb` — SPA-карта объектного графа в браузере (ассеты, сервировка, эпохи)
- `internal/arch` — гард на граф импортов (обычные `go test`, не отдельный линтер)
- `internal/{source,onec,standards,syntax,validate}`: легаси-слой инструментов, без индекса, по запросу читает XML/live-коннектор; кэш разобранных коллекций живёт здесь (`internal/source/cache.go`); новый код не импортирует этот слой, кроме `internal/syntax`
- `connector` — исходники BSL-расширения `МCPКоннектор` (live-режим), отдельный деплой от Go-кода
- `evals`: задачи и раннер оценки качества `get_context_for_task` (`docs/evaluation-report.md`); `evals/findapi`: эталон и счётчик качества `find_api` (`docs/find-api-eval.md`)
- `tools` — вспомогательные python-скрипты вне сборки: `measure_cache_rss.py` (замер памяти), `bsl_ls_report.py` (компактный отчёт bsl-language-server)
- `docs`: `architecture-index.md` и `architecture-graph.md` (архитектура), `adr/` (ADR-002...ADR-039), `tools-index.md`, `benchmarks.md` (замеры), `install.md`, `evaluation-report.md` (оценка качества), `find-api-eval.md` (эталон `find_api`)

## Ключевые файлы

- `cmd/mcp1c/main.go` — entrypoint, `parseFlags`, поднятие stdio-транспорта; жизненный цикл `newServer → Connect → ss.Wait()` неприкосновенен
- `cmd/mcp1c/options.go` — `options` (все tunables процесса), дефолты `defaultCacheTTL`/`defaultCacheLimitBytes`, `registerCacheFlags`, `envDurationOr`/`envInt64Or`; `registerSyntaxIndexFlag`, `syntaxIndexPath`, `warnMissingSyntaxIndex`
- `cmd/mcp1c/options.go`: профиль `toolsProfile` (`defaultToolsProfile = full`, `registerToolsFlag`, `envToolsProfileOr`, неизвестное значение и во флаге, и в env откатывается на full)
- `cmd/mcp1c/memory.go` — `buildMemory`, `memoryOutput`/`cacheMemoryOutput`, `maxCacheRowsShown = 10`, `processStart`
- `cmd/mcp1c/rss_{linux,darwin,rusage,unsupported}.go` — `processRSS() (bytes int64, kind string, ok bool)`, по файлу на платформу, build-теги
- `cmd/mcp1c/tools.go` — `newServer`, composition root, `syntaxCorpus`/`syntaxCorpusErr`, легаси-инструменты, `serverInfoHandler`
- `cmd/mcp1c/tools.go`: `finishSurface` решает поверхность: индексные инструменты только при открытом реестре (`--projects-root`), `coreExcludedTools` снимаются при `--tools=core`, ставится middleware instructions
- `cmd/mcp1c/instructions.go`: `instructionSections` + `buildInstructions(has)`, текст собирается при `initialize` из фактически зарегистрированных инструментов через собственный `tools/list` сервера; лимит 2048 рун на каждую комбинацию режим x профиль x индекс (`TestServerInstructionsFitClientLimit`)
- `cmd/mcp1c/surface.go`: `toolSurface`, набор объявленных сервером инструментов, снимается при `initialize` (`instructionsMiddleware`) и служит единственным источником для instructions и подсказок следующего шага (`nextStepsFor`, `suggestedNextTools` у `get_context_for_task`); сбой `tools/list` даёт полный текст instructions без фильтра и запись в stderr; хендлер находит свой набор через `surfaceOf(server)`
- `cmd/mcp1c/surface_test.go`: табличный тест состава (режим x профиль x реестр: нет, открыт, сломан), списки `индексныеИнструменты` и `исключеныВCore` закреплены литералами
- `cmd/mcp1c/indexreg.go` — реестр индексных инструментов (не редактировать)
- `cmd/mcp1c/idx_{symbol,meta,impact,context,status,objectgraph,api}.go` — регистрация индексных инструментов, по файлу на группу
- `internal/app/api.go`, `apiindex.go`, `apirank.go`: `find_api` (issue #15). Методы программного интерфейса берутся одним запросом `store.ExportedMethodsInRegions` по пути областей символа; секция `bsp` определяется составом подсистемы `СтандартныеПодсистемы` (любой объект состава: общий модуль или владелец модуля менеджера), который читается из объявлений подсистем в блобах индекса (`meta.ParseSubsystemContent`, `workspace.DumpChildSubsystemPath`), пока индекс не хранит рёбра состава. Поиск идёт по обратному индексу слов в памяти (`apiIndex`, `apiindex.go`): слова имени, модуля, первой строки и остального комментария (`symbol.doc`) разбираются один раз на поколение индекса (`apiIndexCache` в `APIService`, по одному на проект), вызов стоит миллисекунды; первый вызов после переиндексации строит индекс слов заново. Слова сравнивает `apiSameWord` (`apirank.go`: общее начало плюс остатки из русских суффиксов и окончаний). Ранжирование (`apiIndex.search`): охват = сумма «редкость слова на вес поля» (`apiFieldCover`), редкость у слова своя для имени, модуля и первой строки и своя для остального комментария; затем доля слов имени, покрытых запросом; затем вес мест. Ответ: `limit` полных описаний и `apiMoreCount` следующих методов коротким списком (`bspMore`, `otherMore`); устаревшие уходят в хвост уже после среза `limit`. **Правила ранжирования меняются только с прогоном эталона `evals/findapi`** (подбор по половине `dev`); `TestRealDumpFindAPI` это страховка на 40 ручных запросах, а не оценка
- `internal/app/apicatalog.go`: `APIService.Catalog`, все методы программного интерфейса двумя секциями без запроса (общий с `FindAPI` отбор `readAPIMethods`); потребитель: оценка `evals/findapi`
- `evals/findapi`: эталон «задача словами → готовый метод» из реальных вызовов (`docs/find-api-eval.md`). Набор `data/ut_demo.jsonl` (498 пар, половины `dev`/`test`, каждая пара принята двумя проверяющими моделями), база `data/ut_demo.baseline.json`. **Любое изменение ранжирования `find_api` меряется прогоном `go run ./evals/findapi score -project <ut_demo> -data ... -baseline ...`**: падение на глубине 10, 20 или 50 валит прогон; выдачу на один запрос печатает подкоманда `query`; правила настраиваются на половине `dev`, качество называется по `test`. Рабочий каталог генератора `evals/findapi/work` содержит код конфигурации и в git не идёт
- `internal/index/symboldoc.go`: `regionPath` и `docFirstLine` наполняют `symbol.region` (путь областей через `domain.RegionPathSeparator`) и `symbol.doc_first_line`; в `fts_symbols.doc` описание сознательно не кладётся (`internal/store/batch.go`)
- `internal/source/cache.go` — `ConfigureCache(ttl, limitBytes)`, `CacheSnapshot() CacheStats`, `cached[T]`, `estimateSize`, `dirStamp`, сам `dumpCache` со сбросом по TTL и вытеснением
- `internal/syntax/lazy.go`: `NewLazy(path) *Index`, `(*Index).Err() error`; `index.go`: `LoadFile`, `Parse`, `DefaultPath`, `EnvPath`, `ErrNotFound`, `Search`, `Count`, `GlobalMethod`, `ParamCounts`; `owner.go` — `Lookup` (owner, `Тип.Член`, компактные члены `Member`, `TypeInfo`), `MemberLimit`
- `internal/syntax/syntaxtest`: `Fixture`/`FixtureFile` (синтетический корпус `internal/syntax/syntaxtest/testdata/corpus.json`, написан руками) и `RealOrSkip` (настоящий индекс для real-dump тестов)
- `internal/app/projects.go` — `Projects`: активный логический проект → пара store+index.Service, ленивое открытие, кэш на жизнь процесса
- `internal/app/activeproject.go`: `Projects` владеет активным проектом процесса, то есть парой «raw-выгрузка + индексный проект» (`docs/architecture-graph.md` §4.1): `SetDump` (set_dump и `--dump`) привязывает проект по корню компонента манифеста, `reindex projectRoot` переключает raw, без выгрузки активен сохранённый в реестре или единственный проект; `registry.json` не переписывается, тип `dumpState` в `cmd/mcp1c/tools.go` лишь адаптер
- `internal/app/snapshot.go`: `ReadSnapshot[T](ctx, op, fn) (T, Snapshot, error)`, единственная точка чтения индекса инструментом: проверка свежести (`index.Service.CachedFreshness`) плюс ровно одна read-транзакция; ответ проходит через `withSnapshot(resp, snap)` (неиспользованный `snap` не компилируется). Голый `readTx` только у resource-чтений, закреплённых за поколением или хэшем (ADR-036)
- `internal/app/errors.go` — `Error`/коды/`similarName` — канонический «возможно, вы имели в виду»
- `internal/app/indexstatus.go` — `Status(ctx, StatusInput)`, `ReindexInput`, `doseDiagnostics`, `DiagnosticsDigest`, `const diagnosticsSample = 10`; `ReindexStageResult{Name, DurationMS}` и `ReindexResultItem.Stages []ReindexStageResult` (`json:"stages,omitempty"`) — поэтапные тайминги `reindex` в MCP-ответе, собираются из `index.Result.Stages`
- `internal/index/service.go`, `pipeline.go` — `Service.Reindex/Status/EnsureFresh`, сам пайплайн; `Result.Stages []StageTiming`
- `internal/index/stagetiming.go` — `StageTiming{Name, DurationMS}`, `stageAccum` (накопитель этапов по имени, `add`/`mark`/`stages`), синтетический этап `"commit"` — остаток вне `runComponent`
- `internal/index/parserversion.go`: `const ParserVersion` (сейчас 5); поднимает каждый, кто меняет ВЫХОД парсера
- `internal/index/corpus.go`, `hydrate.go`, `envbuild.go` — резидентный корпус файлов, восстановление из `source_file`, `fileRecord.hydrated`, инвариант `buildEnvInput`
- `internal/index/freshness.go` — `precheckWork{changed, pending}`, `precheckWorkload`, `precheckChangedCount` (обёртка)
- `internal/index/diskcheck.go`: `CachedFreshness` (дешёвый источник `stale` без запуска инкремента), исход обхода `diskCheck` с TTL/MaxAge (`Config.FreshnessTTL`/`FreshnessMaxAge`, 30 с / 5 мин), обход один на сервис (`diskFlight`, singleflight: синхронный путь, фон и прогрев `WarmFreshness` ждут общий, `opMu` берётся прерываемо с перепроверкой исхода, `Close` отменяет обход и ждёт горутину, после него `ErrServiceClosed`), типизированные причины `StaleReason`; каждый `precheckWorkload` пишет исход, прогон по всем компонентам сбрасывает его в «расхождений нет» на момент старта (ADR-036)
- `internal/index/publish.go`, `publishderive.go`, `publishmeta2.go`, `publishforms.go` — публикация фактов в store; `publishModuleOwner` дописывает `module.owner_object_id` после прохода 1
- `internal/index/plan.go`: чистые `planFile` (проход 1) и `planLinks` (проход 2) строят строки файла на identity_key без id и без SQLite, `publishXxx` только применяют план; `ordered.go`: `runOrdered` строит планы в пуле и отдаёт их единственному писателю строго по порядку файлов, окно `orderedWindow` ограничивает память (issue #3)
- `internal/store/batch.go`: многострочные INSERT листовых таблиц (`txBatches`), `conn.go`: кэш `Prepare` на write-транзакцию (`stmtCache`)
- `internal/store/inbound.go`: `ReplaceSourceFiles(fileIDs, insert)`, единственный путь переопубликования: снимок указателей нетронутых файлов на узлы переопубликуемых во `temp.inbound_ptr`, `DeleteSourceFiles`, `insert` (проход 1), возврат указателей узлам с прежним id (ADR-037); `staleNodes` общий с шагом (1b); `setNullKinds` обязан покрывать все `ON DELETE SET NULL` на symbol/metadata_object/metadata_member (`TestInboundKindsCoverSetNullColumns`); мягкие указатели без REFERENCES (`module.owner_object_id`, `form.owner_object_id`) лежат в `softKinds`, и SET NULL за SQLite делает `DeleteSourceFiles`; файлы каталога объекта, чей XML появился в инкременте, переопубликуются (`filesOfAppearedObjects`, issue #14)
- HTTP-связи между базами (веха В2, ADR-039): факты `parse/bsl/httpcalls.go`
  (`Module.HTTPCalls`) и `parse/meta` (`Facts.HTTPService`), таблицы `http_call`/`http_endpoint`
  (`internal/store/httpfacts.go`, держатся только за свой файл), чистая сшивка и атрибуция
  `internal/resolve/httpstitch.go` (`StitchHTTPCall`, `AttributeSymbolFact`), маппинг хостов
  `internal/workspace/httphosts.go` (`.mcp1c/http-hosts.json`), чтение и сборка связей
  `internal/app/httplinks.go` (`ReadHTTPFacts`, `StitchCrossLinks`, `ObjectHTTPLinks`),
  транспорт `internal/graphweb/crosslinks.go` (`/api/crosslinks`) и `object_graph`
  (`crossProjects`, блок `httpLinks`). В `object_data_edge` вида `http-call` нет: конец ребра
  в индексе другой базы
- `internal/store/cascade.go`: снимок и возврат строк нетронутых файлов, которые уходят `ON DELETE CASCADE` вместе с владельцем переопубликуемого файла (права ролей, рёбра и бейджи объектного графа), внутри того же `ReplaceSourceFiles` (ADR-038); `cascadeKinds` плюс `sameFileCascades` обязаны покрывать все каскады на строки, которые сносит удаление файла (`TestCascadeKindsCoverCrossFileCascades`)
- `internal/store/store.go`, `tx.go`, `schema.go` — `Open/Read/Write/Rebuild/Status`, контракт `ReadTx`/`WriteTx`
- `internal/store/retrieve_read.go`, `read_symbol.go`, `readdiagnostic.go` — выборки для `retrieve`/`app`, в т.ч. `SourceFilesByComponent`
- `internal/resolve/*.go` — `NewEnv`, `Resolve`, `Derive*`; `layer.go` — `ParseInterceptAnnotation`, `DeriveIntercepts` (второе значение — диагностики), `DetectInsteadConflicts`, `DiagInterceptTargetUnknown`
- `internal/effective/*.go`: единственное наложение слоёв расширений на модуль и объект (effective-вид), общее для `app` и `retrieve`; `BorrowedObjects(ObjectSource, obj)` отдаёт строки объекта в применяющихся расширениях, `Overlay` (кэш модулей на вызов) с обратным запросом `InterceptOf` (ADR-035); `Module(Source, base, modulePath) (Result, error)` отдаёт перехватчики с текстом, `BorrowedBy` и диагностики (`effective_blob_unavailable`, ADR-027), `InsteadConflict` даёт текст `instead_conflict`; адаптеры `StoreSource(*store.ReadTx)` (порядок из `store.Components` той же транзакции, не из манифеста) и `Memory` (тесты) над собственным типом `Extension`; правило порядка `ApplyingTo` (applyOrder, при равенстве id компонента), перевод из `store.Component` только в `ExtensionsFromStore`; транзакций не открывает, из `cmd/mcp1c` запрещён гардом
- `internal/parse/bsl/*.go` — `Parse`, `Module`, `Reference` (только Span, текст — через `Module.Text`); `Method.Annotations []Annotation{Name, Arg, HasArg}`, `DiagBadAnnotationArgument`
- `internal/retrieve/build.go` — `Build`/`Run`/`buildWithSymbols`, движок `get_context_for_task`; `anchorExpansion{anchor, candidates, warnings}` — итог typed expansion одного анкера до слияния в плоские срезы; `suppressFormNoiseWhenMatched(expansions)` — гасит `no_forms`/`form_binding_not_matched` от анкера ТОЛЬКО когда анкер-омоним ТОГО ЖЕ `(ObjectType, ObjectName)` дал настоящую находку — анкеры разных имён ИЛИ разных типов друг на друга не влияют, `Component` в ключе группировки сознательно нет (только intent=form, вызывается сразу после сбора `expansions`, до слияния в `allCandidates`/`warnings` и до `dedupWarnings`)
- `internal/retrieve/effective.go` — `symbolFinder`, `effectiveInterceptsForSymbol`, `interceptorSymbol`, предупреждения `interceptor_symbol_*`; `postingInterceptCandidates` (общий хвост обоих путей posting-перехватчиков) и `postingInterceptsWithoutBaseHandler` (базового обработчика нет вовсе); `registerWriterIntercepts` (register), `queryInterceptCandidates` (query), швы `borrowedObjectsFinder`/`queryReader` (подмена через `readSeams`), ADR-035
- `internal/retrieve/expand2.go` — `expandPosting`: `findPostingHandler`, `postingObjectModulePath`, `postingRegisterAccesses`, `postingSubscriptions`, `effectivePostingIntercepts`, `postingBaseHandlerMissingWarning`
- `internal/retrieve/pack.go` — словарь покрытия и достаточность, гейт `complete_empty`
- `internal/workspace/*.go` — `LoadManifest`, `OpenRegistry`, `Discover`, `SafeJoin`
- `internal/workspace/dumplayout.go`: раскладка `DumpConfigToFiles` одним местом, три лица одного
  правила: построение (`DumpObjectDir`, `DumpDeclarationPath`, `DumpModulePath(mtype, name,
  ModuleKind)`, `DumpFormModulePath`), проверка формы (`IsDumpConformantPath`) и, на стороне
  `retrieve`, обратный разбор (`objectModuleDir`). Каталог коллекции берётся из `domain.MetaKinds`;
  неописанный вид объекта даёт панику, а не угаданное множественное число (`DumpCollectionDir`
  отдаёт вторым значением «вид описан»). Согласие лиц закреплено
  `internal/retrieve/dumplayout_roundtrip_test.go`, покрытие выгрузки:
  `TestDumpCollectionsCoverRealDump`
- `internal/domain/metakind.go`: `MetaKind{MType, DumpDir, CollectionRu, IsRegister, ModuleOwner}`,
  один словарь видов метаданных на index, resolve, workspace, parse/bsl и легаси
  `source.folderForType`; поиск `MetaKindByMType`, `MetaKindByDumpDir` (с учётом регистра),
  `MetaKindByDumpDirFold`, `MetaKindByCollection` (русская или английская коллекция менеджеров);
  подмножества прежних таблиц выражены признаками (`ModuleOwner` у индекса, непустая `CollectionRu`
  у парсера), состав закреплён `metakind_test.go`. Вне словаря остались
  `resolve.queryPrefixToMType`, `source.metadataKinds` (typemap.go) и `validate.managerCollections`
- `internal/arch/*.go`: `Load`, `Check`, правила `RuleDomainStdlibOnly` и др., фикстуры в `internal/arch/testdata/broken`

## Архитектура

Направление зависимостей: `cmd/mcp1c → internal/app → domain`; `app → store, resolve, index,
retrieve, workspace`; `app, retrieve → effective → store, resolve, parse/bsl`; `store, resolve,
parse/* → domain`. `app → parse/meta, parse/bsl` только у `find_api` (`internal/app/api.go`):
разбор состава подсистемы из блоба и имена видов модулей. Проверяется тестом
(`internal/arch`), не только соглашением.

Поток вызова индексного инструмента: MCP-запрос → `cmd/mcp1c/idx_*.go` резолвит активный
проект через `app.Projects` → зовёт свой `internal/app.XxxService` → тот открывает одну
read-транзакцию через `app.ReadSnapshot[T]` (она же ставит `stale` и `stale_index` по
`index.Service.CachedFreshness`, ADR-036) и читает уже опубликованные факты.
Единственное исключение — `get_context_for_task`: `cmd/mcp1c/idx_context.go` зовёт
`internal/retrieve.Run` напрямую (`Run` сам делает freshness-precheck через
`index.Service.EnsureFresh`, затем `store.Read`, затем `retrieve.Build`) — `internal/retrieve`
единственный пакет, кроме `internal/app`, которому разрешено трогать `store`/`index` из `cmd/mcp1c`.

Поток индексации (пишущая сторона): `internal/index.Service.Reindex` — `internal/workspace`
даёт границы компонентов и манифест → discover/fingerprint по файлам → `internal/parse/bsl` /
`internal/parse/meta` / `internal/parse/query` разбирают факты со spans, каждый чистый (без
диска) и независимый → пайплайн строит `resolve.EnvInput` из уже прочитанного через
`store.ReadTx` → `internal/resolve.NewEnv`/`Resolve`/`Derive*` (использует
`internal/syntax.Index` как справочник platform builtins) разрешает имена, строит call graph и
производные факты → всё публикуется одной write-транзакцией `store.Write`, которая сама
доводит цикл (учёт blob по хэшам → GC по TTL → orphan-sweep → `generation+1` → commit).

`internal/domain` — общий словарь по всем слоям (`Symbol`, `Reference`, `Span`, `Confidence`,
`Provenance`, `Resolution` и т.д.), сам не знает ни про один из них.

Легаси-слой (`internal/source` и соседи) — отдельный путь без индекса: `cmd/mcp1c/tools.go`
дергает его напрямую по каждому вызову (перечитывает XML/live-коннектор), не проходит через
`internal/app`/`store`. Новый код может опираться только на `internal/syntax` из этого слоя —
единственное разрешённое исключение (`internal/arch.CheckLegacyIsolation`).

**Effective-вид и перехватчики.** Факт перехвата строит `resolve.DeriveIntercepts`: цель —
из аргумента аннотации (`&Вместо("ОбработкаПроведения")`), сам перехватчик — из имени метода;
в реальном коде это разные имена. Неразобранный аргумент факт НЕ строит, вместо него уходит
диагностика `intercept_target_unknown` — понижать confidence нечему, `Intercept` точный факт
парсера (ADR-027). `DetectInsteadConflicts` группирует по ЦЕЛИ: два расширения с `&Вместо` на
один метод несут разные имена перехватчиков. Поверх этого `internal/retrieve` строит
effective-вид: intent `posting` эффективный (категория `posting_handler_intercepts`, вес 0.55,
необязательная, ADR-029). По ADR-035 эффективны и `register` (`writer_intercepts`), `query`
(`query_intercepts` и запросы перехватчиков) и `add-attribute`/`rights` (заимствования объекта в
расширениях через `effective.BorrowedObjects`); `effective_view_partial_coverage` остаётся только
для intent вне `effectiveAwareIntent`, raw всех четырёх прежний. Базовый обработчик
проведения при этом не обязателен: если его нет вовсе, перехватчики ищутся по пути модуля
объекта, выведенному из объявления, а отсутствие базового метода называется предупреждением
`posting_base_handler_missing` — в обоих view, потому что молчание тут хуже пустоты (ADR-034).

**Достаточность ответа — по заявлению сборщика.** `CoverageStatus.CompleteEmpty`
(`complete_empty`) даётся только когда expander явно объявил, что категорию собирал
(`declareCollected`), при `total == 0`. Не собирали — `missing`; сбой чтения не имеет права
выглядеть как честная пустота (`declareCollectionFailed`, коды `posting_subscriptions_read_failed`,
`posting_register_access_read_failed`). Правило «`TotalCount == 0` → `complete_empty`» без
заявления уже давало ответ, который не нашёл ничего и объявил себя полным (ADR-030).
Заявления делают `expandPosting` и под `effective` `expandAddAttribute` (`forms`) и
`expandRights` (`rls`); где пустота нечестна, заявления нет (ADR-035).

**Раскладка XML-выгрузки — одно место.** `internal/workspace/dumplayout.go`: объявление объекта
лежит ФАЙЛОМ рядом с каталогом своих модулей (`Documents/Штрафы.xml` +
`Documents/Штрафы/Ext/ObjectModule.bsl`). Обратный разбор (каталог модулей из строки
`source_file`) — `retrieve/expand2.go:objectModuleDir`. Владение модулем выводится из этого
каталога, не по подстроке имени (ADR-029, ADR-033).

**Поэтапные тайминги `reindex`.** `internal/index.Service.reindexLocked` копит длительность
конвейера в `stageAccum` (`internal/index/stagetiming.go`) и кладёт результат в `Result.Stages`;
`internal/app/indexstatus.go` пробрасывает его в `ReindexResultItem.Stages` тем же способом, что
уже даёт `DurationMS`. Этапы `runComponent` в порядке появления: `discover`, `fingerprint`,
`parse` (с issue #3 сюда же входят SHA-256, deflate и запись blob: образы уходят писателю прямо из
пула), `resolve`, `publish` (публикация в store вместе с производными проекциями: планы файлов
строятся в пуле параллельно с записью, отдельного этапа у них нет), плюс `hydrate` только в
incremental-режиме. `commit`: синтетический
остаток вне `runComponent` (внутри `store.Write`/`store.Rebuild`: открытие транзакции, миграция,
blob GC, orphan-sweep, генерация, физическая фиксация) — `internal/store` инструментировать не
стали: код на пути целостности эпох. На `ut_demo` (48 699 файлов, полный reindex) до issue #3
было 228…274 с, из них `publish` 67%, `commit` 29%; после шагов 1-3 около 142 с, `publish` 76 с,
`commit` 54 с (`docs/benchmarks.md`, разделы «Поэтапные тайминги reindex» и «Ускорение полной
индексации: шаги issue #3»).

**Кэш выгрузки (легаси-слой).** `XMLSource` пересоздаётся на каждый вызов, поэтому кэш
разобранных коллекций (`roles`, `subscriptions`, `options`) — process-wide, в переменной
`exportCache`. Ключ — корень выгрузки плюс вид коллекции; актуальность проверяется `dirStamp`
(число файлов + самая новая mtime каталога), сборка идёт вне лока. Жизненный цикл: `cached[T]`
кладёт запись → `cleanLocked` (истечение по idle-TTL, затем вытеснение по потолку, всегда
оставляя одну запись) → `ensureSweeperLocked` при необходимости поднимает фоновую горутину
`sweepLoop`, которая умирает, как только кэш пуст. Настраивается одним вызовом
`source.ConfigureCache(ttl, limitBytes)` из `newServer`; сам пакет стартует с выключенными
истечением и вытеснением, дефолты (10m / 512 МиБ) объявлены в `cmd/mcp1c/options.go`.
Наружу кэш отдаёт только числа: `source.CacheSnapshot() CacheStats` (TTL, потолок, суммарная
оценка, срез `CacheEntryStat{Root, Kind, Bytes, Idle}`, отсортированный по `Root`, затем `Kind`).

**Корпус синтаксиса.** Текст справки платформы в репозиторий не входит: индекс собирает
`cmd/syntaxgen` из `.hbk` установленной платформы. `newServer` строит корпус через
`syntax.NewLazy(opts.syntaxIndexPath())` (флаг `--syntax-index`, env `MCP_1C_SYNTAX_INDEX`,
иначе `syntax.DefaultPath()` = `<UserConfigDir>/mcp1c/syntax-index.json.gz`): тот же `*Index`,
чтение и разбор ~23k записей — при первом обращении. Отсутствующий файл (`ErrNotFound`) и
ошибка разбора видны только через `(*Index).Err()`; сервер стартует без файла, при старте
лишь `os.Stat` и строка предупреждения в stderr.
`bsl_syntax` и `validate_bsl` принимают не `*syntax.Index`, а интерфейс `syntaxCorpus`
(`Err`/`Search`/`Lookup`/`GlobalMethod`, объявлен в `cmd/mcp1c/tools.go` на стороне потребителя) и
зовут `syntaxCorpusErr` внутри хендлера, а не при регистрации.

**Блок памяти `server_info`.** `serverInfoHandler` собирает `processRSS()` +
`time.Since(processStart)` + `source.CacheSnapshot()` и отдаёт их `buildMemory`. В ответе:
`rssBytes`/`rssKind` (`current` на linux/darwin, `peak` на BSD-семействе), `uptime`,
`cache{entries,totalBytes,ttl,limitBytes,shown[],truncated,note}`; `go{heapAllocBytes,sysBytes}`
появляется **только** когда RSS недоступен (Windows) — вместо RSS, никогда рядом с ним.

`connector/` — не Go: исходники BSL-расширения `МCPКоннектор`
(`connector/src/HTTPServices/MCP_Сервис`; собранный `.cfe` в репозиторий не входит),
которое раскатывается в живую базу 1С для live-режима легаси-слоя; собирается отдельным
тулингом (`connector/tools`), в модуль Go не входит.

Ревью правок коннектора: назови Go-клиента, который читает ответ (`internal/source/httpsource.go`).
Расхождения ключей (`tabularParts` против `tabularSections`, поля `/eventlog`) ревью находит хорошо,
а зелёный HTTP 200 их прячет. Ошибки времени выполнения BSL ревью не видит: проверка только
прогоном на живой базе.

## Соглашения кода

- `internal/domain` — только stdlib, `internal/store` — единственное место с `database/sql`
  и SQL-строками; оба закреплены тестами `internal/arch`, не только код-ревью.
- `cmd/mcp1c` не импортирует `store`/`parse`/`resolve`/`index` напрямую — только через
  `internal/app` (кроме `get_context_for_task`, см. «Архитектура»).
- Новый код не импортирует легаси-слой, кроме `internal/syntax`.
- `CGO_ENABLED=0` всегда. Портируемость:
  никаких `syscall`, литералов путей в аргументах `os`/`filepath`/`exec`, `/tmp`, `~/` вне
  файлов с суффиксом `_windows.go`/`_unix.go` или build tag — короткий явный список исключений
  в `internal/arch`, не расширять его, чтобы протащить нарушение.
- Платформозависимое — отдельный файл с build-тегом и одинаковой сигнатурой во всех вариантах
  (образец: `cmd/mcp1c/rss_*.go`, включая `rss_unsupported.go` с честным `ok=false`).
- Потребитель объявляет интерфейс под свою нужду (`syntaxCorpus` в `cmd/mcp1c`, `symbolFinder`
  в `internal/retrieve/effective.go`), а не принимает конкретный тип чужого пакета — иначе
  недостижимую ветку (сломанный корпус, отказ чтения) нечем протестировать. Публичная сигнатура
  остаётся прежней, шов уезжает во внутренний вариант (`Build` → `buildWithSymbols`).
- **Путь внутри выгрузки в фикстуре строится через `workspace.Dump*`, не литералом.** Литерал
  пути объекта в тесте — повод для замечания на ревью (ADR-033). В `internal/retrieve` и
  `internal/app/impact_test.go` это ещё и гард, в остальных пакетах — только соглашение,
  граница названа в разделе «Тесты».
- Меняешь ВЫХОД парсера — поднимай `index.ParserVersion`. Иначе после рестарта precheck честно
  вернёт 0, индекс объявит себя свежим и отдаст факты старого парсера.
- Новый tunable процесса: поле в `options`, дефолт константой рядом с ним, регистрация флага
  в `registerCacheFlags`-подобной функции, применение одним вызовом из `newServer`. Флаг сильнее
  env; неразбираемое значение env откатывается на дефолт, явный `0` выключает механизм.
- Table-driven тесты — стиль репозитория, писать тесты в том же коммите, что и код.
- Язык комментариев — как у соседей в файле: индексный слой (`internal/{app,index,store,
  resolve,parse,retrieve}`) по-русски, легаси-слой (`internal/source`, `cmd/mcp1c`) по-английски.
  Текст, который видит пользователь (`note` в ответе инструмента, сообщения в stderr), — по-русски.
- Эвристика с `Confidence = 1.0` запрещена на уровне типа (`Provenance.Validate`) — нельзя
  случайно выдать факт от regex/эвристики за точный.
- `store.Write` сам доводит транзакцию (blob GC, orphan-sweep, generation, commit) — не
  повторять этот цикл руками в вызывающем коде.
- Вставки листовых таблиц `WriteTx` идут через буфер пакетной вставки (`internal/store/batch.go`,
  колонки и порядок аргументов из одного `batchSpec`, со схемой их сверяет
  `TestBatchSpecsMatchSchema`). Сброс буферов (`tx.check()`) обязателен перед любым оператором,
  который читает буферизуемую таблицу или удаляет и обновляет строки, на которые буферизуемые
  строки ссылаются. `tx.checkNoFlush()` только у вставок и upsert-ов в небуферизуемые таблицы и у
  выборок из таблиц, которые в буфер не попадают (`node`, `source_file`, `role`); в сомнении
  `check()`. Новая листовая таблица добавляется в буфер вместе с родителем в `parents`, иначе
  дочерний пакет обгонит родительский и упадёт на FK. Id строк `reference` выдаёт `refIDs`
  (MAX(id)+1): у таблицы нет AUTOINCREMENT и вставляет в неё только `InsertReference`
  (`TestReferenceIDAllocationGuard`).
- Курсор пагинации кодирует накопленный offset, не константу лимита (см. «Подводные камни»).
- Новый MCP-инструмент индексного слоя — свой `idx_<name>.go` с `init()`, реестр не трогать.
- Наименования BSL/1С в фикстурах и текстах — по стандартам разработки 1С (its.1c.ru/db/v8std);
  этот файл — про сам Go-сервер. Имена объектов в фикстурах вымышленные, не из чужих баз.

## Окружение

Только имена, без значений — маршрутизация легаси-слоя (offline/live), MCP-транспорт и
tunables процесса:

- `MCP_1C_DUMP` (флаг `--dump`) — каталог XML-выгрузки, оффлайн-режим легаси-слоя
- `MCP_1C_BASE_URL`, `MCP_1C_USER`, `MCP_1C_PASSWORD` (`--base`/`--user`/`--password`) —
  live-режим через HTTP-коннектор (`connector/`)
- `MCP_1C_BASES` (`--bases`) — JSON со списком live-баз, мульти-база, переключение `set_base`
- `MCP_1C_PROJECTS_ROOT` (`--projects-root`) — корень workspace нового индексного слоя:
  здесь живут `.mcp1c/registry.json` и SQLite-эпохи всех проектов, независимо от того, где
  лежат исходники самого проекта. Без него (или с нечитаемым реестром) индексные
  инструменты не регистрируются вовсе
- `MCP_1C_CACHE_TTL` (`--cache-ttl`, duration) — idle-TTL кэша выгрузки; `0` выключает истечение
- `MCP_1C_CACHE_LIMIT` (`--cache-limit`, байты) — потолок памяти кэша; `0` выключает вытеснение
- `MCP_1C_TOOLS` (`--tools`, `full`|`core`, по умолчанию `full`): профиль поверхности
  инструментов; `core` не регистрирует `coreExcludedTools` (`cmd/mcp1c/tools.go`)
- `MCP_1C_SYNTAX_INDEX` (`--syntax-index`) — файл индекса синтаксиса платформы; пусто —
  `syntax.DefaultPath()`

Тестовые (читаются только в `_test.go`, не в production-коде):

- `ONEC_DUMP` / `MCP1C_SPIKE_DUMP` — каталог реальной выгрузки для тестов на реальных данных
- `ONEC_POSTING_INTERCEPTS`, `ONEC_POSTING_NOBASE` — пары «документ @ расширение» для
  real-dump тестов перехватчиков проведения (формат — в «Команды»)
- `ONEC_REAL_DUMP`, `ONEC_REAL_FORM`, `ONEC_REAL_OBJECT`, `ONEC_FIND`, `ONEC_HBK` — легаси-тесты
  `internal/source` на реальных данных, независимый от `ONEC_DUMP` набор

## Тесты

Индексный слой — два шва, и они единственные (интерфейсный контракт):

1. **`internal/app`** — поведение инструмента проверяется через сервис, не через SQL и не
   через MCP-транспорт.
2. **e2e stdio** — `cmd/mcp1c/stdio_live_test.go`, `server_test.go`, `stdout_contract_test.go`
   — контракт инструментов и чистота stdout через реальный запуск сервера поверх stdio.

`store`, `parse/*`, `resolve`, `index` тестируются напрямую только там, где шов `app`
физически не может выразить проверку: crash/recovery (`internal/store/crash_test.go`,
`panic_test.go`), PRAGMA lifecycle, точность spans, property-тест «инкремент == чистая
пересборка» (`internal/index/property_test.go`), фаззинг парсера.

Легаси-слой тестируется в своём пакете плюс `cmd/mcp1c`: `internal/source/cache_test.go`
(жизненный цикл кэша), `internal/syntax/lazy_test.go` (число разборов корпуса),
`cmd/mcp1c/{memory,options,rss,syntaxcorpus}_test.go` (форма блока памяти, резолв tunables,
guard по корпусу, сервер без файла индекса синтаксиса). Тесты, которым нужен корпус, берут
синтетическую фикстуру `syntaxtest.Fixture`/`FixtureFile`; контрактные тесты подкладывают её
в `сессияКлиента`, если путь не задан. Время и тик в кэше инъектируются полями `now`/`newTicker`/`sizeOf` в
`dumpCache` — тесты на TTL пишутся через них, а не через `time.Sleep`.

Контрактные тесты `cmd/mcp1c/tools_contract_test.go` / `tools_behaviour_contract_test.go`
сверяют состав и поведение всех инструментов (легаси и индексных; `posting_review`
влит в `get_movements review=true`) с реестром в обе стороны: падают и на удаление
инструмента, и на добавление незадекларированного. Сервер в них поднимается с
`--projects-root` на пустом `t.TempDir()`, иначе индексных инструментов нет. Новое
необязательное поле в ответе тест не роняет; смена обязательности или типа — роняет.
Новый необязательный вход (`includeAllDiagnostics` у `index_status`/`reindex`) их не красит.

**Гард на раскладку — и его настоящая граница.** Путь фикстуры проверяется
`workspace.IsDumpConformantPath` в ДВУХ точках входа: `fileHelper`
(`internal/retrieve/fixture_test.go`) и `file` (`internal/app/impact_test.go`). Там сид,
написавший литерал мимо помощников (`declPath`, `objModulePath`, `commonModulePath`,
`formModulePathOf`), красный сразу.

Мимо гарда `rel_path` пишут напрямую в `InsertSourceFile`: `internal/app/objectgraph_test.go`,
`internal/app/objectgraph_layers_test.go`, `internal/graphweb/fixture_test.go`,
`internal/index/hydrate_test.go` — там пути выгрузочные и сегодня конформны. Отдельно то же
делают `internal/store/{blob_test.go,fixture_test.go,fk_test.go}`, но там пути нарочно
НЕ выгрузочные (`A.bsl`, `CommonModules/X.xml`) и это осмысленно: store проверяет хранение,
а не раскладку. Правилом конформность первых четырёх НЕ держится —
общего помощника у этих пакетов нет. Правишь их —
сверяйся с `workspace.Dump*` руками. `internal/store/*_test.go` в границу не входит вовсе:
там пути намеренно произвольные (`A.bsl`), тестируется хранилище, а не раскладка.

Проверено мутацией: откат `objectModuleDir` красит `TestPostingHandlerFoundByOwningModule`,
литерал вложенной раскладки в сиде красит гард на месте. Единственный намеренно
«неправильный» путь — `Documents/Заказ_Метаданные.xml` (`postingfallback_test.go`): по форме
он конформен, а по смыслу имя файла не совпадает с именем объекта — это и есть проверяемый
случай «каталог модулей вывести не из чего».

Поэтапные тайминги и подавление шума форм покрыты на шве `internal/app` без реальной выгрузки:
`TestIndexStatusServiceReindexReportsStageTimings` (полный reindex — все этапы присутствуют,
сумма `DurationMS` равна общему `durationMs`), `TestIndexStatusServiceReindexIncrementalStageTimingsNearZero`
(`internal/app/indexstatus_test.go`); `TestFormIntentSuppressesNoFormsNoiseFromMatchedHomonym` и
зеркальный `TestFormIntentKeepsNoFormsWhenNoMatchAnywhere` (`internal/retrieve/formnoise_test.go`).

Без `ONEC_DUMP` весь `go test ./...` зелёный и быстрый (кэшируется). С `ONEC_DUMP` — см.
«Команды» и «Подводные камни»: два real-dump теста известно красные (`TestRealDumpFullIndex`
на бюджете, `TestRealDumpFormIntentReturnsHandlerData` на выгрузке не `ut_demo`), плюс таймаут
при параллельном запуске нескольких пакетов.

## Подводные камни

- `gofmt -l .` при `core.autocrlf=true` бесполезен: файлы на диске с CRLF, и gofmt объявляет
  неотформатированным почти весь репозиторий, включая `testdata`. Судить по нему о своём коде
  нельзя — гнать `gofmt -d` на конкретный файл.
  Исторический долг легаси-слоя (8 файлов `internal/source` + `cmd/mcp1c/formimpact_test.go`)
  виден только там, где дерево в LF; форматировать по-прежнему только то, что правишь сам.
- `internal/index.TestRealDumpFullIndex` **падает** на `ut_demo`: бюджет §28 (холодный
  полный индекс < 90 с) превышен, замер 4m38s (48 698 файлов, 226 420 символов, эпоха 2.28 ГиБ,
  HeapAlloc до 6.1 ГиБ). Это задокументированный принятый долг (`docs/benchmarks.md`), не
  регрессия: не чинить втихую и не удивляться красному тесту.
- Второй известный погранслучай на той же выгрузке: `find_symbol` p50 превышает бюджет 20 мс
  примерно на 10%, p95 в бюджете.
- Гонять весь набор `ONEC_DUMP`-тестов разом рискует упереться в дефолтный 10-минутный
  `-test.timeout` пакета — каждый пакет честно строит свой полный индекс той же выгрузки.
  Прогонять точечно (`-run`, один пакет).
- `syntax.NewLazy(path)` **никогда не nil** — прежняя проверка `idx == nil` больше ничего не ловит;
  сломанный корпус виден только через `Err()`. Guard стоит у `bsl_syntax` и `validate_bsl`;
  `newIndexToolDeps` (`cmd/mcp1c/indexreg.go`) берёт индекс конкретным типом и `Err()` не
  спрашивает — там сломанный или отсутствующий корпус молча деградирует в «не найдено»:
  индексный слой без индекса синтаксиса не распознаёт вызовы платформенных функций.
- `Err()` спрашивают внутри хендлера, а не при регистрации инструментов: вызов при регистрации
  разбирает корпус в каждой сессии и убивает смысл ленивости.
- Без `source.ConfigureCache` кэш выгрузки ведёт себя как до правки — без истечения и потолка.
  Тест или встраивание, поднимающее `internal/source` мимо `newServer`, сдувания не получит.
- Смена TTL под живым тикером применяется со следующего старта sweeper'а: период фиксируется
  при запуске горутины (`TTL/4`, но не чаще раза в минуту). Записи истекают по правильному
  дедлайну, максимум на один старый период позже.
- Сброс записи кэша не равен падению RSS: механизм обещает отпускание ссылок кэша, а не
  возврат страниц ОС. В трёх изолированных замерах пустой кэш (`entries=0`) давал и
  падение ниже холодного старта, и RSS на уровне прогретой точки; причина разброса не
  установлена (ADR-021, `docs/benchmarks.md`, раздел про память процесса). Одиночный замер
  RSS ничего не доказывает.
- `estimateSize` — оценка reflect-обходом глубины 12 (она же защита от циклов), не точный
  размер; годится только для сравнения с потолком, не для отчётности.
- `evictLocked` всегда оставляет одну запись: коллекция крупнее всего потолка иначе вытесняла бы
  кэш в ноль и себя следом, на каждой вставке.
- RSS не универсален: linux отдаёт текущий (`/proc/self/statm`, `rssKind=current`), darwin и
  BSD — пик через `getrusage` (`rssKind=peak`, вниз не ходит никогда; на darwin `ru_maxrss` в
  байтах, на остальных в кибибайтах), Windows не отдаёт вовсе (`rss_unsupported.go`).
  Подпроцессов для чтения RSS нет и быть не должно — закреплено `nosubprocess_test.go`.
  Отсутствие числа — отсутствующее поле, не ноль.
- `resolve.NewEnv` работает над in-memory `EnvInput`, не над `*store.ReadTx`:
  собирать `EnvInput` из строк, прочитанных транзакцией `store`, обязанность вызывающего
  (`internal/index`), не самого резолвера.
- `query_reference` публикуется только для `StaticityStatic`-текстов запроса (98.6% реальных).
  Partial/dynamic литералы не «с низким confidence» — их просто нет в индексе; `find_queries_using`
  их не увидит.
- `find_impact` не обходит: `query_reference` как ребро, 3 из 4 видов `dependency_edge`
  (кроме `field-typed-by`), `reference` в объект метаданных как цель (`target_object_id` не
  заполняется `publishReference`). Ограничения названы в Description самого инструмента, не
  только в коде.
- Неизвестный `direction` у `trace_call_graph` и `object_graph` (и в graph-режиме)
  отдаёт `invalid_argument`, а не `not_found`; оба графа понимают слова друг друга
  (`in` = `callers`, `out` = `callees`). Таблица синонимов одна: `directionWords` в
  `internal/app/direction.go`.
- Пагинация: `NextCursor` кодирует накопленный offset, а не константу `limit` (иначе вторая
  страница повторяет первую; есть регрессионный тест). Новый курсор сверять с
  `find_symbol`/`find_references`.
- `internal/workspace.SafeJoin` — единственная точка валидации путей для нового кода;
  повторное изобретение join/traversal-проверки в другом месте пробивает границу workspace.
- Правка существующего MCP-инструмента красит `cmd/mcp1c/tools_contract_test.go` и
  `tools_behaviour_contract_test.go` — это предохранитель (контракт менять нельзя), не повод
  их фиксить под новое поведение.
- Новый индексный инструмент — свой файл `cmd/mcp1c/idx_<name>.go` с `func init() {
  registerIndexTool(...) }`; `cmd/mcp1c/indexreg.go` не редактируется.

- `module.owner_object_id` **больше не пуста** — `publishModuleOwner`
  (`internal/index/publish.go`) дописывает её вторым проходом. Прежняя запись «никогда не
  пишется» устарела, но опираться на неё как на полную нельзя: `NULL` штатен у модулей вне
  коллекции (приложения, сеанса, внешнего соединения) и у объекта, которого нет в выгрузке
  (владелец модуля и формы ищется по строке `metadata_object`, не по узлу, почему: doc
  `store.MetadataObjectID`, issue #14);
  неизвестная коллекция даёт диагностику `index_module_owner_unknown_collection`, а НЕВЕРНАЯ
  строка словаря видов (`domain.MetaKinds`, индекс читает его через `ownerTypeToMType` и
  признак `ModuleOwner`) в рантайме неотличима от «объекта просто нет» и ловится только
  `TestRealDumpModuleOwnerFilled` на полной выгрузке. Своей копии словаря у индекса больше нет,
  но виды без `ModuleOwner` (Sequence, ExternalDataSource) по-прежнему дают диагностику. Поэтому `internal/retrieve`
  сознательно выводит владельца из каталога `source_file`, а не по этой цепочке.
- `publishReference` по-прежнему не заполняет `reference.target_object_id` — этот пробел жив.
- Восстановление эпох НИКОГДА не удаляет файл с данными (ADR-023). Указатель `pointer.bin`
  не единственный источник истины: если он показывает на пустую эпоху, а рядом есть эпоха с
  данными, удалять её как orphan нельзя (так теряется весь индекс первым же `index_status`).
  Удаляется только доказанный остаток прерванной сборки, поверх стоит инвариант «есть данные,
  значит не удаляем», а указатель на пустую эпоху при живой соседней даёт `EpochQuarantineError`
  с диагностикой и командой `reindex`, ничего не трогая.
- Уборка эпохи на старте идёт ТОЛЬКО через `(*Store).dropUnpublishedEpoch`, номер
  аварийной сборки берётся `nextFreeEpoch`. `buildEpoch` различает режимы:
  `buildDeliberate` только у `Rebuild` (ротация, преемник уже с данными, инвариант не
  действует), `buildEmergency` у всего, что идёт из `start` через `buildEmptyEpoch`
  (преемник пустой, инвариант действует). Новый путь удаления эпохи заводить нельзя.
- Целость эпохи проверять содержимым (`componentsInEpochFile`), а не `os.Stat`:
  уничтоженная и заново созданная пустая эпоха занимает то же имя и почти тот же размер.
- Журнал поколений НЕ пишется: таблица `generation_log` объявлена, `WriteTx.LogGeneration`
  не вызывается ниоткуда. Не опирайся на него как на источник истины о публикации эпохи.

- **Раскладка выгрузки в фикстуре.** `DumpConfigToFiles` кладёт объявление файлом РЯДОМ с
  каталогом модулей (`Documents/X.xml` + `Documents/X/Ext/ObjectModule.bsl`). Вложенной
  `Documents/X/X.xml` не бывает. Фикстура с выдуманной раскладкой маскирует дефекты
  `objectModuleDir`: тест зелёный, а на реальной выгрузке находится обработчик проведения
  ЧУЖОГО документа. Пути брать из `workspace.Dump*` (ADR-033).
- Шаг миграции без DDL с `needsFullRebuild`: способ объявить СОДЕРЖИМОЕ индексов прежней
  версии ненадёжным, когда выход парсера не менялся (схема 3, ADR-037: инкремент до неё обрывал
  указатели нетронутых файлов на пересозданные узлы; схема 4, ADR-038: сносил каскадом их права
  ролей, рёбра и бейджи графа; схема 6, issue #14: оставлял `module.owner_object_id` на
  удалённом объекте; схема 7, issue #15: индексация не заполняла `symbol.region` и
  `symbol.doc_first_line`). `ParserVersion` ради этого не поднимать. Шаг с DDL (схема 8:
  колонка `symbol.doc`, полный комментарий метода) требует ещё и обратного хода в
  `internal/store/storetest` (`DowngradeToSchema7Statements`): тесты миграции получают базу
  прежней версии откатом свежей, и без него `ALTER TABLE` падает на существующей колонке.
  Переключение бинарника `main` (схема 2) и схемы 3 на одном `--projects-root` каждый раз
  стоит полной пересборки: схема 3 требует её у эпохи версии 2, а `main` на эпохе версии 3
  заводит новую пустую эпоху.
- **Подъём `ParserVersion` — это полная пересборка индекса каждого проекта.** Файл со старым
  `parser_version` считается изменённым; пока проект не пересобран, он честно считает себя
  устаревшим, пока его не переиндексируют. Не поднять версию хуже: индекс молча отдаёт факты
  старого парсера.
- Принудительный `reindex` **не проходит через precheck** — он всегда запускает пайплайн, а
  тот по правилу `toRead` дочитывает все гидратированные записи компонента. Число «перебранных
  файлов» у него не зависит от механизма свежести и **ничего о ней не говорит**. Свежесть
  меряется путём `EnsureFresh`: мгновенный ответ без `stale_index` достижим ровно при
  `work.changed == 0`.
- `stale` у индексных инструментов (кроме `get_context_for_task`) не «проверено сейчас», а
  «проверено не раньше TTL назад» (ADR-036): правка, сделанная внутри окна 30 с, видна со
  следующего обхода. `CachedFreshness` индекс не меняет и пересборку не планирует; не
  подменять её на `EnsureFresh` ради «точности»: тот синхронно гоняет инкремент в ответе
  инструмента, а бюджет p50 `find_symbol` 20 мс.
- `precheckWork` не сводить обратно к одному числу: «нужен ли прогон» решается по `changed`,
  «синхронно или в фон» — по `pending`. Одна правка после рестарта иначе уходит в синхронный
  инкремент, который дочитывает весь проект (на крупной конфигурации ~14 минут блокировки
  вызывающего).
- Гидратированная запись корпуса на входе в `buildEnvInput` — ошибка и аборт write-транзакции,
  не пустой `Env`. Инвариант не обходить: восстановленная из `source_file` запись фактов не
  несёт, и молча собранное на ней окружение резолвера опубликовало бы НЕВЕРНОЕ разрешение имён.
- Бывает, что у документа перехватчик расширения на проведение есть, а базового
  `ObjectModule.bsl` нет вовсе — так и в самой базе. Это свойство конфигурации, а не дефект
  связывания. `posting` этот случай обслуживает (ADR-034): перехватчик и его движения в ответе,
  отсутствие базового метода — предупреждением `posting_base_handler_missing` в ОБОИХ view.
  Синтетика — `internal/retrieve/postingnobase_test.go`, реальная выгрузка —
  `TestRealDumpPostingWithoutBaseHandler` с документом из `ONEC_POSTING_NOBASE`. Любая другая
  ошибка там красная.
- **Строители `workspace.Dump*` ПАНИКУЮТ на неописанном виде объекта.** Для фикстур это
  правильно, в production смертельно: `mtype` приходит из индекса без белого списка, а
  единственный `recover` сервера — в `internal/store`, вокруг диспетчеризации инструментов
  его нет, поэтому паника кладёт весь процесс вместе с индексом. Production-код спрашивает
  двухзначную `DumpCollectionDir(mtype) (string, bool)` и честно отвечает «путь не вывелся»
  (образец — `retrieve/expand2.go:postingObjectModulePath`, ADR-034).
- `TestRealDumpFormIntentReturnsHandlerData` (`internal/retrieve/realdump_test.go`) красный на
  любой выгрузке, кроме `ut_demo`: его подслучаи написаны под неё. Не регрессия и не повод
  чинить код.
- `index_status` свежего процесса сливает персистентные диагностики эпохи со своими, а не
  подменяет их: признак «этот процесс сам индексировал» — флаг `reindexedHere`, а не непустота
  `lastDiagnostics` (туда же пишет `ensureHydrated`). Не заменять флаг проверкой длины.
- Диагностики `index_status`/`reindex` дозируются: первые `diagnosticsSample = 10` плюс
  `diagnosticsDigest` (счётчик, разбивка по кодам, `truncated`, `note`), полный список — по
  входу `includeAllDiagnostics`. Одну и ту же диагностику второй раз не класть:
  `ReindexComponentResult.Diagnostics` удалён, принадлежность компоненту — в
  `domain.Diagnostic.Component`.
- `stageAccum.stages()` (`internal/index/stagetiming.go`): синтетический этап `commit` — остаток
  общей длительности сверх суммы измеренных этапов, и считать его нужно от суммы УЖЕ округлённых
  до мс значений остальных этапов (`total.Milliseconds() - sum(.Milliseconds())`), не от суммы
  сырых `time.Duration`. Независимое усечение каждого этапа в `.Milliseconds()` теряет свою долю
  миллисекунды у каждого, и сумма этапов расходится с `durationMs` на единицы мс (закреплено
  юнит-тестом).
- Список ID в SQL (файлы, рёбра) бывает неограниченной длины: полная пересборка `ut_demo` несёт
  48 699 файлов. Плейсхолдер на элемент упирается в лимит SQLite на число переменных (`too many
  SQL variables`), поэтому такой список уходит одним параметром, JSON-массивом через `json_each`
  (образец `edgesDependingOnFiles` в `internal/store/objectgraph.go`). Не батчить: сравнение
  «было / стало» должно оставаться одним отбором.
- `suppressFormNoiseWhenMatched` (`internal/retrieve/build.go`) гасит `no_forms`/
  `form_binding_not_matched` только для intent=form и только внутри ГРУППЫ анкеров одного
  `(ObjectType, ObjectName)`. Не расширять: глобальное подавление по любой находке в ответе
  теряет честное `no_forms` про несвязанный объект, а группировка только по имени объединяет
  объекты РАЗНЫХ видов с одним именем (Catalog и CommonPicture «Номенклатура», как в
  комментарии `dedupWarnings`). `Component` в ключе группировки сознательно нет: тот же тип и
  имя в базовой конфигурации и в расширении и есть подавляемый случай. Тексты предупреждений
  (`expandForm`, `internal/retrieve/expand2.go`) называют тип и компонент
  (`"у объекта %s.%s (%s) …"`), иначе рядом с настоящей находкой того же имени читаются как
  противоречие. Поведение закреплено пятью тестами `internal/retrieve/formnoise_test.go`:
  `TestFormIntentSuppressesNoFormsNoiseFromMatchedHomonym`, `TestFormIntentKeepsNoFormsWhenNoMatchAnywhere`,
  `TestFormIntentDoesNotSuppressNoFormsForUnrelatedObject`,
  `TestFormIntentDoesNotSuppressNoFormsForSameNameDifferentType`,
  `TestFormIntentNoFormsMessageNamesTypeAndComponent`.
