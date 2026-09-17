package main

import (
	"fmt"

	"github.com/blessed2k/1C-WORKFLOW/internal/source"
)

// The routing map in serverInstructions is read once, at the start of a session;
// the decision it is meant to influence ("write this query from memory, or fetch
// the schema first?") is taken much later, when the model is deep in context and
// feels certain. Instructions cannot compete with that: by then they are 100
// messages away and the answer feels already known.
//
// A hint carried by the answer of a neighbouring tool arrives at the other end:
// the object is in hand, the next step is one line below it, and no separate
// recall is required. That is why this exists as data on the response rather than
// as another sentence in the instructions — telling the model harder does not
// work, handing it the next step at the right moment does.
type objectStructureOutput struct {
	source.ObjectStructure
	NextSteps []string `json:"nextSteps,omitempty" jsonschema:"what to call next for this object, at the point where it is needed"`
}

// nextStepsFor derives the follow-ups that apply to this particular object, so
// the hint is specific ("this document posts") rather than a generic reminder.
// Only steps whose tool the server advertises (has) are kept: in live mode the
// offline tools below do not exist.
func nextStepsFor(obj source.ObjectStructure, has func(string) bool) []string {
	ref := fmt.Sprintf("type=%s name=%s", obj.Type, obj.Name)
	type step struct{ tool, text string }
	candidates := []step{{"get_query_schema",
		"Пишете запрос по этому объекту: get_query_schema (" + ref + "), точные имена полей, виртуальные таблицы и их параметры. Имена реквизитов выше даны в терминах объекта, в запросе они другие."}}
	if obj.Type == "Document" {
		candidates = append(candidates, step{"get_movements",
			"Меняете или оцениваете проведение: get_movements (name=" + obj.Name + "), по каким регистрам двигает и что делает код."})
	}
	if len(obj.Forms) > 0 {
		candidates = append(candidates, step{"form_impact",
			"Правите форму программно: form_impact (" + ref + ", form=<имя формы>), кто ещё её меняет; черновик проверяется через draftCode до применения."})
	}
	candidates = append(candidates, step{"find_metadata_usages",
		"Меняете или удаляете реквизит: find_metadata_usages (" + ref + "), где используется как тип и в правах ролей."})
	var steps []string
	for _, c := range candidates {
		if has(c.tool) {
			steps = append(steps, c.text)
		}
	}
	return steps
}
