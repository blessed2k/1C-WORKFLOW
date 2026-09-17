package meta

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// parsePredefinedData разбирает ".../<Имя>/Ext/Predefined.xml": предопределённые
// элементы справочника, плана видов характеристик и подобных объектов.
// Владелец не назван внутри файла — он берётся из пути.
func parsePredefinedData(relPath string, src []byte) (Facts, []domain.Diagnostic) {
	var pd xmlPredefinedData
	if diags := unmarshalXML(relPath, src, &pd); diags != nil {
		return Facts{}, diags
	}

	ownerType, ownerName := predefinedOwnerFromPath(relPath)
	items := make([]PredefinedItemFact, 0, len(pd.Items))
	for _, it := range pd.Items {
		items = append(items, PredefinedItemFact{
			OwnerType:    ownerType,
			OwnerNameRaw: ownerName,
			NameNorm:     domain.NormalizeName(it.Name),
			NameDisplay:  it.Name,
			Code:         it.Code,
			IsFolder:     it.IsFolder == "true",
		})
	}

	return Facts{Predefined: items}, nil
}

// predefinedOwnerFromPath извлекает тип и имя владельца из
// "<Тип>/<Имя>/Ext/Predefined.xml".
func predefinedOwnerFromPath(relPath string) (ownerType, ownerName string) {
	segs := strings.Split(domain.NormalizeModulePath(relPath), "/")
	if n := len(segs); n >= 4 {
		return segs[n-4], segs[n-3]
	}
	return "", ""
}
