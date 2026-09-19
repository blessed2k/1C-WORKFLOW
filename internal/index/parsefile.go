package index

import (
	"bytes"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// parseOneFile разбирает один файл в fileRecord: единственная точка,
// решающая ПО РАСШИРЕНИЮ, каким парсером идти — bsl.Parse для .bsl,
// meta.Classify+meta.ParseFile для .xml (единственная точка чтения XML,
// D01/interfaces.md). Чистая функция: диска не касается, hash считает через
// store.HashContent (единственный алгоритм хэша на весь индекс).
func parseOneFile(relPath string, data []byte) *fileRecord {
	return parseHashedFile(relPath, data, store.HashContent(data))
}

// prepareFile: работа пула разбора над одним файлом (issue #3, шаг 1).
// Образ для blob хэшируется и сжимается здесь же, в воркере, и тот же хэш
// становится contentHash записи: SHA-256 на файл считается один раз, а
// писателю остаётся положить готовые байты.
func prepareFile(relPath string, data []byte) (*fileRecord, store.PreparedBlob, error) {
	blob, err := store.PrepareBlob(data)
	if err != nil {
		return nil, store.PreparedBlob{}, err
	}
	return parseHashedFile(relPath, data, blob.Hash()), blob, nil
}

// parseHashedFile: тело parseOneFile при уже посчитанном хэше содержимого.
func parseHashedFile(relPath string, data []byte, hash string) *fileRecord {
	rec := &fileRecord{
		relPath:       relPath,
		size:          int64(len(data)),
		contentHash:   hash,
		parserVersion: ParserVersion,
	}

	lower := strings.ToLower(relPath)
	switch {
	case strings.HasSuffix(lower, ".bsl"):
		mod, diags := bsl.Parse(data, bsl.Options{File: relPath})
		rec.bslModule = mod
		rec.moduleInfo = bsl.ClassifyModule(relPath)
		rec.diagnostics = append(rec.diagnostics, diags...)
		if mod != nil && rec.moduleInfo.Kind != bsl.ModuleUnknown &&
			len(mod.Methods) == 0 && len(mod.Variables) == 0 && hasSignificantContent(data) {
			// validate (§17 п.7): парсер не вернул ни одного факта на
			// непустом файле известного вида модуля — диагностика, а не
			// молчаливая публикация пустоты. hasSignificantContent отсекает
			// ложные срабатывания (ревью: 887 из 887 на ut_demo, ~90% —
			// файлы из одного BOM и оболочки #Если/#Область без кода внутри
			// ни одной ветки, легитимно пустые модули типовой конфигурации,
			// например RecordSetModule.bsl без переопределений).
			rec.diagnostics = append(rec.diagnostics, domain.Diagnostic{
				Code:     "index_empty_parse",
				Severity: domain.SeverityWarning,
				Message:  "парсер BSL не нашёл ни одного метода или переменной в непустом файле",
				File:     relPath,
			})
		}
	case strings.HasSuffix(lower, ".xml"):
		kind := meta.Classify(relPath)
		rec.metaKind = kind
		if kind != meta.KindUnsupported && kind != meta.KindBinary {
			facts, diags := meta.ParseFile(relPath, data)
			rec.metaFacts = facts
			rec.diagnostics = append(rec.diagnostics, diags...)
		}
	}
	return rec
}

// hasSignificantContent сообщает, остаётся ли в файле хоть один непустой
// токен после снятия BOM и построчного отсева пустых строк, строк
// препроцессора (#Если/#КонецЕсли/#Область/#КонецОбласти и подобных, включая
// многострочные условия, которые начинаются с "#" И потому попадают в тот же
// отсев по первому непробельному символу строки) и строк-комментариев BSL
// (начинающихся с "//" — включая точки врезки БСП/типовых конфигураций вида
// "//++ Локализация" / "//-- Локализация", распространённейший паттерн в
// реальных конфигурациях, не edge case). Файл, где не осталось ничего
// значимого, — легитимно пустой модуль, а не сбой парсера (упрощение:
// построчная эвристика, а не разбор препроцессора — для решения «пуст ли
// модуль» точнее не требуется).
func hasSignificantContent(data []byte) bool {
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	for _, line := range bytes.Split(data, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' || bytes.HasPrefix(trimmed, []byte("//")) {
			continue
		}
		return true
	}
	return false
}
