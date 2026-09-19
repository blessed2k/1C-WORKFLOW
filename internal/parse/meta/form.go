package meta

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// parseFormStructure разбирает структуру формы (.../Ext/Form.xml): элементы,
// команды формы и привязки обработчиков к событиям формы и её элементов.
// Имя самой формы Form.xml не несёт — оно берётся из пути (папка перед
// "/Ext/Form.xml"), поэтому идентичность строится здесь же, из relPath.
func parseFormStructure(relPath string, src []byte) (Facts, []domain.Diagnostic) {
	var f xmlForm
	if diags := unmarshalXML(relPath, src, &f); diags != nil {
		return Facts{}, diags
	}

	structure := &FormStructureFact{Key: formKeyFromStructurePath(relPath)}
	for _, ev := range f.Events.Events {
		if handler := strings.TrimSpace(ev.Handler); handler != "" {
			structure.Handlers = append(structure.Handlers, HandlerBindingFact{
				Event:           ev.Name,
				HandlerNameNorm: domain.NormalizeName(handler),
				HandlerDisplay:  handler,
			})
		}
	}
	walkFormItems(f.ChildItems.Items, structure)
	// Реквизиты формы (f.Attributes) типизируют данные, которые уже видны
	// через элементы и их DataPath; отдельного факта под них ни один
	// критерий приёмки этого таска не требует.
	for _, c := range f.Commands.Commands {
		structure.Commands = append(structure.Commands, FormCommandFact{
			NameNorm:    domain.NormalizeName(c.Name),
			NameDisplay: c.Name,
			ActionNorm:  domain.NormalizeName(c.Action),
		})
	}

	return Facts{FormStructure: structure}, nil
}

// walkFormItems обходит элементы формы рекурсивно через <ChildItems>,
// собирая элементы и привязки их обработчиков.
func walkFormItems(items []xmlItem, structure *FormStructureFact) {
	for _, it := range items {
		structure.Elements = append(structure.Elements, FormElementFact{
			NameNorm:    domain.NormalizeName(it.Name),
			NameDisplay: it.Name,
			EType:       it.XMLName.Local,
			DataPath:    it.DataPath,
		})
		for _, ev := range it.Events.Events {
			if handler := strings.TrimSpace(ev.Handler); handler != "" {
				structure.Handlers = append(structure.Handlers, HandlerBindingFact{
					Source:          domain.NormalizeName(it.Name),
					Event:           ev.Name,
					HandlerNameNorm: domain.NormalizeName(handler),
					HandlerDisplay:  handler,
				})
			}
		}
		if it.ChildItems != nil {
			walkFormItems(it.ChildItems.Items, structure)
		}
	}
}

// formKeyFromStructurePath строит идентификатор формы из пути Form.xml:
// всё, что стоит перед "/Ext/Form.xml". Тот же самый идентификатор строит
// formKeyFromOwnerRef/parseMetadataObject у объявления формы — совпадение
// ключей и есть проверка «одна identity, а не два объекта».
func formKeyFromStructurePath(relPath string) string {
	norm := domain.NormalizeModulePath(relPath)
	const suffix = "/Ext/Form.xml"
	return strings.TrimSuffix(norm, suffix)
}
