package meta

import (
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// parseSubscription разбирает EventSubscriptions/<Имя>.xml. Источник события
// задаётся либо <v8:Type> (конкретный тип), либо <v8:TypeSet> — который сам
// бывает голым видом ("DocumentObject", срабатывает на КАЖДЫЙ документ) или
// ОпределяемымТипом ("DefinedType.Имя"). Обе формы TypeSet обязаны быть
// распознаны: чтение только <v8:Type> теряет 128 из 308 подписок УТ
// (internal/source/writepath.go — образец семантики, сохраняемой здесь).
func parseSubscription(relPath string, src []byte) (Facts, []domain.Diagnostic) {
	var sub xmlSubscription
	if diags := unmarshalXML(relPath, src, &sub); diags != nil {
		return Facts{}, diags
	}
	p := sub.Object.Properties

	var diags []domain.Diagnostic
	if strings.TrimSpace(p.Name) == "" {
		diags = append(diags, domain.Diagnostic{
			Code:     DiagEmptyName,
			Severity: domain.SeverityWarning,
			Message:  "подписка на событие без имени",
			File:     relPath,
		})
	}

	fact := &EventSubscriptionFact{
		NameNorm:    domain.NormalizeName(p.Name),
		NameDisplay: p.Name,
		Event:       p.Event,
		HandlerRaw:  p.Handler,
	}
	for _, raw := range p.Source.Types {
		fact.Sources = append(fact.Sources, EventSubscriptionSourceFact{
			Kind: SourceKindType,
			Name: stripNamespacePrefix(raw),
		})
	}
	for _, raw := range p.Source.TypeSets {
		set := stripNamespacePrefix(raw)
		if name, ok := strings.CutPrefix(set, "DefinedType."); ok {
			fact.Sources = append(fact.Sources, EventSubscriptionSourceFact{
				Kind: SourceKindDefinedType,
				Name: name,
			})
			continue
		}
		fact.Sources = append(fact.Sources, EventSubscriptionSourceFact{
			Kind: SourceKindBareKind,
			Name: set,
		})
	}

	return Facts{Subscription: fact}, diags
}

// stripNamespacePrefix убирает префикс вида "cfg:"/"d5p1:" у идентификатора
// типа — он варьируется между выгрузками, значение всегда после ":".
func stripNamespacePrefix(raw string) string {
	if _, rest, ok := strings.Cut(strings.TrimSpace(raw), ":"); ok {
		return rest
	}
	return strings.TrimSpace(raw)
}
