package index

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
)

// planFixture разбирает фикстуру пайплайна (общий модуль и вызывающий модуль
// менеджера) и разрешает ссылки в памяти: всё, что нужно плану, без store.
func planFixture(t *testing.T, extra map[string]string) (planContext, *componentCorpus) {
	t.Helper()
	root := writeFixtureComponent(t)
	for rel, content := range extra {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	discovered, err := discoverComponent(root, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	corpus := newComponentCorpus("cfg")
	for _, d := range discovered {
		data, err := os.ReadFile(d.absPath)
		if err != nil {
			t.Fatal(err)
		}
		corpus.files[d.relPath] = parseOneFile(d.relPath, data)
	}
	layer := domain.BaseLayer("cfg")
	envIn, err := buildEnvInput("proj", "cfg", layer, corpus)
	if err != nil {
		t.Fatal(err)
	}
	env, err := resolve.NewEnv(envIn, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolveAndUpdate(env, corpus, nil)
	return planContext{project: "proj", component: "cfg", layer: layer, env: env, resolved: corpus.resolved}, corpus
}

// TestPlanFileBuildsRowsOnIdentityKeys: план файла строится без SQLite и
// без id. Символы и объект адресуются identity_key, а id-поля строк пусты:
// их проставляет писатель. Повтор имени под взаимоисключающими ветками
// #Если получает ключ первого объявления и диагностику, а не свою строку.
func TestPlanFileBuildsRowsOnIdentityKeys(t *testing.T) {
	const dupRel = "CommonModules/Дубли/Ext/Module.bsl"
	pc, corpus := planFixture(t, map[string]string{dupRel: `
#Если Сервер Тогда
Процедура Двойная() Экспорт
КонецПроцедуры
#Иначе
Процедура Двойная() Экспорт
КонецПроцедуры
#КонецЕсли
`})

	const common = "CommonModules/УтилитыОбщие/Ext/Module.bsl"
	p, err := planFile(pc, common, corpus.files[common])
	if err != nil {
		t.Fatal(err)
	}
	if p.module == nil || len(p.module.symbols) != 1 {
		t.Fatalf("план модуля %+v, ожидался один символ", p.module)
	}
	sym := p.module.symbols[0].row
	if sym.NameNorm != domain.NormalizeName("Помощь") || !sym.IsExport {
		t.Errorf("символ %+v, ожидался экспортный Помощь", sym)
	}
	if sym.ModuleID != 0 || sym.OriginFileID != 0 {
		t.Errorf("план проставил id: module=%d file=%d", sym.ModuleID, sym.OriginFileID)
	}
	if p.module.methodKeys[0] != sym.IdentityKey {
		t.Errorf("ключ метода %q, ожидался %q", p.module.methodKeys[0], sym.IdentityKey)
	}

	x, err := planFile(pc, "CommonModules/УтилитыОбщие.xml", corpus.files["CommonModules/УтилитыОбщие.xml"])
	if err != nil {
		t.Fatal(err)
	}
	if x.object == nil || x.object.commonModule == nil {
		t.Fatalf("план XML общего модуля без объекта или module_context: %+v", x.object)
	}
	if x.object.commonModule.IdentityKey != p.module.module.IdentityKey {
		t.Errorf("XML и Module.bsl дают разные identity модуля: %q и %q",
			x.object.commonModule.IdentityKey, p.module.module.IdentityKey)
	}
	if x.object.row.FileID != 0 {
		t.Errorf("план проставил file_id объекта: %d", x.object.row.FileID)
	}

	d, err := planFile(pc, dupRel, corpus.files[dupRel])
	if err != nil {
		t.Fatal(err)
	}
	if len(d.module.symbols) != 1 || len(d.module.dupDiagnostics) != 1 {
		t.Fatalf("символов %d, диагностик повтора %d, ожидалось 1 и 1",
			len(d.module.symbols), len(d.module.dupDiagnostics))
	}
	if d.module.dupDiagnostics[0].Code != "index_duplicate_symbol_uid" {
		t.Errorf("код диагностики %q", d.module.dupDiagnostics[0].Code)
	}
	if len(d.module.methodKeys) != 2 || d.module.methodKeys[0] != d.module.methodKeys[1] {
		t.Errorf("повтор метода не получил ключ первого объявления: %q", d.module.methodKeys)
	}
}

// TestPlanFileLinksCarryTargetsAsKeys: план прохода 2 несёт цель вызова и
// метод-владелец ключами, а не id: разрешение в id делает писатель, и только
// он знает, есть ли узел цели в store.
func TestPlanFileLinksCarryTargetsAsKeys(t *testing.T) {
	pc, corpus := planFixture(t, nil)
	const caller = "Catalogs/Товары/Ext/ManagerModule.bsl"
	fp, err := planFile(pc, caller, corpus.files[caller])
	if err != nil {
		t.Fatal(err)
	}
	lp := planLinks(pc, caller, corpus.files[caller], fp.module.methodKeys)
	if len(lp.refs) != 1 {
		t.Fatalf("ссылок в плане %d, ожидалась 1", len(lp.refs))
	}
	r := lp.refs[0]
	if r.callerKey == "" || r.callerKey != fp.module.methodKeys[0] {
		t.Errorf("владелец ссылки %q, ожидался ключ метода Тест %q", r.callerKey, fp.module.methodKeys[0])
	}
	if r.targetKey == "" || r.row.TargetSymbolID != 0 {
		t.Errorf("цель ссылки: ключ %q, id %d; ожидался ключ символа без id", r.targetKey, r.row.TargetSymbolID)
	}
	if r.row.Resolution != string(domain.ResolutionResolved) || len(r.consulted) == 0 {
		t.Errorf("ссылка %+v: ожидались resolved и консультированные ключи", r.row)
	}
	if lp.ownerKey == "" {
		t.Error("модуль менеджера без ключа владельца")
	}

	// Цель вызова в плане совпадает с ключом символа из плана его файла.
	const common = "CommonModules/УтилитыОбщие/Ext/Module.bsl"
	cp, err := planFile(pc, common, corpus.files[common])
	if err != nil {
		t.Fatal(err)
	}
	if r.targetKey != cp.module.symbols[0].row.IdentityKey {
		t.Errorf("ключ цели %q не совпал с ключом символа %q", r.targetKey, cp.module.symbols[0].row.IdentityKey)
	}
}

// TestPlanFileOrderedApply: планы строятся параллельно, а применяются строго
// по порядку; ошибка плана останавливает выдачу, паника плана всплывает у
// вызывающего, а не роняет процесс из чужой горутины.
func TestPlanFileOrderedApply(t *testing.T) {
	const n = 3*orderedWindow + 5
	var got []int
	err := runOrdered(8, n, func(i int) (int, error) { return i * 2, nil }, func(i, v int) error {
		if v != i*2 {
			t.Fatalf("план %d применён со значением %d", i, v)
		}
		got = append(got, i)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n || !sort.IntsAreSorted(got) {
		t.Fatalf("применено %d планов, по порядку: %v", len(got), sort.IntsAreSorted(got))
	}

	boom := errors.New("отказ плана")
	applied := 0
	err = runOrdered(4, n, func(i int) (int, error) {
		if i == 10 {
			return 0, boom
		}
		return i, nil
	}, func(int, int) error { applied++; return nil })
	if !errors.Is(err, boom) || applied != 10 {
		t.Fatalf("ошибка %v, применено %d; ожидались %v и 10", err, applied, boom)
	}

	defer func() {
		if p := recover(); p != "паника плана" {
			t.Fatalf("recover = %v, ожидалась паника плана", p)
		}
	}()
	_ = runOrdered(4, n, func(i int) (int, error) {
		if i == 7 {
			panic("паника плана")
		}
		return i, nil
	}, func(int, int) error { return nil })
	t.Fatal("паника плана не дошла до вызывающего")
}
