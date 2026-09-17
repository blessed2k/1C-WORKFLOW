package source

import (
	"context"
	"fmt"
	"strings"
)

// access_diagnose проходит цепочку «почему пользователь не видит объект» до ПЕРВОГО
// обрыва и там останавливается. Смысл именно в остановке: две сессии ушли на разбор
// условий RLS в базе, где ограничение на уровне записей выключено целиком, и на
// поиск роли у пользователя, который вообще не связан с пользователем ИБ.
//
// Инструмент живой и отвечает на вопрос «что база даёт этому человеку». Последнее
// звено — даёт ли конкретная роль право на конкретный объект — читается из
// конфигурации, а не из базы, поэтому здесь оно не додумывается: вместо догадки
// возвращается готовый следующий шаг с набором ролей для rights_audit.

// AccessStep — одно звено цепочки.
type AccessStep struct {
	Step   string `json:"step"`
	OK     bool   `json:"ok" jsonschema:"звено пройдено; false означает обрыв, дальше не проверялось"`
	Detail string `json:"detail"`
}

// AccessDiagnosis — результат диагностики.
type AccessDiagnosis struct {
	User     string       `json:"user"`
	Object   string       `json:"object,omitempty"`
	Steps    []AccessStep `json:"steps"`
	Verdict  string       `json:"verdict"`
	Profiles []string     `json:"profiles,omitempty"`
	Roles    []string     `json:"roles,omitempty" jsonschema:"роли, которые пользователь получает через профили"`
	RoleNote string       `json:"roleNote,omitempty"`
	NextStep string       `json:"nextStep,omitempty" jsonschema:"чем продолжить, если цепочка дошла до конца"`
	Caveat   string       `json:"caveat,omitempty"`
}

func (d *AccessDiagnosis) add(step, detail string, ok bool) {
	d.Steps = append(d.Steps, AccessStep{Step: step, OK: ok, Detail: detail})
}

// stop фиксирует обрыв: дальше цепочку не идём, вердикт называет причину.
func (d *AccessDiagnosis) stop(step, detail, verdict string) *AccessDiagnosis {
	d.add(step, detail, false)
	d.Verdict = verdict
	return d
}

type userCard struct {
	Found    bool
	Name     string
	Invalid  bool
	Deleted  bool
	Service  bool
	IBLinked bool
}

// AccessDiagnose отвечает, что мешает пользователю получить доступ. object
// необязателен: без него цепочка всё равно доходит до набора ролей.
func AccessDiagnose(ctx context.Context, run queryRunner, user, object string) (*AccessDiagnosis, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return nil, fmt.Errorf("укажите пользователя: user=<наименование в справочнике Пользователи>")
	}
	d := &AccessDiagnosis{User: user, Object: strings.TrimSpace(object)}

	card, err := lookupUser(ctx, run, user)
	if err != nil {
		return nil, err
	}
	if !card.Found {
		return d.stop("пользователь в справочнике",
			"элемент справочника Пользователи с таким наименованием не найден",
			"пользователь не найден: проверьте написание наименования, оно не совпадает с именем входа"), nil
	}
	d.add("пользователь в справочнике", "найден", true)

	if card.Deleted || card.Invalid {
		reason := "помечен на удаление"
		if card.Invalid {
			reason = "установлен признак Недействителен"
		}
		return d.stop("пользователь действует", reason,
			"пользователь отключён ("+reason+"): роли и профили значения не имеют"), nil
	}
	d.add("пользователь действует", "не помечен на удаление, признак Недействителен снят", true)

	if !card.IBLinked {
		return d.stop("связь с пользователем ИБ",
			"ИдентификаторПользователяИБ не заполнен",
			"элемент справочника не связан с пользователем информационной базы: войти под ним нельзя, права не назначаются"), nil
	}
	d.add("связь с пользователем ИБ", "ИдентификаторПользователяИБ заполнен", true)

	access, err := userAccess(ctx, run, user)
	if err != nil {
		return nil, err
	}
	if len(access.Profiles) == 0 {
		return d.stop("группы доступа",
			"пользователь не входит ни в одну группу доступа",
			"профилей нет: пользователь получает только те права, что заданы вне механизма групп доступа"), nil
	}
	d.Profiles = access.Profiles
	d.add("группы доступа", fmt.Sprintf("групп %d, профилей %d: %s",
		len(access.Groups), len(access.Profiles), strings.Join(access.Profiles, ", ")), true)

	if len(access.Roles) == 0 {
		return d.stop("роли профилей",
			"ни один профиль не содержит ролей",
			"профили есть, но пусты: прав они не дают"), nil
	}
	d.Roles = access.Roles
	d.add("роли профилей", fmt.Sprintf("ролей всего %d", len(access.Roles)), true)
	if len(d.Roles) > maxRolesShown {
		d.RoleNote = fmt.Sprintf("показано %d из %d, полный список — access_profiles user=%q includeRoles=true",
			maxRolesShown, len(d.Roles), user)
		d.Roles = d.Roles[:maxRolesShown]
	}

	settings, err := accessSettings(ctx, run)
	if err != nil {
		return nil, err
	}
	if !settings.RecordLevel {
		d.add("ограничение на уровне записей",
			"ОграничиватьДоступНаУровнеЗаписей выключено: RLS, виды и значения доступа ни на что не влияют", true)
	} else {
		kinds, err := userAccessKinds(ctx, run, access.Profiles)
		if err != nil {
			return nil, err
		}
		d.add("ограничение на уровне записей",
			"включено; виды доступа профилей: "+describeKinds(kinds), true)
	}

	// Кэш назначаемых ролей — самое неочевидное звено: роль может быть в профиле и
	// при этом не действовать, пока параметры не пересчитаны обработчиками обновления ИБ.
	d.Caveat = "список назначаемых ролей кэшируется в Константа.ПараметрыРаботыПользователей и обновляется только обработчиками обновления ИБ. Если роль добавлена в профиль, а доступ не появился, дело обычно здесь, а не в правах"

	if d.Object == "" {
		d.Verdict = "цепочка до ролей пройдена без обрывов: пользователь действует, связан с ИБ и получает роли через профили"
		d.NextStep = "укажите object=<Тип.Имя>, чтобы получить готовый следующий шаг по правам на конкретный объект"
		return d, nil
	}

	d.Verdict = "цепочка в базе пройдена без обрывов: всё, что зависит от данных, у пользователя есть. Осталось звено из конфигурации — даёт ли хоть одна из его ролей право на объект"
	d.NextStep = fmt.Sprintf("rights_audit по %s в offline-режиме, затем пересечь список дающих ролей с ролями пользователя выше; если пересечение пусто — права нет ни у одной роли профиля, если не пусто — смотрите кэш параметров из caveat", d.Object)
	return d, nil
}

func lookupUser(ctx context.Context, run queryRunner, name string) (userCard, error) {
	res, err := run.ExecuteQuery(ctx, QueryParams{
		Limit:  1,
		Params: map[string]any{"ИмяПользователя": name},
		Text: `ВЫБРАТЬ ПЕРВЫЕ 1
	Пользователи.Наименование КАК Наименование,
	Пользователи.Недействителен КАК Недействителен,
	Пользователи.ПометкаУдаления КАК ПометкаУдаления,
	Пользователи.Служебный КАК Служебный,
	Пользователи.ИдентификаторПользователяИБ КАК ИдентификаторИБ
ИЗ
	Справочник.Пользователи КАК Пользователи
ГДЕ
	Пользователи.Наименование = &ИмяПользователя`,
	})
	if err != nil {
		return userCard{}, fmt.Errorf("карточка пользователя %q: %w", name, err)
	}
	if len(res.Rows) == 0 {
		return userCard{}, nil
	}
	row := res.Rows[0]
	return userCard{
		Found:    true,
		Name:     asText(row["Наименование"]),
		Invalid:  asBool(row["Недействителен"]),
		Deleted:  asBool(row["ПометкаУдаления"]),
		Service:  asBool(row["Служебный"]),
		IBLinked: isFilledUUID(asText(row["ИдентификаторИБ"])),
	}, nil
}

func userAccessKinds(ctx context.Context, run queryRunner, profiles []string) ([]AccessKind, error) {
	seen := map[string]AccessKind{}
	for _, profile := range profiles {
		kinds, err := profileKinds(ctx, run, profile)
		if err != nil {
			return nil, err
		}
		for _, k := range kinds {
			if prev, ok := seen[k.Kind]; !ok || (prev.AllAllowed && !k.AllAllowed) {
				seen[k.Kind] = k
			}
		}
	}
	out := make([]AccessKind, 0, len(seen))
	for _, name := range sortedKeys(toSetOfKinds(seen)) {
		out = append(out, seen[name])
	}
	return out, nil
}

func toSetOfKinds(m map[string]AccessKind) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

func describeKinds(kinds []AccessKind) string {
	if len(kinds) == 0 {
		return "видов доступа у профилей нет"
	}
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		state := "ограничен значениями"
		if k.AllAllowed {
			state = "все разрешены"
		}
		parts = append(parts, k.Kind+" ("+state+")")
	}
	return strings.Join(parts, ", ")
}

// AccessDiagnose реализует точку входа со стороны живого источника.
func (s *HTTPSource) AccessDiagnose(ctx context.Context, user, object string) (*AccessDiagnosis, error) {
	return AccessDiagnose(ctx, s, user, object)
}
