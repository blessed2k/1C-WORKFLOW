package source

import (
	"fmt"
	"sort"
	"strings"
)

// formGuidance turns what is already on the form into rules for writing your
// own code on it. Conflicts describe what is already broken; guidance says how
// not to break it — it is what an agent needs BEFORE writing, when its code is
// not in any export yet.
// scanned reports whether at least one export root was actually read; without
// it an empty result says nothing about the form and must not reassure.
func formGuidance(sources []FormModifier, draft *DraftOptions, scanned bool) []string {
	var out []string

	if !scanned {
		return []string{"Источники не прочитаны — по этой форме нельзя сказать ничего. Проверьте пути в dumps (нужен каталог с Configuration.xml)."}
	}
	if len(sources) == 0 {
		return []string{"На форме нет программных изменений: можно добавлять своё, соблюдая префикс имён своего расширения."}
	}

	// 1. Does the base method attach BSP print / pluggable commands? Then an
	// &Вместо interceptor without ПродолжитьВызов would silently drop them.
	var pluggableTargets []string
	for _, m := range sources {
		if m.Extension != "" {
			continue
		}
		if has(m, "БСП_ПодключаемыеКоманды") || has(m, "БСП_Печать") {
			pluggableTargets = append(pluggableTargets, m.Module)
		}
	}
	if len(pluggableTargets) > 0 {
		out = append(out, "Базовый модуль формы подключает команды печати/подключаемые команды БСП: перехватывать через &Вместо можно только с вызовом ПродолжитьВызов, иначе печать (в т.ч. бейджики) молча пропадёт. Безопаснее &Перед или &После.")
	} else {
		out = append(out, "Использовать &Перед или &После; &Вместо — только с вызовом ПродолжитьВызов, иначе базовый метод не выполнится.")
	}

	// 2. Naming: the prefix to use and the names already taken.
	if draft != nil && draft.Prefix != "" {
		out = append(out, fmt.Sprintf("Именовать добавляемые элементы, реквизиты и команды с префиксом %q.", draft.Prefix))
	} else {
		out = append(out, "Именовать добавляемые элементы, реквизиты и команды с префиксом своего расширения.")
	}
	if taken := takenNames(sources, draft); len(taken) > 0 {
		out = append(out, fmt.Sprintf("Имена уже заняты, не использовать: %s.", strings.Join(taken, ", ")))
	}

	// 3. Foreign programmatic elements must be left alone.
	if foreign := foreignElements(sources, draft); len(foreign) > 0 {
		out = append(out, fmt.Sprintf("Чужие программные элементы (не удалять, не перемещать, не менять родителя): %s.", strings.Join(foreign, ", ")))
	}

	// 4. Attribute set.
	if changers := attributeChangers(sources, draft); len(changers) > 0 {
		out = append(out, fmt.Sprintf("Состав реквизитов формы уже меняют: %s. Добавлять свои реквизиты одним вызовом ИзменитьРеквизиты и не рассчитывать на чужие (порядок расширений не гарантирован).",
			strings.Join(changers, ", ")))
	}
	out = append(out, "Не передавать второй аргумент в ИзменитьРеквизиты, если не удаляете СВОИ реквизиты: удаляются ровно переданные пути.")

	return out
}

// takenNames lists element/attribute/command names already added by others.
func takenNames(sources []FormModifier, draft *DraftOptions) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range sources {
		if isDraft(m, draft) {
			continue
		}
		for _, c := range m.Changes {
			switch c.Kind {
			case "ДобавитьЭлемент", "ДобавитьРеквизит", "ДобавитьКоманду":
				if c.Target != "" && !seen[c.Target] {
					seen[c.Target] = true
					out = append(out, c.Target)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// foreignElements lists elements created programmatically by other sources.
func foreignElements(sources []FormModifier, draft *DraftOptions) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range sources {
		if isDraft(m, draft) {
			continue
		}
		for _, c := range m.Changes {
			if c.Kind != "ДобавитьЭлемент" || c.Target == "" || seen[c.Target] {
				continue
			}
			seen[c.Target] = true
			owner := m.Extension
			if owner == "" {
				owner = "основная конфигурация"
			}
			out = append(out, fmt.Sprintf("%s (%s)", c.Target, owner))
		}
	}
	sort.Strings(out)
	return out
}

// attributeChangers lists sources that already change the attribute set.
func attributeChangers(sources []FormModifier, draft *DraftOptions) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range sources {
		if isDraft(m, draft) {
			continue
		}
		if !has(m, "ИзменитьРеквизиты") && !has(m, "ИзменитьРеквизитыСУдалением") {
			continue
		}
		name := m.Extension
		if name == "" {
			name = "основная конфигурация"
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// isDraft reports whether a modifier is the draft itself: guidance describes the
// environment, so the draft must not be listed as its own neighbour.
func isDraft(m FormModifier, draft *DraftOptions) bool {
	return draft != nil && m.Dump == draftDump
}
