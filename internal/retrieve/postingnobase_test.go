package retrieve

import (
	"context"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Документ БЕЗ базового модуля объекта — не выдуманный погранслучай, а
// реальная конфигурация: у документа в выгрузке с расширениями каталога
// Documents/<Имя>/Ext/ не существует вовсе (так и в самой базе), а расширение «РасширениеА» заимствует этот же путь и пишет
// движения из &После("ОбработкаПроведения"). Раньше ответ был пуст и
// молчал: перехватчик — единственный исполняемый код проведения, и его не
// было видно.
const postingNoBaseTask = "Почему при проведении документа ВходящиеПлатежи не создаются движения"

var postingNoBaseModule = objModulePath("Document", "ВходящиеПлатежи")

// postingNoBaseExtBody — тело модуля расширения. Аннотация и имя метода
// РАЗНЫЕ (ADR-027): цель — из аргумента, перехватчик — из имени.
const postingNoBaseExtBody = "&После(\"ОбработкаПроведения\")\n" +
	"Процедура РасшА_ОбработкаПроведения(Отказ, РежимПроведения)\n" +
	"\tДвижения.ОбщийДенежныеСредстваСотрудников.Записать();\n" +
	"КонецПроцедуры"

// seedPostingNoBaseFixture — документ ВходящиеПлатежи, у которого в базовом
// слое есть ТОЛЬКО объявление: ни файла модуля объекта, ни символа
// ОбработкаПроведения. Рядом — документ-омоним с настоящим обработчиком:
// если поиск когда-нибудь снова съедет на подстроку имени, он подставит
// чужой обработчик и тест это увидит.
func seedPostingNoBaseFixture(t *testing.T, st *store.Store) {
	t.Helper()
	err := st.Write(context.Background(), func(tx *store.WriteTx) error {
		if err := tx.UpsertComponent(store.Component{ID: "cfg", Kind: "configuration", Root: "cfg"}); err != nil {
			return err
		}
		fReg := fileHelper(t, tx, "cfg", declPath("AccumulationRegister", "ОбщийДенежныеСредстваСотрудников"), "<meta/>")
		oReg, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00AccumulationRegister\x00общийденежныесредствасотрудников", ComponentID: "cfg",
			MType: "AccumulationRegister", NameNorm: "общийденежныесредствасотрудников",
			NameDisplay: "ОбщийДенежныеСредстваСотрудников", FileID: fReg, Layer: "base",
		})
		if err != nil {
			return err
		}

		// Омоним: "ВходящиеПлатежиРеестр" содержит подстроку "входящиеплатежи".
		omoBody := "Процедура ОбработкаПроведения(Отказ, РежимПроведения)\n\tДвижения.ОбщийДенежныеСредстваСотрудников.Записать();\nКонецПроцедуры"
		fOmoMeta := fileHelper(t, tx, "cfg", declPath("Document", "ВходящиеПлатежиРеестр"), "<meta/>")
		if _, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00входящиеплатежиреестр", ComponentID: "cfg",
			MType: "Document", NameNorm: "входящиеплатежиреестр", NameDisplay: "ВходящиеПлатежиРеестр",
			FileID: fOmoMeta, Layer: "base",
		}); err != nil {
			return err
		}
		omoModule := objModulePath("Document", "ВходящиеПлатежиРеестр")
		fOmoMod := fileHelper(t, tx, "cfg", omoModule, omoBody)
		mOmoMod := moduleHelper(t, tx, "cfg", omoModule, "входящиеплатежиреестр", "ВходящиеПлатежиРеестр", fOmoMod)
		symbolHelper(t, tx, symbolSpec{
			uid: "sym-op-reestr", componentID: "cfg", nameNorm: "обработкапроведения",
			nameDisplay: "ОбработкаПроведения", kind: "procedure", moduleID: mOmoMod, fileID: fOmoMod,
			span: spanOf(0, len(omoBody)),
		})

		// Сам ВходящиеПлатежи: ТОЛЬКО объявление, модуля объекта нет.
		fMeta := fileHelper(t, tx, "cfg", declPath("Document", "ВходящиеПлатежи"), "<meta/>")
		if _, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "cfg\x00object\x00Document\x00входящиеплатежи", ComponentID: "cfg",
			MType: "Document", NameNorm: "входящиеплатежи", NameDisplay: "ВходящиеПлатежи",
			FileID: fMeta, Layer: "base",
		}); err != nil {
			return err
		}

		// Расширение заимствует НЕСУЩЕСТВУЮЩИЙ в базе модуль объекта.
		if err := tx.UpsertComponent(store.Component{
			ID: "ext-a", Kind: "extension", Root: "ext-a", AppliesTo: "cfg", ApplyOrder: 1,
		}); err != nil {
			return err
		}
		// Расширение публикует и ЗАИМСТВОВАННОЕ объявление документа — так же,
		// как реальная выгрузка: у Documents/ВходящиеПлатежи.xml есть строка и в
		// cfg, и в ext-a. Из-за неё анкеров по имени два, и путь «базового
		// метода нет» рискует высказаться дважды.
		fExtMeta := fileHelper(t, tx, "ext-a", declPath("Document", "ВходящиеПлатежи"), "<meta/>")
		if _, err := tx.EnsureMetadataObject(store.MetadataObject{
			IdentityKey: "ext-a\x00object\x00Document\x00входящиеплатежи", ComponentID: "ext-a",
			MType: "Document", NameNorm: "входящиеплатежи", NameDisplay: "ВходящиеПлатежи",
			FileID: fExtMeta, Layer: "ext-a",
		}); err != nil {
			return err
		}
		fExt := fileHelper(t, tx, "ext-a", postingNoBaseModule, postingNoBaseExtBody)
		mExt := moduleHelper(t, tx, "ext-a", postingNoBaseModule, "входящиеплатежи", "ВходящиеПлатежи", fExt)
		sExt := symbolHelper(t, tx, symbolSpec{
			uid: "sym-op-ext-a", componentID: "ext-a",
			nameNorm: domain.NormalizeName("РасшА_ОбработкаПроведения"), nameDisplay: "РасшА_ОбработкаПроведения",
			kind: "procedure", moduleID: mExt, fileID: fExt, span: spanOf(0, len(postingNoBaseExtBody)),
		})
		return tx.InsertRegisterAccess(store.RegisterAccess{
			FileID: fExt, SymbolID: sExt, ObjectID: oReg, RegisterNameNorm: "общийденежныесредствасотрудников",
			Mode: "movement", Static: true, Confidence: 1, Span: sp(), Layer: "ext-a",
		})
	})
	if err != nil {
		t.Fatalf("seedPostingNoBaseFixture: %v", err)
	}
}

func warningByCode(r Result, code string) (Warning, bool) {
	for _, w := range r.Warnings {
		if w.Code == code {
			return w, true
		}
	}
	return Warning{}, false
}

// TestPostingWithoutBaseHandlerShowsInterceptors:
// базового обработчика нет, перехватчик расширения есть и он единственный
// исполняемый код проведения. Он обязан попасть в ответ со своим слоем, его
// собственные движения — в movements с component расширения, а отсутствие
// базового метода — быть НАЗВАНО предупреждением, а не оставлено пустотой.
func TestPostingWithoutBaseHandlerShowsInterceptors(t *testing.T) {
	st := openFixtureStore(t)
	seedPostingNoBaseFixture(t, st)

	res := buildFor(t, st, Request{Task: postingNoBaseTask, ProjectID: "p", View: "effective"})
	if res.Intent.Primary != IntentPosting {
		t.Fatalf("Intent.Primary = %q, want %q", res.Intent.Primary, IntentPosting)
	}

	// Пункт 3: ответ прямо говорит, что базового метода нет.
	w, ok := warningByCode(res, "posting_base_handler_missing")
	if !ok {
		t.Fatalf("нет предупреждения posting_base_handler_missing: warnings=%+v", res.Warnings)
	}
	if !strings.Contains(w.Message, "ВходящиеПлатежи") {
		t.Errorf("предупреждение не называет объект: %q", w.Message)
	}
	if !strings.Contains(strings.ToLower(w.Message), "расша_обработкапроведения") {
		t.Errorf("предупреждение не называет перехватчик, который и есть исполняемый код: %q", w.Message)
	}

	// Пункт 1: перехватчик в ответе, со своим слоем и видом перехвата.
	var sigFound bool
	for _, s := range res.Signatures {
		if !strings.EqualFold(s.Name, "расша_обработкапроведения") {
			continue
		}
		sigFound = true
		if s.Component != "ext-a" {
			t.Errorf("перехватчик: component %q, want ext-a", s.Component)
		}
		if s.Kind != "После" {
			t.Errorf("перехватчик: вид перехвата %q, want После", s.Kind)
		}
		if !strings.Contains(s.Text, "ОбщийДенежныеСредстваСотрудников") {
			t.Errorf("перехватчик отдан без тела: %q", s.Text)
		}
	}
	if !sigFound {
		t.Fatalf("перехватчика нет в signatures: %+v", res.Signatures)
	}

	// Пункт 2: движения перехватчика — с его собственным component/layer.
	var movements int
	for _, rel := range res.Relations {
		if rel.Kind != "register_access" || !strings.Contains(rel.To, "общийденежныесредствасотрудников") {
			continue
		}
		movements++
		if rel.Component != "ext-a" {
			t.Errorf("движение перехватчика: component %q, want ext-a", rel.Component)
		}
		if !strings.EqualFold(rel.From, "РасшА_ОбработкаПроведения") {
			t.Errorf("движение перехватчика: from %q, want РасшА_ОбработкаПроведения", rel.From)
		}
	}
	if movements != 1 {
		t.Errorf("движений перехватчика в ответе %d, want 1: relations=%+v", movements, res.Relations)
	}

	// Пункт 4: movements/register_access собраны и непусты; posting_handler
	// собирать было НЕ ОТ ЧЕГО — он остаётся missing, а не complete_empty.
	cov, ok := coverageOf(res, "posting_handler")
	if !ok {
		t.Fatalf("в покрытии нет posting_handler: %+v", res.RequiredCoverage)
	}
	if cov.Status != Missing {
		t.Errorf("posting_handler: статус %q, want %q — базового обработчика нет, собирать было не от чего",
			cov.Status, Missing)
	}
	mov, _ := coverageOf(res, "movements")
	if mov.ReturnedCount != 1 {
		t.Errorf("movements: returnedCount %d, want 1 (%+v)", mov.ReturnedCount, mov)
	}
}

// TestPostingWithoutBaseHandlerRawSpeaksButStaysBaseOnly: решение
// по view=raw (ADR-034). Правило «raw поведения не меняет»
// сохранено в части ФАКТОВ: перехватчик расширения в raw не появляется.
// Но пустота raw про документ, у которого проведение целиком написано
// расширением, — не «сырой вид», а ложный ответ, поэтому предупреждение
// уходит в оба вида и называет перехватчик поимённо.
func TestPostingWithoutBaseHandlerRawSpeaksButStaysBaseOnly(t *testing.T) {
	st := openFixtureStore(t)
	seedPostingNoBaseFixture(t, st)

	res := buildFor(t, st, Request{Task: postingNoBaseTask, ProjectID: "p"})

	w, ok := warningByCode(res, "posting_base_handler_missing")
	if !ok {
		t.Fatalf("raw промолчал про отсутствующий базовый обработчик: warnings=%+v", res.Warnings)
	}
	if !strings.Contains(strings.ToLower(w.Message), "расша_обработкапроведения") {
		t.Errorf("raw не назвал перехватчик, который и есть исполняемый код: %q", w.Message)
	}

	for _, s := range res.Signatures {
		if strings.EqualFold(s.Name, "расша_обработкапроведения") {
			t.Errorf("raw отдал ФАКТ перехватчика расширения (raw не несёт фактов расширений): %+v", s)
		}
	}
	for _, rel := range res.Relations {
		if rel.Component == "ext-a" {
			t.Errorf("raw отдал движение слоя расширения (raw не несёт фактов расширений): %+v", rel)
		}
	}

	// Ни одна категория, которую собирать было не от чего, не притворяется
	// собранной: complete_empty в raw здесь не имеет права появиться.
	for _, cat := range []string{"posting_handler", "movements", "register_access"} {
		cov, found := coverageOf(res, cat)
		if !found {
			t.Fatalf("в покрытии нет %s: %+v", cat, res.RequiredCoverage)
		}
		if cov.Status != Missing {
			t.Errorf("%s: статус %q, want %q — собирать было не от чего", cat, cov.Status, Missing)
		}
	}
}

// TestPostingWithoutBaseHandlerSpeaksOnce — находка живого вызова на выгрузке
// с расширениями: у документа, заимствованного расширением, строка metadata_object есть
// в ОБОИХ компонентах, поэтому анкеров по имени два. Путь «базового метода
// нет» высказывался по каждому и выдавал ДВА взаимоисключающих
// предупреждения: «проведение целиком описано перехватчиками расширений» и
// «ни одно применяющееся расширение не перехватывает ОбработкаПроведения».
//
// Ответ, говорящий об одном документе две противоположности, хуже молчания:
// агент верит второму. Вопрос «перехватывает ли расширение недостающий
// базовый метод» осмыслен ровно для объекта БАЗОВОЙ конфигурации.
func TestPostingWithoutBaseHandlerSpeaksOnce(t *testing.T) {
	st := openFixtureStore(t)
	seedPostingNoBaseFixture(t, st)

	res := buildFor(t, st, Request{Task: postingNoBaseTask, ProjectID: "p", View: "effective"})

	var msgs []string
	for _, w := range res.Warnings {
		if w.Code == "posting_base_handler_missing" {
			msgs = append(msgs, w.Message)
		}
	}
	if len(msgs) != 1 {
		t.Fatalf("предупреждений posting_base_handler_missing %d, want 1: %q", len(msgs), msgs)
	}
	if !strings.Contains(strings.ToLower(msgs[0]), "расша_обработкапроведения") {
		t.Errorf("уцелевшее предупреждение не называет перехватчик: %q", msgs[0])
	}
}
