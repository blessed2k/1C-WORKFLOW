package effective

import (
	"fmt"
	"sort"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// ApplyingTo отбирает расширения с AppliesTo == base и упорядочивает их по
// ApplyOrder, а при равном ApplyOrder по ID. Второй ключ делает порядок
// детерминированным и совпадает с тем, что раньше давала стабильная
// сортировка поверх store.Components (там строки идут ORDER BY id).
// Единственное правило порядка наложения: им пользуются оба адаптера и отбор
// подписок расширений в retrieve (ADR-030).
func ApplyingTo(exts []Extension, base string) []Extension {
	var out []Extension
	for _, e := range exts {
		if e.AppliesTo == base {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ApplyOrder != out[j].ApplyOrder {
			return out[i].ApplyOrder < out[j].ApplyOrder
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ExtensionsFromStore переводит состав компонентов store в расширения пакета.
// Компоненты других видов отбрасываются здесь: дальше пакет о видах не знает.
func ExtensionsFromStore(comps []store.Component) []Extension {
	var out []Extension
	for _, c := range comps {
		if c.Kind == string(domain.KindExtension) {
			out = append(out, Extension{ID: c.ID, AppliesTo: c.AppliesTo, ApplyOrder: c.ApplyOrder})
		}
	}
	return out
}

// StoreSource: продакшн-источник поверх уже открытой read-транзакции.
// Порядок расширений берётся из store.Components ТОЙ ЖЕ транзакции, а не из
// манифеста: только он согласован по снапшоту с читаемыми blob-ами. Манифест
// на диске может разойтись с проиндексированным составом, и тогда два
// инструмента показали бы разный порядок одних и тех же перехватчиков.
func StoreSource(tx *store.ReadTx) Source { return storeSource{tx: tx} }

type storeSource struct{ tx *store.ReadTx }

func (s storeSource) ExtensionsApplyingTo(base string) ([]Extension, error) {
	comps, err := s.tx.Components()
	if err != nil {
		return nil, fmt.Errorf("состав компонентов: %w", err)
	}
	return ApplyingTo(ExtensionsFromStore(comps), base), nil
}

func (s storeSource) ModuleText(ext, modulePath string) ([]byte, bool, error) {
	fid, ok, err := s.tx.SourceFileID(ext, modulePath)
	if err != nil || !ok {
		return nil, false, err
	}
	sf, ok, err := s.tx.SourceFileByID(fid)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		// Модуль заимствован (SourceFileID нашёлся), но строки файла нет:
		// расширение числится заимствовавшим, текста нет.
		return nil, true, nil
	}
	blob, err := s.tx.Blob(sf.ContentHash)
	if err != nil {
		return nil, true, &BlobUnavailableError{Err: err}
	}
	return blob, true, nil
}

// ModuleKey: адрес модуля в in-memory источнике.
type ModuleKey struct {
	Component string
	Path      string
}

// Memory: in-memory источник для тестов. Порядок расширений считает тем же
// ApplyingTo, что и StoreSource, поэтому тест порядка проверяет продакшн-правило.
type Memory struct {
	Extensions []Extension
	// Modules: исходники заимствованных модулей.
	Modules map[ModuleKey][]byte
	// NoFileRow: модуль заимствован, но строки файла нет (текста нет).
	NoFileRow map[ModuleKey]bool
	// Unreadable: модуль заимствован, но исходник не читается.
	Unreadable map[ModuleKey]error
	// Failing: отказ источника, прерывающий вычисление.
	Failing map[ModuleKey]error
}

func (m *Memory) ExtensionsApplyingTo(base string) ([]Extension, error) {
	return ApplyingTo(m.Extensions, base), nil
}

func (m *Memory) ModuleText(ext, modulePath string) ([]byte, bool, error) {
	k := ModuleKey{Component: ext, Path: modulePath}
	if err, ok := m.Failing[k]; ok {
		return nil, false, err
	}
	if err, ok := m.Unreadable[k]; ok {
		return nil, true, &BlobUnavailableError{Err: err}
	}
	if m.NoFileRow[k] {
		return nil, true, nil
	}
	text, ok := m.Modules[k]
	return text, ok, nil
}
