package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// postingTask — формулировка, классифицируемая как posting (intentLexicon:
// "проведени" + "движени") и называющая документ Заказ: общий вход тестов ниже.
const postingTask = "Почему при проведении документа Заказ не создаются движения"

// Модули документов — по раскладке выгрузки, из одного места
// (fixture_test.go: objModulePath -> workspace.DumpModulePath).
var (
	postingModuleZakaz = objModulePath("Document", "Заказ")
	postingModuleZK    = objModulePath("Document", "ЗаказКлиента")
)

// postingFixtureOpts — переключатели фикстуры. Каждый меняет РЕАЛЬНОЕ
// содержимое BSL-текста расширения, а не ожидания теста: мутация «аннотации
// нет» обязана гасить факты перехвата, «аргумента нет» — превращать их в
// диагностику intercept_target_unknown.
type postingFixtureOpts struct {
	// ext: "" — расширения нет вовсе; "instead" — &Вместо("ОбработкаПроведения");
	// "none" — метод с тем же именем, но без аннотации; "noarg" — &Вместо() без
	// аргумента (цель не выводится).
	ext string
	// secondInstead — второе расширение с &Вместо на тот же метод (конфликт).
	secondInstead bool
	// subs — подписки на событие: своя (cfg), объявленная расширением
	// (ext-subs, применяется к cfg), чужого документа (ЗаказКлиента) и чужой
	// конфигурации (cfg2, к cfg не применяется). Последние две в ответ по
	// Заказу попадать не должны.
	subs bool
}

// seedPostingSubscriptions — подписки на событие вокруг документа Заказ.
// source_name_norm хранит нормализованный СЫРОЙ текст источника платформы
// («documentobject.заказ»), см. store.EventSubscriptionsBySourceNames.
func seedPostingSubscriptions(t *testing.T, tx *store.WriteTx) error {
	t.Helper()
	add := func(componentID, name, source string) error {
		f := fileHelper(t, tx, componentID, commonModulePath("Подписки_"+name), "// подписка")
		return tx.InsertEventSubscription(store.EventSubscription{
			ComponentID: componentID, NameNorm: domain.NormalizeName(name), NameDisplay: name,
			SourceKind: "Object", SourceNameNorm: domain.NormalizeName(source), Event: "ОбработкаПроведения",
			HandlerNameNorm: domain.NormalizeName(name + "Обработчик"), OriginFileID: f,
			Resolution: "resolved", Layer: componentID,
		})
	}
	if err := add("cfg", "ПриПроведенииЗаказа", "DocumentObject.Заказ"); err != nil {
		return err
	}
	if err := add("cfg", "ПриПроведенииЗаказаКлиента", "DocumentObject.ЗаказКлиента"); err != nil {
		return err
	}
	if err := tx.UpsertComponent(store.Component{ID: "ext-subs", Kind: "extension", Root: "ext-subs", AppliesTo: "cfg", ApplyOrder: 3}); err != nil {
		return err
	}
	if err := add("ext-subs", "РасшА_ПриПроведенииЗаказа", "DocumentObject.Заказ"); err != nil {
		return err
	}
	if err := tx.UpsertComponent(store.Component{ID: "cfg2", Kind: "configuration", Root: "cfg2"}); err != nil {
		return err
	}
	return add("cfg2", "ЧужаяПриПроведенииЗаказа", "DocumentObject.Заказ")
}

// seedPostingFixture строит документ Заказ с обработчиком проведения в
// СОБСТВЕННОМ модуле объекта и документ-омоним ЗаказКлиента с таким же
// обработчиком (вставлен ПЕРВЫМ — на нём ловится поиск обработчика по
// подстроке имени модуля). У базового обработчика Заказа — движение по
// ТоварыНаСкладах, у перехватчика расширения — своё движение по
// ДвиженияДсСотрудников: разные факты, и в ответе они обязаны нести разные
// component.
func seedPostingFixture(t *testing.T, st *store.Store, opts postingFixtureOpts) {
	t.Helper()
	ctx := context.Background()

	baseBody := "Процедура ОбработкаПроведения(Отказ, РежимПроведения)\n\tДвижения.ТоварыНаСкладах.Записать();\nКонецПроцедуры"
	var extBody string
	switch opts.ext {
	case "instead":
		extBody = "&Вместо(\"ОбработкаПроведения\")\nПроцедура РасшА_ОбработкаПроведения(Отказ, РежимПроведения)\n\tПродолжитьВызов(Отказ, РежимПроведения);\n\tДвижения.ДвиженияДсСотрудников.Записать();\nКонецПроцедуры"
	case "none":
		extBody = "// метод с тем же именем, но БЕЗ аннотации перехвата\nПроцедура РасшА_ОбработкаПроведения(Отказ, РежимПроведения)\n\tДвижения.ДвиженияДсСотрудников.Записать();\nКонецПроцедуры"
	case "noarg":
		extBody = "&Вместо()\nПроцедура РасшА_ОбработкаПроведения(Отказ, РежимПроведения)\n\tДвижения.ДвиженияДсСотрудников.Записать();\nКонецПроцедуры"
	}

	err := st.Write(ctx, func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}

		// --- регистры, на которые ссылаются движения ---
		fRegTov := fileHelper(t, tx, "cfg", declPath("AccumulationRegister", "ТоварыНаСкладах"), "<meta/>")
		oRegTov, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00AccumulationRegister\x00товарынаскладах", ComponentID: "cfg",
			MType: "AccumulationRegister", NameNorm: "товарынаскладах", NameDisplay: "ТоварыНаСкладах",
			FileID: fRegTov, Layer: "base",
		})
		if err != nil {
			return err
		}
		fRegDs := fileHelper(t, tx, "cfg", declPath("AccumulationRegister", "ДвиженияДсСотрудников"), "<meta/>")
		oRegDs, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00AccumulationRegister\x00движениядссотрудников", ComponentID: "cfg",
			MType: "AccumulationRegister", NameNorm: "движениядссотрудников", NameDisplay: "ДвиженияДсСотрудников",
			FileID: fRegDs, Layer: "base",
		})
		if err != nil {
			return err
		}

		// --- документ-омоним ЗаказКлиента (вставлен первым) ---
		fZKMeta := fileHelper(t, tx, "cfg", declPath("Document", "ЗаказКлиента"), "<meta/>")
		if _, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00заказклиента", ComponentID: "cfg",
			MType: "Document", NameNorm: "заказклиента", NameDisplay: "ЗаказКлиента", FileID: fZKMeta, Layer: "base",
		}); err != nil {
			return err
		}
		fZKMod := fileHelper(t, tx, "cfg", postingModuleZK, baseBody)
		mZKMod := moduleHelper(t, tx, "cfg", postingModuleZK, "заказклиента", "ЗаказКлиента", fZKMod)
		symbolHelper(t, tx, symbolSpec{
			uid: "sym-obrabotka-provedeniya-zk", componentID: "cfg", nameNorm: "обработкапроведения",
			nameDisplay: "ОбработкаПроведения", kind: "procedure", moduleID: mZKMod, fileID: fZKMod,
			span: spanOf(0, len(baseBody)),
		})
		// Второй омоним — ЗаполнитьДвижения в обоих документах: неоднозначность
		// по имени, НЕ связанная с обработчиком проведения (нужна тестам П4/R32,
		// чтобы отличить «убрали шум по ОбработкаПроведения» от «выключили
		// ambiguities для posting целиком»).
		symbolHelper(t, tx, symbolSpec{
			uid: "sym-zapolnit-dvizheniya-zk", componentID: "cfg", nameNorm: "заполнитьдвижения",
			nameDisplay: "ЗаполнитьДвижения", kind: "procedure", moduleID: mZKMod, fileID: fZKMod,
			span: spanOf(0, len(baseBody)),
		})

		// --- документ Заказ ---
		fZakazMeta := fileHelper(t, tx, "cfg", declPath("Document", "Заказ"), "<meta/>")
		if _, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00заказ", ComponentID: "cfg",
			MType: "Document", NameNorm: "заказ", NameDisplay: "Заказ", FileID: fZakazMeta, Layer: "base",
		}); err != nil {
			return err
		}
		fZakazMod := fileHelper(t, tx, "cfg", postingModuleZakaz, baseBody)
		mZakazMod := moduleHelper(t, tx, "cfg", postingModuleZakaz, "заказ", "Заказ", fZakazMod)
		sBase := symbolHelper(t, tx, symbolSpec{
			uid: "sym-obrabotka-provedeniya-zakaz", componentID: "cfg", nameNorm: "обработкапроведения",
			nameDisplay: "ОбработкаПроведения", kind: "procedure", moduleID: mZakazMod, fileID: fZakazMod,
			span: spanOf(0, len(baseBody)),
		})
		symbolHelper(t, tx, symbolSpec{
			uid: "sym-zapolnit-dvizheniya-zakaz", componentID: "cfg", nameNorm: "заполнитьдвижения",
			nameDisplay: "ЗаполнитьДвижения", kind: "procedure", moduleID: mZakazMod, fileID: fZakazMod,
			span: spanOf(0, len(baseBody)),
		})
		if err := tx.InsertRegisterAccess(store.RegisterAccess{
			FileID: fZakazMod, SymbolID: sBase, ObjectID: oRegTov, RegisterNameNorm: "товарынаскладах",
			Mode: "movement", Static: true, Confidence: 1, Span: sp(), Layer: "base",
		}); err != nil {
			return err
		}

		// --- расширения, заимствующие модуль объекта Заказ ---
		seedExt := func(id string, order int, methodDisplay, body string) error {
			if err := tx.UpsertComponent(store.Component{ID: id, Kind: "extension", Root: id, AppliesTo: "cfg", ApplyOrder: order}); err != nil {
				return err
			}
			fExt := fileHelper(t, tx, id, postingModuleZakaz, body)
			mExt := moduleHelper(t, tx, id, postingModuleZakaz, "заказ", "Заказ", fExt)
			sExt := symbolHelper(t, tx, symbolSpec{
				uid: "sym-obrabotka-ext-" + id, componentID: id, nameNorm: domain.NormalizeName(methodDisplay),
				nameDisplay: methodDisplay, kind: "procedure", moduleID: mExt, fileID: fExt,
				span: spanOf(0, len(body)),
			})
			return tx.InsertRegisterAccess(store.RegisterAccess{
				FileID: fExt, SymbolID: sExt, ObjectID: oRegDs, RegisterNameNorm: "движениядссотрудников",
				Mode: "movement", Static: true, Confidence: 1, Span: sp(), Layer: id,
			})
		}
		if extBody != "" {
			if err := seedExt("ext-a", 1, "РасшА_ОбработкаПроведения", extBody); err != nil {
				return err
			}
		}
		if opts.subs {
			if err := seedPostingSubscriptions(t, tx); err != nil {
				return err
			}
		}
		if opts.secondInstead {
			if err := seedExt("ext-b", 2, "РасшБ_ОбработкаПроведения",
				"&Вместо(\"ОбработкаПроведения\")\nПроцедура РасшБ_ОбработкаПроведения(Отказ, РежимПроведения)\n\tПродолжитьВызов(Отказ, РежимПроведения);\nКонецПроцедуры"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seedPostingFixture: %v", err)
	}
}

// TestPostingHandlerFoundByOwningModule — критерий приёмки П2.2 (R19):
// обработчик проведения ищется по факту принадлежности СВОЕМУ модулю
// объекта, а не по вхождению имени объекта в путь модуля. У документа Заказ
// есть омоним ЗаказКлиента с таким же обработчиком, и его путь
// "Documents/ЗаказКлиента/..." содержит подстроку "заказ" — прежнее правило
// отдавало обработчик чужого документа. Проверяется в обоих режимах: П2.2
// чинит поиск и при view=raw, и при view=effective.
func TestPostingHandlerFoundByOwningModule(t *testing.T) {
	for _, view := range []string{"", "effective"} {
		name := "raw"
		if view != "" {
			name = view
		}
		t.Run(name, func(t *testing.T) {
			st := openFixtureStore(t)
			seedPostingFixture(t, st, postingFixtureOpts{})

			res := buildFor(t, st, Request{Task: postingTask, ProjectID: "p", View: view})
			if res.Intent.Primary != IntentPosting {
				t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentPosting)
			}
			var found bool
			for _, s := range res.Snippets {
				if !strings.Contains(s.WhyIncluded, "(posting_handler)") {
					continue
				}
				found = true
				if s.Module != postingModuleZakaz {
					t.Errorf("posting_handler ModulePath = %q, want %q (обработчик самого документа Заказ)", s.Module, postingModuleZakaz)
				}
			}
			if !found {
				t.Fatalf("в ответе нет snippet категории posting_handler: %+v", res.Snippets)
			}
		})
	}
}

// TestPostingHandlerInterceptsEffectiveOnly — критерии приёмки П2.3 (R20) и
// R25: при view=effective перехватчик расширения попадает в ответ отдельной
// категорией posting_handler_intercepts — с ФАКТИЧЕСКИМ телом и своим слоем;
// при view=raw ответ прежний, фактов перехвата в нём нет. Мутация ext="none"
// (метод с тем же именем, но без аннотации) обязана гасить факт: это проверка
// того, что читается аннотация, а не совпадение имени.
func TestPostingHandlerInterceptsEffectiveOnly(t *testing.T) {
	t.Run("аннотация есть", func(t *testing.T) {
		st := openFixtureStore(t)
		seedPostingFixture(t, st, postingFixtureOpts{ext: "instead"})

		raw := buildFor(t, st, Request{Task: postingTask, ProjectID: "p"})
		for _, s := range raw.Signatures {
			if s.Kind == "Вместо" {
				t.Fatalf("view=raw не должен нести фактов перехвата: %+v", raw.Signatures)
			}
		}
		assertRawPostingBaseline(t, raw)

		eff := buildFor(t, st, Request{Task: postingTask, ProjectID: "p", View: "effective"})
		var found bool
		for _, s := range eff.Signatures {
			if s.Kind != "Вместо" {
				continue
			}
			found = true
			if s.Component != "ext-a" {
				t.Errorf("Component = %q, want ext-a (слой перехватчика)", s.Component)
			}
			if !strings.Contains(s.Text, "ПродолжитьВызов") {
				t.Errorf("Text = %q, want фактический текст перехватчика", s.Text)
			}
			if s.Confidence != 1 {
				t.Errorf("Confidence = %v, want 1 (точный факт DeriveIntercepts)", s.Confidence)
			}
		}
		if !found {
			t.Fatalf("effective Signatures = %+v, want запись Kind=Вместо (posting_handler_intercepts)", eff.Signatures)
		}
	})

	t.Run("аннотации нет", func(t *testing.T) {
		st := openFixtureStore(t)
		seedPostingFixture(t, st, postingFixtureOpts{ext: "none"})

		eff := buildFor(t, st, Request{Task: postingTask, ProjectID: "p", View: "effective"})
		for _, s := range eff.Signatures {
			if s.Kind == "Вместо" {
				t.Fatalf("одноимённый метод без аннотации не перехватчик: %+v", eff.Signatures)
			}
		}
	})
}

// TestPostingInterceptorRegisterAccesses — критерий приёмки П2.4 (R21):
// движения САМОГО перехватчика попадают в те же movements/register_access,
// что и движения базового обработчика, но с component своего слоя. В raw
// движение расширения не появляется вовсе (R25).
func TestPostingInterceptorRegisterAccesses(t *testing.T) {
	st := openFixtureStore(t)
	seedPostingFixture(t, st, postingFixtureOpts{ext: "instead"})

	raw := buildFor(t, st, Request{Task: postingTask, ProjectID: "p"})
	for _, r := range raw.Relations {
		if strings.Contains(r.To, "движениядссотрудников") {
			t.Fatalf("view=raw не должен нести движений расширения: %+v", raw.Relations)
		}
	}
	assertRawPostingBaseline(t, raw)

	eff := buildFor(t, st, Request{Task: postingTask, ProjectID: "p", View: "effective"})
	var base, ext *Relation
	for i := range eff.Relations {
		r := &eff.Relations[i]
		switch {
		case strings.Contains(r.To, "товарынаскладах"):
			base = r
		case strings.Contains(r.To, "движениядссотрудников"):
			ext = r
		}
	}
	if base == nil {
		t.Fatalf("нет движения базового обработчика (товарынаскладах): %+v", eff.Relations)
	}
	if base.Component != "cfg" {
		t.Errorf("движение базового обработчика Component = %q, want cfg", base.Component)
	}
	if ext == nil {
		t.Fatalf("нет движения перехватчика (движениядссотрудников): %+v", eff.Relations)
	}
	if ext.Component != "ext-a" {
		t.Errorf("движение перехватчика Component = %q, want ext-a (слой самого перехватчика)", ext.Component)
	}
	// display-имя, не нормализованное: у базового обработчика в этой же
	// категории стоит NameDisplay, и два регистра написания в одной категории
	// ответа — тихая потеря правды (таск 09 п.3).
	if ext.From != "РасшА_ОбработкаПроведения" {
		t.Errorf("движение перехватчика From = %q, want РасшА_ОбработкаПроведения", ext.From)
	}
}

// TestEffectivePartialCoverageWarningScope — критерий приёмки П2.1 (R22,
// R23) и issue #4 (ADR-035): intent, чей builder консультируется с наложением
// слоёв, предупреждения effective_view_partial_coverage не несёт; intent, ещё
// построенный как raw, обязан его нести, иначе предел покрытия перестанет
// называться честно. Таблица держит обе стороны: сдвиг intent из одной
// группы в другую виден здесь явно.
func TestEffectivePartialCoverageWarningScope(t *testing.T) {
	cases := []struct {
		intent      string
		task        string
		wantWarning bool
	}{
		{IntentPosting, postingTask, false},
		{IntentRegister, "Кто пишет в регистр ТоварыНаСкладах", false},
		{IntentQuery, "Перепиши текст запроса в отчёте по остаткам", false},
		{IntentRights, "Пользователь не видит документ, нужен разбор прав и RLS", true},
		{IntentAddAttribute, "Добавь реквизит Комментарий в документ ЗаказКлиента", false},
	}
	for _, tc := range cases {
		t.Run(tc.intent, func(t *testing.T) {
			st := openFixtureStore(t)
			seedPostingFixture(t, st, postingFixtureOpts{ext: "instead"})
			eff := buildFor(t, st, Request{Task: tc.task, ProjectID: "p", View: "effective"})
			if eff.Intent.Primary != tc.intent {
				t.Fatalf("Intent.Primary = %q, want %q (формулировка не классифицируется как ожидалось)", eff.Intent.Primary, tc.intent)
			}
			if got := hasWarning(eff, "effective_view_partial_coverage"); got != tc.wantWarning {
				t.Fatalf("effective_view_partial_coverage для intent %q = %v, want %v: %+v", tc.intent, got, tc.wantWarning, eff.Warnings)
			}
		})
	}
}

// TestPostingTwoInsteadConflict — критерий приёмки R24: два расширения
// перехватывают ОбработкаПроведения через &Вместо. Молчаливого выбора одного
// слоя быть не должно: в ответе оба перехватчика и предупреждение
// instead_conflict с обоими слоями и confidence < 1 (ADR-4).
func TestPostingTwoInsteadConflict(t *testing.T) {
	st := openFixtureStore(t)
	seedPostingFixture(t, st, postingFixtureOpts{ext: "instead", secondInstead: true})

	eff := buildFor(t, st, Request{Task: postingTask, ProjectID: "p", View: "effective"})
	var conflict *Warning
	for i := range eff.Warnings {
		if eff.Warnings[i].Code == "instead_conflict" {
			conflict = &eff.Warnings[i]
		}
	}
	if conflict == nil {
		t.Fatalf("Warnings = %+v, want instead_conflict", eff.Warnings)
	}
	for _, layer := range []string{"ext-a", "ext-b"} {
		if !strings.Contains(conflict.Message, layer) {
			t.Errorf("Message = %q, want упоминание слоя %s", conflict.Message, layer)
		}
	}
	if !strings.Contains(conflict.Message, "confidence=0.5") {
		t.Errorf("Message = %q, want confidence<1 в тексте", conflict.Message)
	}
	layers := map[string]bool{}
	for _, s := range eff.Signatures {
		if s.Kind == "Вместо" {
			layers[s.Component] = true
		}
	}
	if len(layers) != 2 {
		t.Fatalf("перехватчиков в ответе = %v, want оба слоя (конфликт не выбирает один)", layers)
	}
}

// TestPostingInterceptTargetUnknownReachesWarnings — диагностика
// resolve.DiagInterceptTargetUnknown (&Вместо без аргумента: цель не
// выводится, факт перехвата НЕ строится) обязана доехать до предупреждений
// ответа get_context_for_task. Молчаливая потеря диагностики неотличима от
// «перехватчиков нет» — ровно тот дефект, против которого правило «отказ
// вместо догадки» (ADR-027).
func TestPostingInterceptTargetUnknownReachesWarnings(t *testing.T) {
	st := openFixtureStore(t)
	seedPostingFixture(t, st, postingFixtureOpts{ext: "noarg"})

	eff := buildFor(t, st, Request{Task: postingTask, ProjectID: "p", View: "effective"})
	var diag *Warning
	for i := range eff.Warnings {
		if eff.Warnings[i].Code == "intercept_target_unknown" {
			diag = &eff.Warnings[i]
		}
	}
	if diag == nil {
		t.Fatalf("Warnings = %+v, want intercept_target_unknown", eff.Warnings)
	}
	if !strings.Contains(diag.Message, "РасшА_ОбработкаПроведения") {
		t.Errorf("Message = %q, want имя метода-перехватчика", diag.Message)
	}
	for _, s := range eff.Signatures {
		if s.Kind == "Вместо" {
			t.Fatalf("факт перехвата при неразобранной цели строиться не должен: %+v", eff.Signatures)
		}
	}
}

// TestObjectModuleDir — регрессия приёмки на реальной выгрузке: каталог модулей
// объекта выводится из ПУТИ ОБЪЯВЛЕНИЯ БЕЗ РАСШИРЕНИЯ, а не из каталога файла
// объявления. Раскладка DumpConfigToFiles — "Documents/Штрафы.xml", и
// каталогом файла оказывается "Documents": префикс, общий любому документу
// конфигурации, по которому анкер Штрафы отдавал обработчик чужого
// документа. Вырожденный каталог не должен получаться ни при какой раскладке:
// пустой результат честнее, вызывающий откатывается на прежнее правило.
func TestObjectModuleDir(t *testing.T) {
	cases := []struct {
		name    string
		relPath string
		objNorm string
		want    string
	}{
		{"раскладка DumpConfigToFiles", "Documents/Штрафы.xml", "штрафы", "documents/штрафы"},
		{"вложенная раскладка", "Documents/Заказ/Заказ.xml", "заказ", "documents/заказ"},
		{"регистр", declPath("AccumulationRegister", "ДвиженияДсСотрудников"), "движениядссотрудников", "accumulationregisters/движениядссотрудников"},
		{"имя файла не совпало с именем объекта", "Documents/Прочее.xml", "штрафы", ""},
		{"пустой путь", "", "штрафы", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := objectModuleDir(tc.relPath, tc.objNorm)
			if got != tc.want {
				t.Fatalf("objectModuleDir(%q, %q) = %q, want %q", tc.relPath, tc.objNorm, got, tc.want)
			}
			if got == "documents" || got == "accumulationregisters" {
				t.Fatalf("вырожденный каталог %q подходит любому объекту вида", got)
			}
		})
	}
}

// assertRawPostingBaseline — то, что raw ОБЯЗАН отдать по документу Заказ:
// обработчик проведения его собственного модуля и движение базового слоя.
//
// Ставится рядом с каждой проверкой «в raw нет фактов расширения»: та
// проверка доказывает ОТСУТСТВИЕ и проходит на пустом ответе целиком —
// регрессия, обнуляющая raw, прошла бы зелёной. Ожидаемые значения взяты из
// seedPostingFixture (движение базового обработчика — ТоварыНаСкладах,
// component cfg), а не из вывода Build.
func assertRawPostingBaseline(t *testing.T, raw Result) {
	t.Helper()
	var handlerModule string
	for _, s := range raw.Snippets {
		if strings.Contains(s.WhyIncluded, "(posting_handler)") {
			handlerModule = s.Module
		}
	}
	if handlerModule != postingModuleZakaz {
		t.Fatalf("raw posting_handler Module = %q, want %q — raw пуст, и «нет фактов расширения» ничего не доказывает: %+v",
			handlerModule, postingModuleZakaz, raw.Snippets)
	}
	var base *Relation
	for i := range raw.Relations {
		r := &raw.Relations[i]
		if r.Kind == "register_access" && strings.Contains(r.To, "товарынаскладах") {
			base = r
		}
	}
	if base == nil {
		t.Fatalf("raw не несёт движения базового обработчика (товарынаскладах): %+v", raw.Relations)
	}
	if base.Component != "cfg" {
		t.Errorf("движение базового обработчика Component = %q, want cfg", base.Component)
	}
}
