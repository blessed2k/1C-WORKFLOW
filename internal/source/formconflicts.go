package source

import (
	"fmt"
	"sort"
	"strings"
)

// detectFormConflicts turns the per-source change lists into predicted
// interference. Rules encode the ways programmatic form changes from several
// sources break each other in practice.
func detectFormConflicts(sources []FormModifier) []FormConflict {
	out := []FormConflict{}
	out = append(out, insteadWithoutContinue(sources)...)
	out = append(out, changeAttrsDropsDynamicCommands(sources)...)
	out = append(out, changeAttrsWithRemoval(sources)...)
	out = append(out, multipleChangeAttrs(sources)...)
	out = append(out, nameCollisions(sources)...)
	out = append(out, unprefixedNames(sources)...)
	out = append(out, foreignElementMutation(sources)...)
	return out
}

// has reports whether a modifier performs a change of the given kind.
func has(m FormModifier, kind string) bool {
	for _, c := range m.Changes {
		if c.Kind == kind {
			return true
		}
	}
	return false
}

// label identifies a source in a conflict message.
func label(m FormModifier) string {
	if m.Extension != "" {
		return "расширение " + m.Extension + " (" + m.Module + ")"
	}
	return "основная конфигурация (" + m.Module + ")"
}

// pluggableKinds are the BSP wirings whose silent loss this rule is about, in
// message order.
var pluggableKinds = []string{"БСП_ПодключаемыеКоманды", "БСП_Печать"}

// pluggableKindNames renders a wiring kind for a message.
var pluggableKindNames = map[string]string{
	"БСП_ПодключаемыеКоманды": "подключаемые команды БСП",
	"БСП_Печать":              "команды печати",
}

// baseWiring maps each BSP wiring to the base-configuration module that
// performs it. Only the base configuration counts: the rule is about an
// interceptor skipping the base method.
func baseWiring(sources []FormModifier) map[string]FormModifier {
	out := map[string]FormModifier{}
	for _, m := range sources {
		if m.Extension != "" {
			continue
		}
		for _, kind := range pluggableKinds {
			if _, seen := out[kind]; !seen && has(m, kind) {
				out[kind] = m
			}
		}
	}
	return out
}

// insteadWithoutContinue is the one mechanism that provably kills form wiring
// silently: an &Вместо interceptor without ПродолжитьВызов means the base
// method never runs. When the base method is the one that attaches BSP print /
// pluggable commands, those commands simply never appear.
//
// The interceptor may, however, be a copy of the base method and re-attach that
// wiring itself — then the commands do work and the "print disappears" verdict
// would be false. So the wiring is compared per kind: only what the interceptor
// does NOT redo is reported as lost. Re-attaching everything is still worth a
// finding of its own: the base method is skipped, so the copy silently falls
// behind the typical one on the next configuration update.
func insteadWithoutContinue(sources []FormModifier) []FormConflict {
	wiring := baseWiring(sources)
	var out []FormConflict
	for _, m := range sources {
		if m.Kind != "Вместо" || m.HasContinue {
			continue
		}
		var lost, involved []string
		for _, kind := range pluggableKinds {
			base, inBase := wiring[kind]
			if !inBase || has(m, kind) {
				continue
			}
			lost = append(lost, pluggableKindNames[kind])
			involved = append(involved, label(base))
		}
		switch {
		case len(lost) > 0:
			out = append(out, FormConflict{
				Code:     "InsteadWithoutContinueOverPrint",
				Severity: "high",
				Message: fmt.Sprintf("%s перехватывает %q через &Вместо и не вызывает ПродолжитьВызов — базовый метод не выполнится, а в нём основная конфигурация подключает: %s. Перехватчик этого не повторяет — подключение молча пропадёт.",
					label(m), m.Target, strings.Join(lost, ", ")),
				Suggestion: "Вызвать ПродолжитьВызов(...) в перехватчике, либо использовать &После/&Перед вместо &Вместо.",
				Involved:   append([]string{label(m)}, involved...),
			})
		case len(wiring) > 0:
			out = append(out, FormConflict{
				Code:     "InsteadDuplicatesBaseWiring",
				Severity: "medium",
				Message: fmt.Sprintf("%s перехватывает %q через &Вместо без ПродолжитьВызов, но сам повторяет подключение БСП из базового метода — это копия типового метода. Команды и печать работают, однако базовый метод не выполняется: при обновлении конфигурации копия отстанет от типовой и новая логика молча потеряется.",
					label(m), m.Target),
				Suggestion: "Свести правку к &Перед/&После либо вызвать ПродолжитьВызов(...). Пока это копия — сверять её с типовым методом при каждом обновлении.",
				Involved:   []string{label(m)},
			})
		default:
			out = append(out, FormConflict{
				Code:     "InsteadWithoutContinue",
				Severity: "medium",
				Message: fmt.Sprintf("%s перехватывает %q через &Вместо и не вызывает ПродолжитьВызов — базовый метод не выполнится целиком.",
					label(m), m.Target),
				Suggestion: "Вызвать ПродолжитьВызов(...), если базовое поведение нужно сохранить.",
				Involved:   []string{label(m)},
			})
		}
	}
	return out
}

// firstEdit returns the first change of any of the given kinds.
func firstEdit(m FormModifier, kinds ...string) (FormEdit, bool) {
	for _, c := range m.Changes {
		for _, k := range kinds {
			if c.Kind == k {
				return c, true
			}
		}
	}
	return FormEdit{}, false
}

// changeAttrsDropsDynamicCommands is the silent one, and the reason
// changeAttrsWithRemoval is not enough: ИзменитьРеквизиты rebuilds the form and
// resets commands that were added programmatically BEFORE the call. No removal
// list is involved and no error is raised — the submenu simply never appears.
func changeAttrsDropsDynamicCommands(sources []FormModifier) []FormConflict {
	var out []FormConflict
	for _, m := range sources {
		// Only code running after the base method can drop what it created:
		// &Перед runs before it, and &Вместо without ПродолжитьВызов means the
		// commands are never created in the first place.
		if m.Kind != "После" && !(m.Kind == "Вместо" && m.HasContinue) {
			continue
		}
		call, ok := firstEdit(m, "ИзменитьРеквизиты", "ИзменитьРеквизитыСУдалением")
		if !ok {
			continue
		}
		var lost, involved []string
		seen := map[string]bool{}
		for _, other := range sources {
			if other.Dump == m.Dump {
				continue // its own commands are its own business
			}
			for _, c := range other.Changes {
				if c.Kind != "ДобавитьКоманду" {
					continue
				}
				lost = append(lost, fmt.Sprintf("%s (строка %d)", c.Target, c.Line))
				if !seen[label(other)] {
					seen[label(other)] = true
					involved = append(involved, label(other))
				}
			}
		}
		if len(lost) == 0 {
			continue // no dynamic commands on this form: nothing to drop
		}
		out = append(out, FormConflict{
			Code:     "ChangeAttributesDropsDynamicCommands",
			Severity: "high",
			Message: fmt.Sprintf("%s вызывает ИзменитьРеквизиты в перехвате %s (строка %d). До этого момента команды формы добавляются программно: %s — ИзменитьРеквизиты перестраивает форму и сбрасывает динамически добавленные команды, подменю молча перестанет работать.",
				label(m), m.Interceptor, call.Line, strings.Join(lost, ", ")),
			Suggestion: "Не использовать ИзменитьРеквизиты на форме с динамическими командами: добавить реквизит и элементы статически в конфигураторе (заимствовать форму), в коде оставить только заполнение.",
			Involved:   append([]string{label(m)}, involved...),
		})
	}
	return out
}

// changeAttrsWithRemoval flags the only documented way ИзменитьРеквизиты drops
// something explicitly: the second argument. It removes exactly the paths passed
// to it, so passing anything that is not your own attribute deletes someone
// else's.
func changeAttrsWithRemoval(sources []FormModifier) []FormConflict {
	var out []FormConflict
	for _, m := range sources {
		for _, c := range m.Changes {
			if c.Kind != "ИзменитьРеквизитыСУдалением" {
				continue
			}
			out = append(out, FormConflict{
				Code:     "ChangeAttributesWithRemoval",
				Severity: "high",
				Message: fmt.Sprintf("%s вызывает ИзменитьРеквизиты с массивом удаляемых реквизитов (строка %d). Удаляются ровно переданные пути — если среди них реквизиты чужого кода, они исчезнут.",
					label(m), c.Line),
				Suggestion: "Удалять только реквизиты, добавленные этим же расширением; чужие программные реквизиты не трогать.",
				Involved:   []string{label(m)},
			})
		}
	}
	return out
}

// sameModule reports whether m is one of the listed modifiers.
func sameModule(m FormModifier, list []FormModifier) bool {
	for _, x := range list {
		if x.Module == m.Module && x.Dump == m.Dump {
			return true
		}
	}
	return false
}

// multipleChangeAttrs flags several sources rebuilding the attribute collection
// of one form: order between extensions is not guaranteed, and each call
// invalidates references obtained earlier.
func multipleChangeAttrs(sources []FormModifier) []FormConflict {
	byDump := map[string]string{}
	for _, m := range sources {
		if has(m, "ИзменитьРеквизиты") || has(m, "ИзменитьРеквизитыСУдалением") {
			byDump[m.Dump] = label(m)
		}
	}
	if len(byDump) < 2 {
		return nil
	}
	var involved, names []string
	for _, lbl := range byDump {
		involved = append(involved, lbl)
		names = append(names, lbl)
	}
	sort.Strings(involved)
	sort.Strings(names)
	return []FormConflict{{
		Code:     "MultipleChangeAttributes",
		Severity: "medium",
		Message: fmt.Sprintf("Состав реквизитов формы меняют несколько источников (%s). Операция ресурсоёмкая (платформа рекомендует пакетное изменение), а порядок выполнения расширений не гарантирован — не полагайтесь на реквизиты, добавленные другим расширением.",
			strings.Join(names, ", ")),
		Suggestion: "Каждому источнику добавлять свои реквизиты одним вызовом и не рассчитывать на чужие; при необходимости проверять наличие реквизита перед использованием.",
		Involved:   involved,
	}}
}

// nameCollisions flags the same element (or the same attribute) name added by
// two different exports: the second Добавить fails or silently shadows the
// first. Elements and attributes are separate namespaces, so they are keyed
// apart; sources are keyed by export root, because the form module path is
// identical in the base configuration and in every extension.
func nameCollisions(sources []FormModifier) []FormConflict {
	byKey := map[string]map[string]string{} // "kind|lower(name)" -> dump -> label
	origName := map[string]string{}         // key -> name as written
	for _, m := range sources {
		for _, c := range m.Changes {
			if c.Kind != "ДобавитьЭлемент" && c.Kind != "ДобавитьРеквизит" {
				continue
			}
			k := c.Kind + "|" + strings.ToLower(c.Target)
			if byKey[k] == nil {
				byKey[k] = map[string]string{}
			}
			byKey[k][m.Dump] = label(m)
			if _, ok := origName[k]; !ok {
				origName[k] = c.Target
			}
		}
	}
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var out []FormConflict
	for _, k := range keys {
		owners := byKey[k]
		if len(owners) < 2 {
			continue
		}
		kind, _, _ := strings.Cut(k, "|")
		name := origName[k]
		involved := make([]string, 0, len(owners))
		for _, lbl := range owners {
			involved = append(involved, lbl)
		}
		sort.Strings(involved)
		out = append(out, FormConflict{
			Code:     "NameCollision",
			Severity: "high",
			Message: fmt.Sprintf("%s с именем %q добавляют несколько источников (%s) — коллизия имён на форме.",
				kind, name, strings.Join(involved, ", ")),
			Suggestion: "Дать имени префикс своего расширения: имена элементов и реквизитов формы должны быть уникальны.",
			Involved:   involved,
		})
	}
	return out
}

// unprefixedNames flags an extension adding names without its own prefix: that
// is exactly what makes collisions with other extensions possible.
func unprefixedNames(sources []FormModifier) []FormConflict {
	var out []FormConflict
	for _, m := range sources {
		if m.Extension == "" || m.Prefix == "" {
			continue
		}
		var bad []string
		for _, c := range m.Changes {
			if c.Kind != "ДобавитьЭлемент" && c.Kind != "ДобавитьРеквизит" {
				continue
			}
			if !strings.HasPrefix(strings.ToLower(c.Target), strings.ToLower(m.Prefix)) {
				bad = append(bad, c.Target)
			}
		}
		if len(bad) == 0 {
			continue
		}
		out = append(out, FormConflict{
			Code:     "UnprefixedName",
			Severity: "medium",
			Message: fmt.Sprintf("%s добавляет имена без своего префикса %q: %s. Другое расширение может занять то же имя.",
				label(m), m.Prefix, strings.Join(bad, ", ")),
			Suggestion: "Именовать программно добавляемые элементы и реквизиты с префиксом расширения.",
			Involved:   []string{label(m)},
		})
	}
	return out
}

// foreignElementMutation flags deleting/moving/reparenting an element that a
// different export created: the owner's code then works on a missing element,
// and the order between extensions is not guaranteed.
func foreignElementMutation(sources []FormModifier) []FormConflict {
	type owner struct{ dump, label string }
	added := map[string][]owner{} // lower(name) -> exports creating it (may be several: that is a collision on its own)
	for _, m := range sources {
		for _, c := range m.Changes {
			if c.Kind == "ДобавитьЭлемент" {
				added[strings.ToLower(c.Target)] = append(added[strings.ToLower(c.Target)], owner{m.Dump, label(m)})
			}
		}
	}
	var out []FormConflict
	for _, m := range sources {
		for _, c := range m.Changes {
			if c.Kind != "УдалитьЭлемент" && c.Kind != "ПереместитьЭлемент" && c.Kind != "СменитьРодителя" {
				continue
			}
			name := strings.ToLower(c.Target)
			if i := strings.LastIndex(name, "."); i >= 0 {
				name = name[i+1:] // Элементы.Номенклатура / Поле.Родитель -> имя
			}
			var foreign []string
			for _, o := range added[name] {
				if o.dump != m.Dump {
					foreign = append(foreign, o.label)
				}
			}
			if len(foreign) == 0 {
				continue
			}
			sort.Strings(foreign)
			out = append(out, FormConflict{
				Code:     "ForeignElementMutation",
				Severity: "medium",
				Message: fmt.Sprintf("%s делает %s над элементом %q, который создаёт %s (строка %d).",
					label(m), c.Kind, c.Target, strings.Join(foreign, ", "), c.Line),
				Suggestion: "Не трогать чужие программные элементы: порядок выполнения расширений не гарантирован, элемента может не быть.",
				Involved:   append([]string{label(m)}, foreign...),
			})
		}
	}
	return out
}

// NOTE: an ActionOverride rule was considered and dropped: the element is
// almost always reached through a local variable (Поле = Элементы.Добавить(...);
// Поле.УстановитьДействие(...)), so it cannot be resolved statically. Keying by
// event name alone flagged two extensions setting ПриИзменении on their own
// separate fields, which is normal and not a conflict.
