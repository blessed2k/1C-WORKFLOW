package retrieve

import (
	"regexp"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// identifierPattern извлекает из свободного текста задачи кандидатов на имя:
// голое слово ИЛИ Квалификатор.Имя (та же форма, что реальный вызов вырезал
// бы из BSL — см. cmd/mcp1c-соседний приём callSiteTextPattern в
// internal/app/graph_realworld_test.go, здесь применяется к тексту ЗАДАЧИ,
// не к коду).
var identifierPattern = regexp.MustCompile(`[\p{L}_][\p{L}\p{N}_]*(\.[\p{L}_][\p{L}\p{N}_]*)?`)

// maxAnchorCandidates — потолок кандидатов-имён, разбираемых из текста
// задачи: без него длинная формулировка задачи означала бы десятки походов в
// store на явно бесполезные общеупотребимые слова.
const maxAnchorCandidates = 20

// maxAnchors — потолок возвращаемых anchors: agent получает несколько точек
// входа expansion, не десятки.
const maxAnchors = 6

// maxAmbiguityOptions — потолок Options ВНУТРИ одной Ambiguity, тот же приём
// и тот же порядок величины, что maxAnchors: находка доводки P6 — частое имя
// (до фикса стоп-словами, например «Проверить») резолвилось в 21 омоним, и
// opts писался в ambiguities БЕЗ потолка, раздувая ответ (74312/40405 байт
// при budgetChars=16000 по умолчанию). Truncated+TotalOptions на самой
// Ambiguity — честная отметка обрезки, тот же паттерн, что
// truncated/maxCallGraphWalkNodes в expand.go (walkCallGraph), а не молчаливое
// urezanie.
const maxAmbiguityOptions = 8

// maxAmbiguities — потолок числа САМИХ Ambiguity-записей в ответе (не путать
// с maxAmbiguityOptions — потолком опций внутри одной записи): до
// стоп-слов один текст задачи мог дать по Ambiguity на каждого из
// maxAnchorCandidates=20 кандидатов сразу в двух проходах (metadata+symbol) —
// 40 записей потенциально. boundAmbiguities (build.go) применяет и этот
// потолок, и реальный учёт char budget, с честным warning при обрезке.
const maxAmbiguities = 10

// taskStopWords — императивные глаголы и служебные обороты, которыми
// формулировка задачи 1С-разработчика почти всегда НАЧИНАЕТСЯ («Проверить
// план обмена...», «Исправь ошибку...», «Поменяй обработчик...» — см.
// evals/*.json, корпус реалистичных формулировок), но которые сами по себе
// никогда не являются содержательным именем объекта/символа 1С. Без этого
// фильтра голое слово вроде «Проверить» становится anchor-кандидатом наравне
// с «ПланОбмена»/«Контрагенты» — а «Проверить» как имя процедуры-проверки
// (конвенция именования, та же природа, что «СкладПриИзменении» из
// doc-комментария findAnchors) реально встречается в десятках модулей
// реальной выгрузки, exactSymbolLookup честно резолвит все и полностью
// уводит контекст от темы задачи (находка доводки P6, воспроизведена дважды:
// «Проверить план обмена для синхронизации контрагентов» даёт 21
// несвязанный anchor/ambiguity, та же тема без «Проверить» — чистый
// релевантный anchor).
//
// Список — только грамматический класс «глагол-императив/инфинитив
// формулировки задачи», не эвристика по частоте: каждое слово либо взято из
// примера находки, либо реально встречается в evals/*.json как служебный
// глагол вокруг содержательных терминов (не сам термин). Существительные и
// потенциальные части составных терминов 1С (например «Проведение») сюда
// намеренно не входят — при малейшем сомнении слово остаётся кандидатом,
// ложноположительный anchor дешевле, чем случайно отфильтрованный реальный
// термин.
//
// Фильтр применяется только к токенам, разобранным из текста задачи (не к
// focusHints — там агент называет имя явно, это сильнее эвристики по тексту,
// см. doc-комментарий extractCandidateNames) и только к БЕЗ-квалификаторным
// кандидатам: «Модуль.Проверить» — явная структурная ссылка на конкретный
// метод, качественно иной, куда более сильный сигнал (structuralSymbolLookup
// дополнительно проверяет совпадение пути модуля), фильтровать её так же
// неверно, как фильтровать реальный термин.
var taskStopWords = map[string]bool{
	"проверить": true, "проверь": true,
	"изменить": true, "измени": true, "поменять": true, "поменяй": true,
	"добавить": true, "добавь": true,
	"исправить": true, "исправь": true, "почини": true, "починить": true,
	"сделать": true, "сделай": true,
	"поправить": true, "поправь": true,
	"удалить": true, "удали": true,
	"настроить": true, "настрой": true,
	"посмотреть": true, "посмотри": true,
	"найти": true, "найди": true,
	"оценить": true, "оцени": true,
	"разобраться": true, "разберись": true,
	"показать": true, "покажи": true,
}

// candidateName — токен-кандидат с признаком «структурная форма»
// (Квалификатор.Имя) — такие разбираются в первую очередь и с более сильным
// anchorStrength: явное указание модуля резко снижает шанс случайного
// совпадения с общеупотребимым словом.
type candidateName struct {
	raw        string
	qualifier  string // normalized, "" если нет точки
	name       string // normalized (часть после точки, либо весь raw)
	structural bool
}

// extractCandidateNames — §24 шаг 2 «explicitNames(in.task, in.focusHints)»:
// focusHints идут первыми (агент назвал их явно, это сильнее эвристики по
// тексту), затем токены самого текста задачи. Дедуплицируется по name_norm,
// сортируется structural-first и по убыванию длины (длиннее — реже
// совпадает случайно), обрезается maxAnchorCandidates.
func extractCandidateNames(task string, focusHints []string) []candidateName {
	seen := map[string]bool{}
	var out []candidateName

	add := func(raw string, filterStopWords bool) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		qualifier, name, hasDot := "", raw, false
		if idx := strings.LastIndex(raw, "."); idx > 0 && idx < len(raw)-1 {
			qualifier, name, hasDot = raw[:idx], raw[idx+1:], true
		}
		nameNorm := domain.NormalizeName(name)
		if len([]rune(nameNorm)) < 3 {
			return
		}
		// Стоп-слова режут только голые (без квалификатора) кандидаты из
		// текста задачи — см. doc-комментарий taskStopWords: focusHints и
		// структурные Модуль.Имя — сильные явные сигналы, им фильтр не
		// нужен и был бы вреден.
		if filterStopWords && !hasDot && taskStopWords[nameNorm] {
			return
		}
		key := domain.NormalizeName(qualifier) + "\x00" + nameNorm
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, candidateName{
			raw: raw, qualifier: domain.NormalizeName(qualifier), name: nameNorm, structural: hasDot,
		})
	}

	for _, h := range focusHints {
		add(strings.TrimSpace(h), false)
	}
	for _, m := range identifierPattern.FindAllString(task, -1) {
		add(m, true)
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].structural != out[j].structural {
			return out[i].structural
		}
		return len(out[i].name) > len(out[j].name)
	})
	if len(out) > maxAnchorCandidates {
		out = out[:maxAnchorCandidates]
	}
	return out
}

// anchorKey — ключ дедупликации anchors.
func anchorKey(a Anchor) string {
	if a.Kind == "symbol" {
		return "symbol\x00" + a.UID
	}
	return "metadata_object\x00" + a.ObjectType + "\x00" + a.ObjectName + "\x00" + a.Component
}

// findAnchors — §24 шаг 2: точный lookup по symbol/metadata (name_norm) для
// каждого кандидата; структурные (Квалификатор.Имя) совпадения тем же
// проходом; если после этого anchors пуст или единственно слаб — подстрочный/
// синонимный (fuzzy), затем FTS. Embeddings отсутствуют (v1) — эскалация
// останавливается на FTS, честно.
//
// Стадия 1 идёт в ТРИ прохода по всем кандидатам, а не один интерлив —
// регрессия §4.3 evaluation-report.md: обработчик формы с общеупотребимым
// именем (например «СкладПриИзменении» — конвенция именования, встречается
// на форме почти каждого объекта с реквизитом «Склад») даёт РОВНО
// maxAnchors одноимённых exact-символов уже на первом кандидате, и объект-
// владелец («ЗаказКлиента»), названный в задаче following-словом, до
// truncation просто не доходит — anchors[:maxAnchors] отрезает его молча
// (никакого warning: expandForm/expandRegister/… честно возвращают nil,nil
// на чужом a.Kind, это не ошибка ИХ кода). Порядок проходов — по убыванию
// специфичности: структурные (Квалификатор.Имя, самый явный сигнал) →
// объекты метаданных (нужны form/register/add-attribute/rights/posting/
// query — ниже вариативность имён, реже коллизия) → голые символы (самые
// массовые тёзки). anchorKey-дедупликация (addAnchor) не даёт добавить один
// и тот же anchor дважды, если он совпал в нескольких проходах.
func findAnchors(tx *store.ReadTx, req Request) ([]Anchor, []Ambiguity, error) {
	candidates := extractCandidateNames(req.Task, req.FocusHints)
	componentFilter := map[string]bool{}
	for _, c := range req.ComponentHints {
		componentFilter[domain.NormalizeName(c)] = true
	}
	passesComponent := func(componentID string) bool {
		if len(componentFilter) == 0 {
			return true
		}
		return componentFilter[domain.NormalizeName(componentID)]
	}

	var anchors []Anchor
	var ambiguities []Ambiguity
	seen := map[string]bool{}
	addAnchor := func(a Anchor) {
		k := anchorKey(a)
		if seen[k] {
			return
		}
		seen[k] = true
		anchors = append(anchors, a)
	}

	// Проход 1: структурные совпадения (Квалификатор.Имя) — самый явный сигнал.
	for _, c := range candidates {
		if !c.structural {
			continue
		}
		structuralAnchors, err := structuralSymbolLookup(tx, c, passesComponent)
		if err != nil {
			return nil, nil, err
		}
		for _, a := range structuralAnchors {
			addAnchor(a)
		}
	}

	// Проход 2: объекты метаданных точно по имени — раньше голых символов,
	// иначе builder'ы, которым нужен именно metadata_object anchor (form,
	// register, add-attribute, rights, posting, query owner), остаются без
	// него, если другой кандидат из той же задачи оказался частым именем
	// символа (см. doc-комментарий выше).
	for _, c := range candidates {
		exactObj, err := exactMetadataLookup(tx, c.name, passesComponent)
		if err != nil {
			return nil, nil, err
		}
		if len(exactObj) > 1 {
			var opts []string
			for _, a := range exactObj {
				if len(opts) >= maxAmbiguityOptions {
					break
				}
				opts = append(opts, a.ObjectType+"."+a.Display+"@"+a.Component)
			}
			ambiguities = append(ambiguities, Ambiguity{
				Subject: c.name, Options: opts,
				Note:      "имя объекта метаданных совпадает в нескольких видах/компонентах",
				Truncated: len(exactObj) > len(opts), TotalOptions: len(exactObj),
			})
		}
		for _, a := range exactObj {
			addAnchor(a)
		}
	}

	// Проход 3: голые символы точно по имени — самый массовый источник
	// тёзок (конвенции именования вроде «XПриИзменении»), поэтому последний.
	for _, c := range candidates {
		exactSym, err := exactSymbolLookup(tx, c.name, passesComponent)
		if err != nil {
			return nil, nil, err
		}
		if len(exactSym) > 1 {
			var opts []string
			for _, a := range exactSym {
				if len(opts) >= maxAmbiguityOptions {
					break
				}
				opts = append(opts, a.Component+"."+a.Display)
			}
			ambiguities = append(ambiguities, Ambiguity{
				Subject: c.name, Options: opts,
				Note:      "несколько символов с этим именем в разных компонентах/модулях — anchors несут все, expansion идёт от каждого",
				Truncated: len(exactSym) > len(opts), TotalOptions: len(exactSym),
			})
		}
		for _, a := range exactSym {
			addAnchor(a)
		}
	}

	// Стадия 2 (эскалация только если стадия 1 не дала anchors — weak(anchors)
	// в буквальном прочтении псевдокода означает «пусто»; частичный, но
	// непустой результат стадии 1 уже доказал релевантность и не нуждается в
	// подстрочном шуме поверх).
	if len(anchors) == 0 {
		for _, c := range candidates {
			if len([]rune(c.name)) < 4 {
				continue // короткая подстрока даёт слишком много случайных совпадений
			}
			fuzzySym, err := fuzzySymbolLookup(tx, c.name, passesComponent)
			if err != nil {
				return nil, nil, err
			}
			for _, a := range fuzzySym {
				addAnchor(a)
			}
			fuzzyObj, err := fuzzyMetadataLookup(tx, c.name, passesComponent)
			if err != nil {
				return nil, nil, err
			}
			for _, a := range fuzzyObj {
				addAnchor(a)
			}
			if len(anchors) >= maxAnchors {
				break
			}
		}
	}

	// Стадия 3 (FTS) — только если ничего не нашлось вообще ни на одной из
	// предыдущих стадий.
	if len(anchors) == 0 {
		var terms []string
		for _, c := range candidates {
			if len([]rune(c.name)) >= 4 {
				terms = append(terms, c.name)
			}
		}
		ids, err := tx.SearchSymbolsFTS(terms, maxAnchors*2)
		if err != nil {
			return nil, nil, err
		}
		for _, id := range ids {
			row, ok, err := tx.SymbolByID(id)
			if err != nil {
				return nil, nil, err
			}
			if !ok || !passesComponent(row.ComponentID) {
				continue
			}
			addAnchor(Anchor{
				Kind: "symbol", UID: row.UID, Component: row.ComponentID,
				Display: row.ModulePath + "." + row.NameDisplay, Method: "fts", Strength: 0.4,
			})
			if len(anchors) >= maxAnchors {
				break
			}
		}
	}

	sort.SliceStable(anchors, func(i, j int) bool { return anchors[i].Strength > anchors[j].Strength })
	if len(anchors) > maxAnchors {
		anchors = anchors[:maxAnchors]
	}
	return anchors, ambiguities, nil
}

// suppressPostingHandlerAmbiguity убирает из ambiguities ровно один вид шума
// (П4/R32): омонимию по имени обработчика проведения, когда задача и так
// названа объектом. Условие узкое и именно такое — intent posting И среди
// анкеров есть объект метаданных И subject нормализуется в имя обработчика
// проведения: в этой тройке десятки одноимённых ОбработкаПроведения чужих
// документов к задаче отношения не имеют, анкер задан объектом, и обработчик
// найден фактом принадлежности своему модулю (findPostingHandler), а не
// выбором из омонимов. Вне тройки неоднозначность остаётся: без анкера-объекта
// она реальная развилка, а «выключить ambiguities для posting» решением не
// является.
func suppressPostingHandlerAmbiguity(intent string, anchors []Anchor, ambiguities []Ambiguity) []Ambiguity {
	if intent != IntentPosting || len(ambiguities) == 0 {
		return ambiguities
	}
	hasObjectAnchor := false
	for _, a := range anchors {
		if a.Kind == "metadata_object" {
			hasObjectAnchor = true
			break
		}
	}
	if !hasObjectAnchor {
		return ambiguities
	}
	var out []Ambiguity
	for _, am := range ambiguities {
		if domain.NormalizeName(am.Subject) == postingHandlerNameNorm {
			continue
		}
		out = append(out, am)
	}
	return out
}

// exactSymbolLookup ищет символ РОВНО по имени — через SymbolsByNameNormExact
// (name_norm=?, использует idx_symbol_name), НЕ через FindSymbols: та строит
// LIKE '%x%', обязывающий SQLite сканировать всю таблицу symbol на каждый
// вызов — на реальной выгрузке (десятки тысяч символов) это было измеримо
// дорого (см. doc-комментарий store.SymbolsByNameNormExact).
func exactSymbolLookup(tx *store.ReadTx, nameNorm string, passes func(string) bool) ([]Anchor, error) {
	rows, err := tx.SymbolsByNameNormExact(nameNorm)
	if err != nil {
		return nil, err
	}
	var out []Anchor
	for _, r := range rows {
		if !passes(r.ComponentID) {
			continue
		}
		out = append(out, Anchor{
			Kind: "symbol", UID: r.UID, Component: r.ComponentID,
			Display: r.ModulePath + "." + r.NameDisplay, Method: "exact", Strength: 1.0,
		})
	}
	return out, nil
}

func fuzzySymbolLookup(tx *store.ReadTx, nameNormSubstr string, passes func(string) bool) ([]Anchor, error) {
	rows, err := tx.FindSymbols(store.SymbolSearch{NameNorm: nameNormSubstr, Limit: 20})
	if err != nil {
		return nil, err
	}
	var out []Anchor
	for _, r := range rows {
		if !passes(r.ComponentID) {
			continue
		}
		out = append(out, Anchor{
			Kind: "symbol", UID: r.UID, Component: r.ComponentID,
			Display: r.ModulePath + "." + r.NameDisplay, Method: "fuzzy", Strength: 0.6,
		})
	}
	return out, nil
}

// structuralSymbolLookup разбирает Квалификатор.Имя: находит символы с точным
// name_norm=имя, чей путь модуля содержит нормализованный квалификатор
// подстрокой (общий модуль/менеджер объекта обычно несёт своё имя в пути) —
// «структурные совпадения» §24 шаг 2, третья (после точного и до FTS) стадия
// разбора explicit names, специфичная для формы Модуль.Метод.
func structuralSymbolLookup(tx *store.ReadTx, c candidateName, passes func(string) bool) ([]Anchor, error) {
	rows, err := tx.SymbolsByNameNormExact(c.name)
	if err != nil {
		return nil, err
	}
	var out []Anchor
	for _, r := range rows {
		if !passes(r.ComponentID) {
			continue
		}
		if !strings.Contains(domain.NormalizeName(r.ModulePath), c.qualifier) {
			continue
		}
		out = append(out, Anchor{
			Kind: "symbol", UID: r.UID, Component: r.ComponentID,
			Display: r.ModulePath + "." + r.NameDisplay, Method: "structural", Strength: 0.95,
		})
	}
	return out, nil
}

func exactMetadataLookup(tx *store.ReadTx, nameNorm string, passes func(string) bool) ([]Anchor, error) {
	rows, err := tx.MetadataObjectsByNameNormAnyType(nameNorm)
	if err != nil {
		return nil, err
	}
	var out []Anchor
	for _, r := range rows {
		if !passes(r.ComponentID) {
			continue
		}
		out = append(out, Anchor{
			Kind: "metadata_object", ObjectType: r.MType, ObjectName: r.NameNorm, Component: r.ComponentID,
			Display: r.NameDisplay, Method: "exact", Strength: 1.0,
		})
	}
	return out, nil
}

func fuzzyMetadataLookup(tx *store.ReadTx, nameNormSubstr string, passes func(string) bool) ([]Anchor, error) {
	rows, err := tx.SearchMetadataObjects(nameNormSubstr, 20)
	if err != nil {
		return nil, err
	}
	var out []Anchor
	for _, r := range rows {
		if !passes(r.ComponentID) {
			continue
		}
		out = append(out, Anchor{
			Kind: "metadata_object", ObjectType: r.MType, ObjectName: r.NameNorm, Component: r.ComponentID,
			Display: r.NameDisplay, Method: "fuzzy", Strength: 0.6,
		})
	}
	return out, nil
}
