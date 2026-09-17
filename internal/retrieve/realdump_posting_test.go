package retrieve

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/index"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
	"github.com/blessed2k/1C-WORKFLOW/internal/syntax/syntaxtest"
	"github.com/blessed2k/1C-WORKFLOW/internal/workspace"
)

// postingIntercept — документ выгрузки, слой расширения, имя перехватчика
// его обработчика проведения и вид перехвата. Набор задаётся окружением,
// потому что пары «документ @ расширение» свои у каждой выгрузки:
//
//	ONEC_POSTING_INTERCEPTS="Документ:Вид:слой:ИмяПерехватчика;..."
//	ONEC_POSTING_NOBASE="Документ:Вид:слой:ИмяПерехватчика"
//
// Вид — После, Перед или Вместо; слой — id компонента из 1c-project.json.
// ONEC_POSTING_NOBASE называет документ без базового модуля объекта, чьё
// проведение целиком написано расширением. Без переменной тест пропускается.
type postingIntercept struct {
	doc         string
	module      string
	layer       string
	interceptor string
	kind        string
}

// postingInterceptsFromEnv разбирает переменную env в список пар. Пустая
// переменная — пропуск теста, битая запись — красный: опечатка в окружении
// не должна выглядеть как «проверять нечего».
func postingInterceptsFromEnv(t *testing.T, env string) []postingIntercept {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		t.Skipf("%s не задан — пары «документ @ расширение» для этой выгрузки не названы", env)
	}
	var out []postingIntercept
	for _, item := range strings.Split(raw, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		parts := strings.Split(item, ":")
		if len(parts) != 4 {
			t.Fatalf("%s: запись %q, want Документ:Вид:слой:ИмяПерехватчика", env, item)
		}
		doc := strings.TrimSpace(parts[0])
		out = append(out, postingIntercept{
			doc:         doc,
			module:      workspace.DumpModulePath("Document", doc, workspace.ModuleObject),
			kind:        strings.TrimSpace(parts[1]),
			layer:       strings.TrimSpace(parts[2]),
			interceptor: domain.NormalizeName(strings.TrimSpace(parts[3])),
		})
	}
	if len(out) == 0 {
		t.Skipf("%s не содержит ни одной записи", env)
	}
	return out
}

// realDumpPostingProject — проект на РЕАЛЬНОЙ выгрузке вместе с её слоями:
// манифест читается из самой выгрузки (1c-project.json), а не собирается в
// памяти одним компонентом, как в соседних real-dump тестах этого пакета —
// иначе расширений в проекте не будет и проверять перехватчики не на чем.
//
// Пропуск только один: выгрузки или пар в окружении нет. Слой, названный в
// окружении, но не объявленный манифестом, — красный: окружение описывает
// другую выгрузку. Любой отказ ВНУТРИ прогона (индексация, чтение,
// несовпадение факта) тоже красный: сторож, превращающий любую ошибку в Skip,
// уже один раз позволил регрессии «символ перестал находиться» вернуться
// зелёным прогоном.
func realDumpPostingProject(t *testing.T, pairs []postingIntercept) (*index.Service, *store.Store, domain.ProjectID) {
	t.Helper()
	root := retrieveRealDumpRoot(t)
	manifest, err := workspace.LoadManifest(root)
	if err != nil {
		t.Skipf("в выгрузке %s нет читаемого 1c-project.json (%v) — слоёв расширений нет", root, err)
	}
	have := map[domain.ComponentID]bool{}
	for _, c := range manifest.Extensions() {
		have[c.ID] = true
	}
	for _, pair := range pairs {
		if !have[domain.ComponentID(pair.layer)] {
			t.Fatalf("выгрузка %s не объявляет слой %s из окружения", root, pair.layer)
		}
	}

	builtins := syntaxtest.RealOrSkip(t)
	workspaceRoot := t.TempDir()
	st, err := store.Open(workspaceRoot, store.Options{ProjectID: manifest.Project, StateDirName: workspace.RegistryDirName})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	svc := index.NewService(st, manifest.Project, manifest, builtins, index.Config{})
	t.Cleanup(func() { svc.Close() })

	t0 := time.Now()
	if _, err := svc.Reindex(context.Background(), index.ModeFull, ""); err != nil {
		t.Fatalf("Reindex(full) на реальной выгрузке %s: %v", root, err)
	}
	t.Logf("полная индексация %s: %s", root, time.Since(t0))
	return svc, st, manifest.Project
}

// TestRealDumpPostingBindsOwnDocument — постоянный real-dump тест на intent
// posting. У пакета их не было ни одного, хотя весь прогон чинил именно
// posting: регрессию, отдававшую обработчик проведения ЧУЖОГО документа
// (модуль другого документа вместо запрошенного), поймала приёмка
// на живой базе, а не прогон — фикстура описывала раскладку
// "Documents/Заказ/Заказ.xml", которой DumpConfigToFiles не производит, и
// зелёный тест не значил ничего.
//
// Тест утверждает две вещи, и обе — на реальной раскладке:
//
//  1. posting_handler — модуль ИМЕННО этого документа, а не омонима;
//  2. перехватчик своего слоя в ответе есть, со своим видом перехвата.
func TestRealDumpPostingBindsOwnDocument(t *testing.T) {
	pairs := postingInterceptsFromEnv(t, "ONEC_POSTING_INTERCEPTS")
	svc, st, projectID := realDumpPostingProject(t, pairs)
	ctx := context.Background()

	for _, pair := range pairs {
		t.Run(pair.doc, func(t *testing.T) {
			task := fmt.Sprintf("в проведении документа %s не формируются движения по регистрам", pair.doc)
			res, err := Run(ctx, svc, st, Request{
				Task: task, ProjectID: projectID, View: "effective", Freshness: FreshnessAllowStale,
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if res.Intent.Primary != IntentPosting {
				t.Fatalf("Intent.Primary = %q, want %q (задача %q)", res.Intent.Primary, IntentPosting, task)
			}

			// 1. Обработчик проведения принадлежит ЭТОМУ документу.
			var handlerModules []string
			for _, s := range res.Snippets {
				if strings.Contains(s.WhyIncluded, "(posting_handler)") {
					handlerModules = append(handlerModules, s.Module)
				}
			}
			if len(handlerModules) == 0 {
				t.Fatalf("в ответе нет posting_handler: snippets=%+v warnings=%+v", res.Snippets, res.Warnings)
			}
			for _, m := range handlerModules {
				if !strings.EqualFold(m, pair.module) {
					t.Errorf("posting_handler взят из модуля %q, want %q — это обработчик ЧУЖОГО документа", m, pair.module)
				}
			}
			// Откат на правило подстроки — то самое, которое отдавало
			// омонима. На этой паре он не имеет права применяться: каталог
			// модулей выводится из объявления.
			for _, w := range res.Warnings {
				if w.Code == "posting_handler_owner_unknown" {
					t.Errorf("применён откат на правило подстроки: %s", w.Message)
				}
			}

			// 2. Перехватчик своего слоя в ответе есть.
			var found bool
			for _, s := range res.Signatures {
				if s.Component != pair.layer || !strings.EqualFold(s.Name, pair.interceptor) {
					continue
				}
				found = true
				if s.Kind != pair.kind {
					t.Errorf("перехватчик %s: вид перехвата %q, want %q", s.Name, s.Kind, pair.kind)
				}
				if s.Confidence != 1 {
					t.Errorf("перехватчик %s: confidence %v, want 1 (точный факт DeriveIntercepts)", s.Name, s.Confidence)
				}
			}
			if !found {
				t.Errorf("перехватчик %s слоя %s в ответ не попал: signatures=%+v warnings=%+v",
					pair.interceptor, pair.layer, res.Signatures, res.Warnings)
			}
		})
	}
}

// TestRealDumpPostingWithoutBaseHandler — документ без базового модуля
// объекта (ONEC_POSTING_NOBASE) на реальной выгрузке. Раньше
// get_context_for_task по такому документу отдавал posting_handler=missing, movements=missing, signatures пустой и
// warnings=null — то есть молчал о том, что проведение целиком написано
// расширением.
//
// Предусловие проверяется В ИНДЕКСЕ, а не предполагается: в базовом слое
// модуля объекта нет, в слое расширения он есть. Не так — премиссы теста в
// этой выгрузке не живёт, и это пропуск; любой отказ ВНУТРИ прогона красный.
func TestRealDumpPostingWithoutBaseHandler(t *testing.T) {
	noBase := postingInterceptsFromEnv(t, "ONEC_POSTING_NOBASE")
	if len(noBase) != 1 {
		t.Fatalf("ONEC_POSTING_NOBASE: записей %d, want 1", len(noBase))
	}
	noBaseDoc := noBase[0]
	svc, st, projectID := realDumpPostingProject(t, noBase)
	ctx := context.Background()

	modulePath := workspace.DumpModulePath("Document", noBaseDoc.doc, workspace.ModuleObject)
	var baseHas, extHas bool
	if err := st.Read(ctx, func(tx *store.ReadTx) error {
		var err error
		if _, baseHas, err = tx.SourceFileID("cfg", modulePath); err != nil {
			return err
		}
		_, extHas, err = tx.SourceFileID(noBaseDoc.layer, modulePath)
		return err
	}); err != nil {
		t.Fatalf("проверка предусловия в индексе: %v", err)
	}
	if baseHas || !extHas {
		t.Skipf("предусловие не выполняется (базовый модуль %s: есть=%v, модуль слоя %s: есть=%v) — в этой выгрузке пары «базового метода нет, перехватчик есть» нет",
			modulePath, baseHas, noBaseDoc.layer, extHas)
	}

	task := fmt.Sprintf("в проведении документа %s не создаются движения по регистрам", noBaseDoc.doc)
	res, err := Run(ctx, svc, st, Request{
		Task: task, ProjectID: projectID, View: "effective", Freshness: FreshnessAllowStale,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Intent.Primary != IntentPosting {
		t.Fatalf("Intent.Primary = %q, want %q (задача %q)", res.Intent.Primary, IntentPosting, task)
	}

	// 1. Ответ НЕ молчит про отсутствие базового метода.
	var missingWarns []string
	var namesInterceptor bool
	for _, w := range res.Warnings {
		if w.Code != "posting_base_handler_missing" {
			continue
		}
		missingWarns = append(missingWarns, w.Message)
		if strings.Contains(strings.ToLower(w.Message), noBaseDoc.interceptor) {
			namesInterceptor = true
		}
	}
	if len(missingWarns) == 0 {
		t.Fatalf("нет предупреждения posting_base_handler_missing — это и есть молчаливая потеря: anchors=%+v warnings=%+v",
			res.Anchors, res.Warnings)
	}
	if !namesInterceptor {
		t.Errorf("ни одно предупреждение не называет перехватчик %s: %q", noBaseDoc.interceptor, missingWarns)
	}

	// 2. Перехватчик в ответе, со своим слоем и видом перехвата.
	var found bool
	for _, s := range res.Signatures {
		if s.Component != noBaseDoc.layer || !strings.EqualFold(s.Name, noBaseDoc.interceptor) {
			continue
		}
		found = true
		if s.Kind != noBaseDoc.kind {
			t.Errorf("перехватчик %s: вид перехвата %q, want %q", s.Name, s.Kind, noBaseDoc.kind)
		}
	}
	if !found {
		t.Errorf("перехватчик %s слоя %s в ответ не попал: signatures=%+v warnings=%+v",
			noBaseDoc.interceptor, noBaseDoc.layer, res.Signatures, res.Warnings)
	}

	// 3. Его собственные движения — со своим component.
	var extMovements int
	for _, rel := range res.Relations {
		if rel.Kind == "register_access" && rel.Component == noBaseDoc.layer {
			extMovements++
		}
	}
	if extMovements == 0 {
		t.Errorf("движений перехватчика слоя %s в ответе нет: relations=%+v", noBaseDoc.layer, res.Relations)
	}

	// 4. Чужого обработчика проведения в ответе быть не должно: своего у
	//    документа нет, и подстановка омонима здесь была бы хуже пустоты.
	for _, s := range res.Snippets {
		if strings.Contains(s.WhyIncluded, "(posting_handler)") {
			t.Errorf("в ответе есть posting_handler из модуля %q, хотя у документа своего обработчика нет", s.Module)
		}
	}
}
