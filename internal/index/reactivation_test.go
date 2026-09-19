package index

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// TestRealDumpIncrementalReactivation — доводка P4: reindex(mode=incremental,
// default) на СВЕЖЕМ Service (свежий резидентный corpus, §17 «корпус —
// резидентное состояние, живёт МЕЖДУ вызовами Reindex ОДНОГО процесса», см.
// runComponent/corpusFor) поверх store, уже содержащего опубликованную full-
// эпоху из ДРУГОГО Service — ровно то, что делает MCP-сервер, когда новый
// процесс реактивирует уже существующий проект из реестра (internal/app.
// Projects открывает store и заводит НОВЫЙ index.Service лениво, на первое
// обращение этого процесса). До фикса падал на FOREIGN KEY (role_right.
// object_id -> metadata_object.id): пустой corpus заставляет incremental-путь
// считать вообще все файлы компонента изменившимися (fingerprint не находит
// ни одной "known" записи), incrementalRepublishSet уходит в fallback «весь
// компонент как full», но publishFiles ВСЁ РАВНО СНАЧАЛА удаляет старые факты
// republish-набора (шаг (2) раздела 15) — то, чего full-рёбилд в пустую эпоху
// никогда не делает (там просто нечего удалять). Rights.xml почти всегда
// republish-ится раньше объекта, на чьё право он ссылается (алфавит: "Roles/"
// раньше "Subsystems/"): его node уже пережил удаление (стабильная identity),
// а metadata_object-строка — ещё нет, InsertRoleRight падает на FK. Фикс —
// publishRoleRights переехал из прохода 1 в проход 2 publishFiles (см.
// publish.go): та же гарантия «все цели транзакции уже вставлены», что уже
// была у обычных ссылок и handler_binding, просто не была дана role_right.
func TestRealDumpIncrementalReactivation(t *testing.T) {
	root := realDumpRoot(t)
	dir := t.TempDir()

	m := workspace.Manifest{
		Version: 1, Project: "utdemo-react", Root: root,
		Components: []workspace.Component{
			{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root},
		},
	}
	builtins := syntaxtest.RealOrSkip(t)
	ctx := context.Background()

	// "Процесс А": холодный полный индекс с нуля, отдельный store.Store и
	// index.Service, закрываются полностью (как выход процесса) до открытия
	// следующих — не просто новый Service поверх уже открытого store.
	stA, err := store.Open(dir, store.Options{ProjectID: "utdemo-react", StateDirName: testStateDir})
	if err != nil {
		t.Fatalf("store.Open (A): %v", err)
	}
	svcA := NewService(stA, "utdemo-react", m, builtins, Config{})
	if _, err := svcA.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full, процесс А): %v", err)
	}
	if err := svcA.Close(); err != nil {
		t.Fatalf("svcA.Close: %v", err)
	}
	if err := stA.Close(); err != nil {
		t.Fatalf("stA.Close: %v", err)
	}

	// "Процесс Б": свежий store.Open на ТОМ ЖЕ каталоге (существующая эпоха
	// читается с диска, не пересобирается) и свежий index.Service — свежий,
	// пустой резидентный corpus. reindex без явного mode = incremental
	// (default, ровно как в отчёте).
	stB, err := store.Open(dir, store.Options{ProjectID: "utdemo-react", StateDirName: testStateDir})
	if err != nil {
		t.Fatalf("store.Open (Б): %v", err)
	}
	t.Cleanup(func() { stB.Close() })
	svcB := NewService(stB, "utdemo-react", m, builtins, Config{})
	t.Cleanup(func() { svcB.Close() })

	res, err := svcB.Reindex(ctx, ModeIncremental, "")
	if err != nil {
		if strings.Contains(err.Error(), "FOREIGN KEY") {
			t.Fatalf("реактивация incremental на свежем процессе упала на FK (регресс воспроизведён): %v", err)
		}
		t.Fatalf("Reindex(incremental, процесс Б, реактивация): %v", err)
	}
	if len(res.Components) != 1 {
		t.Fatalf("Components = %+v, want 1", res.Components)
	}

	// index_status обязан остаться читаемым и свежим после реактивации —
	// не только "не упал".
	stB2, err := stB.Status(ctx)
	if err != nil {
		t.Fatalf("Status после реактивации: %v", err)
	}
	if stB2.NeedsFullRebuild {
		t.Errorf("NeedsFullRebuild = true после успешной реактивации, want false")
	}

	err = stB.Read(ctx, func(tx *store.ReadTx) error {
		bad, err := tx.Validate()
		if err != nil {
			return err
		}
		if len(bad) != 0 {
			t.Errorf("Validate() после реактивации = %v, want пусто", bad)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
}

// TestRealDumpDiagnosticsSurviveReactivation — доводка P7: находка — таблица
// diagnostic реально пишется store.WriteTx.InsertDiagnostic на каждый
// Reindex (см. publish.go), но до этого фикса ничто её не читало обратно:
// index.Service.Status отдавал ТОЛЬКО s.lastDiagnostics — поле процесса,
// заполняемое единственно внутри Reindex ЭТОГО ЖЕ процесса (service.go). Для
// свежего процесса B (типичный сценарий: MCP-сервер — не долгоживущий процесс
// между сессиями агента) lastDiagnostics пуст всегда, и index_status молчал о
// diagnostics, физически уже лежащих в SQLite от прошлого Reindex процесса A —
// даже когда никто на диске ничего не менял (тот же generation).
//
// Тот же двухпроцессный паттерн, что TestRealDumpIncrementalReactivation
// (P4): store.Open/Close по-настоящему дважды, не два Service поверх одного
// открытого store — иначе фолбэк на s.lastDiagnostics процесса A замаскировал
// бы отсутствие чтения из store.
//
// "Что именно должна показать Б" сверяется НЕ с svcA.Status().LastDiagnostics
// (in-memory самоотчёт процесса А), а с сырым чтением таблицы diagnostic
// ЭТОГО ЖЕ store (тем же tx.Diagnostics, которым пользуется фикс) до закрытия
// процесса А. Причина — отдельная, не связанная с P7 находка, вскрытая этим
// тестом: publishModuleSymbols вставляет diagnostic "index_duplicate_symbol_uid"
// (см. publish.go) сразу в store, но НЕ возвращает его в ComponentResult.
// Diagnostics/s.lastDiagnostics — на ut_demo из-за этого svcA.Status() сам
// видит МЕНЬШЕ diagnostics, чем реально лежит в store (в этом прогоне: 1
// против 63). Это баг самостоятельный (диагностика теряется уже на пути
// publish -> Reindex-result, до P7 и до store-фолбэка), сюда не входит —
// сравнение со «своим» in-memory отчётом процесса А было бы нечестным тестом
// (потребовало бы, чтобы Б повторил ЧУЖУЮ недостачу). Тест П7 обязан
// проверить ровно то, что находка называет проблемой: всё, что физически
// осело в store, свежий процесс обязан увидеть через index_status.
func TestRealDumpDiagnosticsSurviveReactivation(t *testing.T) {
	root := realDumpRoot(t)
	dir := t.TempDir()

	m := workspace.Manifest{
		Version: 1, Project: "utdemo-diag", Root: root,
		Components: []workspace.Component{
			{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root},
		},
	}
	builtins := syntaxtest.RealOrSkip(t)
	ctx := context.Background()

	// "Процесс А": полный индекс с нуля — реальная выгрузка ut_demo даёт
	// diagnostics (index_empty_parse, index_duplicate_symbol_uid и подобные,
	// см. находку) без специальной порчи фикстур.
	stA, err := store.Open(dir, store.Options{ProjectID: "utdemo-diag", StateDirName: testStateDir})
	if err != nil {
		t.Fatalf("store.Open (A): %v", err)
	}
	svcA := NewService(stA, "utdemo-diag", m, builtins, Config{})
	if _, err := svcA.Reindex(ctx, ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full, процесс А): %v", err)
	}
	genA, err := svcA.Status(ctx)
	if err != nil {
		t.Fatalf("Status (процесс А): %v", err)
	}

	// Ground truth — сырое чтение store ДО закрытия процесса А: то, что
	// Reindex процесса А реально опубликовал (не то, что его собственный
	// in-memory путь решил показать, см. doc-комментарий выше).
	var wantRows []store.DiagnosticRow
	err = stA.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		wantRows, err = tx.Diagnostics(diagnosticsFallbackLimit)
		return err
	})
	if err != nil {
		t.Fatalf("сырое чтение diagnostic (процесс А, до закрытия): %v", err)
	}
	if len(wantRows) == 0 {
		t.Skip("реальная выгрузка не дала ни одной diagnostic на полном индексе — сценарию нечего проверять (см. docs/benchmarks.md про ожидаемые ~47)")
	}

	if err := svcA.Close(); err != nil {
		t.Fatalf("svcA.Close: %v", err)
	}
	if err := stA.Close(); err != nil {
		t.Fatalf("stA.Close: %v", err)
	}

	// "Процесс Б": свежий store.Open на ТОМ ЖЕ каталоге и свежий
	// index.Service — БЕЗ единого вызова Reindex этим процессом. Это ровно
	// сценарий из находки: агент открывает новую сессию, MCP-сервер
	// запускается заново, первый вызов — index_status.
	stB, err := store.Open(dir, store.Options{ProjectID: "utdemo-diag", StateDirName: testStateDir})
	if err != nil {
		t.Fatalf("store.Open (Б): %v", err)
	}
	t.Cleanup(func() { stB.Close() })
	svcB := NewService(stB, "utdemo-diag", m, builtins, Config{})
	t.Cleanup(func() { svcB.Close() })

	statusB, err := svcB.Status(ctx)
	if err != nil {
		t.Fatalf("Status (процесс Б, без единого Reindex): %v", err)
	}
	// Тот же generation, ничего на диске не менялось между А и Б — контроль,
	// что сравнение ниже честное (не «разные эпохи случайно совпали числом»).
	if statusB.Store.Generation != genA.Store.Generation {
		t.Fatalf("Generation Б = %v, want то же, что А = %v (между процессами файлы не менялись)",
			statusB.Store.Generation, genA.Store.Generation)
	}
	if len(statusB.LastDiagnostics) == 0 {
		t.Fatalf("index_status процесса Б: diagnostics пуст, want %d записей — те же, что реально лежат в store "+
			"(diagnostic физически осел там от Reindex процесса А, но Status свежего процесса его не прочитал)",
			len(wantRows))
	}
	if len(statusB.LastDiagnostics) != len(wantRows) {
		t.Errorf("len(diagnostics) Б = %d, want %d (столько же, сколько реально лежит в store)",
			len(statusB.LastDiagnostics), len(wantRows))
	}

	keyOfRow := func(r store.DiagnosticRow) string {
		return fmt.Sprintf("%s|%s|%s|%s", r.Code, r.File, r.Severity, r.Message)
	}
	keyOfDiag := func(d domain.Diagnostic) string {
		return fmt.Sprintf("%s|%s|%s|%s", d.Code, d.File, d.Severity, d.Message)
	}
	want := make([]string, len(wantRows))
	for i, r := range wantRows {
		want[i] = keyOfRow(r)
	}
	got := make([]string, len(statusB.LastDiagnostics))
	for i, d := range statusB.LastDiagnostics {
		got[i] = keyOfDiag(d)
	}
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("diagnostics процесса Б расходятся с тем, что реально лежит в store:\nwant (первые 10 из %d): %v\ngot  (первые 10 из %d): %v",
			len(want), firstNStr(want, 10), len(got), firstNStr(got, 10))
	}
}

// TestRealDumpReindexResultDiagnosticsMatchStore:
// находка, вскрытая ПРИ проверке P7 (doc-комментарий
// TestRealDumpDiagnosticsSurviveReactivation выше) — publishModuleSymbols
// вставляет diagnostic "index_duplicate_symbol_uid" сразу в store
// (tx.InsertDiagnostic), но раньше не добавлял её в возвращаемый
// ComponentResult.Diagnostics (в отличие от rec.diagnostics — напр.
// index_empty_parse, — которые пишутся в store И собираются в
// out.diagnostics ещё в проходе 1 publishFiles). Из-за этого именно
// САМООТЧЁТ процесса, только что сделавшего reindex (res.Components[i].
// Diagnostics, из которого строится и s.lastDiagnostics), занижал число
// diagnostics — не только у "чужого" свежего процесса (это уже покрыто P7 /
// TestRealDumpDiagnosticsSurviveReactivation, который сознательно сверяется
// с сырым store, а не с этим самоотчётом, чтобы не наследовать его
// недостачу).
//
// Тест — ОДИН процесс (в отличие от P4/P7): svc.Reindex(full) → сразу
// сравнить res.Components[0].Diagnostics (то, что publishFiles реально
// вернул ИЗ ЭТОЙ ЖЕ транзакции) с tx.Diagnostics того же store, прочитанным
// сразу после commit. До фикса это расходилось: store содержал реальное
// число index_duplicate_symbol_uid (на ut_demo в находке — 62 из 63 diag-
// ностик), а ComponentResult.Diagnostics — только rec.diagnostics (в
// находке: 1).
func TestRealDumpReindexResultDiagnosticsMatchStore(t *testing.T) {
	root := realDumpRoot(t)
	st := openTestStore(t)
	m := workspace.Manifest{
		Version: 1, Project: "utdemo-selfreport", Root: root,
		Components: []workspace.Component{
			{ID: "cfg", Kind: domain.KindConfiguration, Root: ".", AbsRoot: root},
		},
	}
	builtins := syntaxtest.RealOrSkip(t)
	ctx := context.Background()
	svc := NewService(st, "utdemo-selfreport", m, builtins, Config{})
	t.Cleanup(func() { svc.Close() })

	res, err := svc.Reindex(ctx, ModeFull, "")
	if err != nil {
		t.Fatalf("Reindex(full): %v", err)
	}
	if len(res.Components) != 1 {
		t.Fatalf("Components = %+v, want 1", res.Components)
	}

	// Ground truth — сырое чтение store СРАЗУ после commit того же Reindex,
	// тем же процессом. Раздельного tx.check() по коду не делаем: store уже
	// закрыл write-транзакцию, читаем через штатный store.Read.
	var storeRows []store.DiagnosticRow
	err = st.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		storeRows, err = tx.Diagnostics(diagnosticsFallbackLimit)
		return err
	})
	if err != nil {
		t.Fatalf("сырое чтение diagnostic после Reindex: %v", err)
	}

	countByCode := func(rows []store.DiagnosticRow, code string) int {
		n := 0
		for _, r := range rows {
			if r.Code == code {
				n++
			}
		}
		return n
	}
	countDiagByCode := func(ds []domain.Diagnostic, code string) int {
		n := 0
		for _, d := range ds {
			if d.Code == code {
				n++
			}
		}
		return n
	}

	wantDup := countByCode(storeRows, "index_duplicate_symbol_uid")
	if wantDup == 0 {
		t.Skip("реальная выгрузка не дала ни одной index_duplicate_symbol_uid — сценарию нечего проверять")
	}
	if wantDup <= 1 {
		t.Skipf("index_duplicate_symbol_uid в store = %d, недостаточно для содержательной проверки занижения (нужно >1)", wantDup)
	}

	gotDup := countDiagByCode(res.Components[0].Diagnostics, "index_duplicate_symbol_uid")
	if gotDup != wantDup {
		t.Errorf("ComponentResult.Diagnostics: index_duplicate_symbol_uid = %d, want %d (столько же, сколько реально осело в store в ЭТОЙ ЖЕ транзакции)",
			gotDup, wantDup)
	}

	// Полная сверка всех diagnostics (не только dup-кода) — тем же ключом,
	// что и P7, чтобы фикс не подменил одну недостачу другой.
	keyOfRow := func(r store.DiagnosticRow) string {
		return fmt.Sprintf("%s|%s|%s|%s", r.Code, r.File, r.Severity, r.Message)
	}
	keyOfDiag := func(d domain.Diagnostic) string {
		return fmt.Sprintf("%s|%s|%s|%s", d.Code, d.File, d.Severity, d.Message)
	}
	want := make([]string, len(storeRows))
	for i, r := range storeRows {
		want[i] = keyOfRow(r)
	}
	got := make([]string, len(res.Components[0].Diagnostics))
	for i, d := range res.Components[0].Diagnostics {
		got[i] = keyOfDiag(d)
	}
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("ComponentResult.Diagnostics (самоотчёт процесса, только что сделавшего reindex) расходится со store:\nwant (первые 10 из %d): %v\ngot  (первые 10 из %d): %v",
			len(want), firstNStr(want, 10), len(got), firstNStr(got, 10))
	}
}
