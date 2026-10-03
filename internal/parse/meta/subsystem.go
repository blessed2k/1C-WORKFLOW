package meta

import (
	"encoding/xml"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// SubsystemContent — состав одной подсистемы, как он записан в её объявлении.
type SubsystemContent struct {
	// Synonym: представление подсистемы для человека («Работа с файлами»).
	Synonym string
	// Objects — объекты состава в виде "Вид.Имя" (CommonModule.ОбщегоНазначения).
	Objects []string
	// Children — имена дочерних подсистем. Их объявления лежат в каталоге
	// этой подсистемы, состав родителя их объекты не повторяет.
	Children []string
}

type xmlSubsystemRoot struct {
	XMLName   xml.Name `xml:"MetaDataObject"`
	Subsystem struct {
		Properties struct {
			Synonym xmlSynonym `xml:"Synonym"`
			Content struct {
				Items []string `xml:"Item"`
			} `xml:"Content"`
		} `xml:"Properties"`
		ChildObjects struct {
			Subsystems []string `xml:"Subsystem"`
		} `xml:"ChildObjects"`
	} `xml:"Subsystem"`
}

// ParseSubsystemContent читает состав подсистемы из её объявления.
//
// В Facts этот разбор не входит: ParseFile отдаёт индексу плоские скалярные
// свойства объекта, а состав подсистемы повторяющийся элемент. Пока индекс не
// хранит рёбра «подсистема содержит объект», состав нужен только тому, кто
// читает объявление сам.
func ParseSubsystemContent(relPath string, src []byte) (SubsystemContent, []domain.Diagnostic) {
	var root xmlSubsystemRoot
	if diags := unmarshalXML(relPath, src, &root); diags != nil {
		return SubsystemContent{}, diags
	}
	return SubsystemContent{
		Synonym:  root.Subsystem.Properties.Synonym.ru(),
		Objects:  trimmedNonEmpty(root.Subsystem.Properties.Content.Items),
		Children: trimmedNonEmpty(root.Subsystem.ChildObjects.Subsystems),
	}, nil
}

func trimmedNonEmpty(items []string) []string {
	var out []string
	for _, it := range items {
		if it = strings.TrimSpace(it); it != "" {
			out = append(out, it)
		}
	}
	return out
}
