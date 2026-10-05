package source

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// Профили групп доступа живут в базе, а не в конфигурации: rights_audit разбирает
// поставляемые профили из кода УправлениеДоступомУТ и про заведённые руками не знает
// ничего. Этот файл закрывает вторую половину — состав профилей как данные.
//
// Три вещи, на которых здесь ломаются запросы в лоб, и все три стоили по сессии:
//
//  1. Алиас таблицы табличной части не может совпадать с именем самой части:
//     «Справочник.ПрофилиГруппДоступа.Роли КАК Роли» даёт «Неоднозначное поле Роли.Ссылка».
//     Отсюда префикс ТЧ у всех алиасов ниже.
//  2. Роль в ТЧ — это ссылка на ИдентификаторыОбъектовМетаданных, а не строка;
//     без соединения с этим справочником вместо имён приходят представления ссылок.
//  3. ВидДоступа — ПУСТАЯ ссылка, и вид доступа закодирован её ТИПОМ. Представление
//     всегда пустое, поэтому «ВЫБРАТЬ ВидДоступа» возвращает столбец пустых строк и
//     выглядит как сломанный запрос. Нужен ТИПЗНАЧЕНИЯ.

// AccessKind — вид доступа профиля или группы. Kind это имя типа пустой ссылки
// («Организация», «Склад»), а не наименование элемента.
type AccessKind struct {
	Kind       string `json:"kind" jsonschema:"вид доступа: тип значения, например Организация"`
	AllAllowed bool   `json:"allAllowed" jsonschema:"установлен признак ВсеРазрешены"`
	Preset     bool   `json:"preset,omitempty" jsonschema:"вид доступа предустановлен профилем"`
}

// AccessValue — конкретное значение доступа группы (значимо только при включённом
// ограничении на уровне записей).
type AccessValue struct {
	Kind  string `json:"kind" jsonschema:"вид доступа"`
	Value string `json:"value" jsonschema:"значение доступа"`
}

// AccessGroup — группа доступа, через которую профиль достаётся пользователям.
type AccessGroup struct {
	Name    string        `json:"group"`
	Members []string      `json:"members,omitempty" jsonschema:"участники: пользователи и группы пользователей"`
	Values  []AccessValue `json:"values,omitempty" jsonschema:"значения доступа группы"`
}

// LiveAccessProfile — профиль как запись базы. Отличается от AccessProfile из
// accessprofiles.go: тот описывает ПОСТАВЛЯЕМЫЙ профиль, разобранный из кода
// УправлениеДоступомУТ, этот — то, что реально заведено в базе.
type LiveAccessProfile struct {
	Name        string        `json:"profile"`
	Comment     string        `json:"comment,omitempty"`
	Supplied    bool          `json:"supplied" jsonschema:"профиль поставляемый (заполнен идентификатор поставляемых данных)"`
	Changed     bool          `json:"changed,omitempty" jsonschema:"поставляемый профиль изменён вручную"`
	RoleCount   int           `json:"roleCount"`
	Roles       []string      `json:"roles,omitempty" jsonschema:"имена ролей; отдаются только по одному профилю"`
	RolesNote   string        `json:"rolesNote,omitempty"`
	AccessKinds []AccessKind  `json:"accessKinds,omitempty"`
	Groups      []AccessGroup `json:"groups,omitempty"`
}

// ProfileDiff — сравнение двух профилей по составу ролей.
type ProfileDiff struct {
	A       string   `json:"a"`
	B       string   `json:"b"`
	OnlyA   []string `json:"onlyA" jsonschema:"роли, которые есть только в A"`
	OnlyB   []string `json:"onlyB" jsonschema:"роли, которые есть только в B"`
	Common  int      `json:"common" jsonschema:"сколько ролей совпадает"`
	Summary string   `json:"summary"`
}

// UserAccess — что пользователь получает через группы доступа.
type UserAccess struct {
	User     string   `json:"user"`
	Found    bool     `json:"found" jsonschema:"пользователь найден в справочнике"`
	Groups   []string `json:"groups,omitempty"`
	Profiles []string `json:"profiles,omitempty"`
	Roles    []string `json:"roles,omitempty" jsonschema:"объединение ролей всех профилей"`
	Note     string   `json:"note,omitempty"`
}

// AccessSettings — глобальные переключатели подсистемы. Без них ответы про RLS
// вводят в заблуждение: искать условие ограничения в базе, где ограничение на
// уровне записей выключено целиком, можно очень долго.
type AccessSettings struct {
	AccessManagement bool `json:"accessManagement" jsonschema:"ИспользоватьУправлениеДоступом"`
	RecordLevel      bool `json:"recordLevel" jsonschema:"ОграничиватьДоступНаУровнеЗаписей"`
	// Note говорит, что часть переключателей прочитать не удалось и почему.
	Note string `json:"note,omitempty"`
}

// AccessProfileReport — ответ инструмента access_profiles.
type AccessProfileReport struct {
	Settings AccessSettings      `json:"settings"`
	Profiles []LiveAccessProfile `json:"profiles,omitempty"`
	Diff     *ProfileDiff        `json:"diff,omitempty"`
	User     *UserAccess         `json:"user,omitempty"`
	Note     string              `json:"note,omitempty"`
}

// AccessProfileOptions — режим работы. Пустые опции дают список профилей.
type AccessProfileOptions struct {
	Profile      string
	User         string
	Diff         []string
	IncludeRoles bool
}

const maxRolesShown = 40

// AccessProfiles обслуживает все режимы одного инструмента: список, один профиль,
// сверка по пользователю, diff двух профилей.
func AccessProfiles(ctx context.Context, run queryRunner, opts AccessProfileOptions) (*AccessProfileReport, error) {
	settings, err := accessSettings(ctx, run)
	if err != nil {
		return nil, err
	}
	report := &AccessProfileReport{Settings: settings}
	if !settings.RecordLevel {
		report.Note = "ограничение доступа на уровне записей выключено: значения доступа и RLS ни на что не влияют, доступ определяют только роли профилей"
	}

	switch {
	case len(opts.Diff) == 2:
		diff, err := profileDiff(ctx, run, opts.Diff[0], opts.Diff[1])
		if err != nil {
			return nil, err
		}
		report.Diff = diff
		return report, nil

	case strings.TrimSpace(opts.User) != "":
		user, err := userAccess(ctx, run, strings.TrimSpace(opts.User))
		if err != nil {
			return nil, err
		}
		report.User = user
		return report, nil

	case strings.TrimSpace(opts.Profile) != "":
		profile, err := profileDetail(ctx, run, strings.TrimSpace(opts.Profile), opts.IncludeRoles)
		if err != nil {
			return nil, err
		}
		report.Profiles = []LiveAccessProfile{*profile}
		return report, nil

	default:
		list, err := profileList(ctx, run)
		if err != nil {
			return nil, err
		}
		report.Profiles = list
		return report, nil
	}
}

func accessSettings(ctx context.Context, run queryRunner) (AccessSettings, error) {
	res, err := run.ExecuteQuery(ctx, QueryParams{Text: `ВЫБРАТЬ
	Константы.ИспользоватьУправлениеДоступом КАК УправлениеДоступом,
	Константы.ОграничиватьДоступНаУровнеЗаписей КАК ПоЗаписям
ИЗ
	Константы КАК Константы`})
	if queryRejected(err) {
		// Константы ИспользоватьУправлениеДоступом нет во многих конфигурациях (в «Бухгалтерии
		// предприятия» тоже), а запрос с несуществующей константой падает целиком. Без неё
		// инструмент отвечал ошибкой в каждой такой базе.
		return recordLevelOnly(ctx, run)
	}
	if err != nil {
		return AccessSettings{}, fmt.Errorf("настройки подсистемы прав: %w", err)
	}
	if len(res.Rows) == 0 {
		return AccessSettings{}, nil
	}
	row := res.Rows[0]
	return AccessSettings{
		AccessManagement: asBool(row["УправлениеДоступом"]),
		RecordLevel:      asBool(row["ПоЗаписям"]),
	}, nil
}

// recordLevelOnly читает единственный переключатель, который есть в любой конфигурации с
// подсистемой управления доступом.
func recordLevelOnly(ctx context.Context, run queryRunner) (AccessSettings, error) {
	res, err := run.ExecuteQuery(ctx, QueryParams{Text: `ВЫБРАТЬ
	Константы.ОграничиватьДоступНаУровнеЗаписей КАК ПоЗаписям
ИЗ
	Константы КАК Константы`})
	if err != nil {
		return AccessSettings{}, fmt.Errorf("настройки подсистемы прав: %w", err)
	}
	settings := AccessSettings{Note: "константы ИспользоватьУправлениеДоступом в конфигурации нет, accessManagement не прочитан"}
	if len(res.Rows) > 0 {
		settings.RecordLevel = asBool(res.Rows[0]["ПоЗаписям"])
	}
	return settings, nil
}

// queryRejected сообщает, что базу спросили, а она отвергла сам текст запроса: коннектор
// ответил query_failed либо, если он старше конверта ошибок, кодом 400. Недоступная база и
// прерванный по времени запрос сюда не относятся.
func queryRejected(err error) bool {
	var failure *ConnectorError
	if !errors.As(err, &failure) {
		return false
	}
	if failure.Code != "" {
		return failure.Code == "query_failed"
	}
	return failure.Status == http.StatusBadRequest
}

func profileList(ctx context.Context, run queryRunner) ([]LiveAccessProfile, error) {
	res, err := run.ExecuteQuery(ctx, QueryParams{Limit: 500, Text: `ВЫБРАТЬ
	Профиль.Наименование КАК Профиль,
	Профиль.Комментарий КАК Комментарий,
	Профиль.ПоставляемыйПрофильИзменен КАК Изменен,
	Профиль.ИдентификаторПоставляемыхДанных КАК Поставляемый,
	КОЛИЧЕСТВО(ТЧРоли.Роль) КАК ЧислоРолей
ИЗ
	Справочник.ПрофилиГруппДоступа КАК Профиль
	ЛЕВОЕ СОЕДИНЕНИЕ Справочник.ПрофилиГруппДоступа.Роли КАК ТЧРоли
	ПО ТЧРоли.Ссылка = Профиль.Ссылка
ГДЕ
	НЕ Профиль.ПометкаУдаления
СГРУППИРОВАТЬ ПО
	Профиль.Наименование,
	Профиль.Комментарий,
	Профиль.ПоставляемыйПрофильИзменен,
	Профиль.ИдентификаторПоставляемыхДанных
УПОРЯДОЧИТЬ ПО
	ЧислоРолей УБЫВ`})
	if err != nil {
		return nil, fmt.Errorf("список профилей: %w", err)
	}
	out := make([]LiveAccessProfile, 0, len(res.Rows))
	for _, row := range res.Rows {
		out = append(out, LiveAccessProfile{
			Name:      asText(row["Профиль"]),
			Comment:   asText(row["Комментарий"]),
			Changed:   asBool(row["Изменен"]),
			Supplied:  isFilledUUID(asText(row["Поставляемый"])),
			RoleCount: int(asInt(row["ЧислоРолей"])),
		})
	}
	return out, nil
}

func profileDetail(ctx context.Context, run queryRunner, name string, includeRoles bool) (*LiveAccessProfile, error) {
	head, err := run.ExecuteQuery(ctx, QueryParams{
		Params: map[string]any{"ИмяПрофиля": name},
		Text: `ВЫБРАТЬ
	Профиль.Наименование КАК Профиль,
	Профиль.Комментарий КАК Комментарий,
	Профиль.ПоставляемыйПрофильИзменен КАК Изменен,
	Профиль.ИдентификаторПоставляемыхДанных КАК Поставляемый
ИЗ
	Справочник.ПрофилиГруппДоступа КАК Профиль
ГДЕ
	Профиль.Наименование = &ИмяПрофиля
	И НЕ Профиль.ПометкаУдаления`,
	})
	if err != nil {
		return nil, fmt.Errorf("профиль %q: %w", name, err)
	}
	if len(head.Rows) == 0 {
		return nil, fmt.Errorf("профиль %q в базе не найден: проверьте наименование, список даёт вызов без аргументов", name)
	}

	row := head.Rows[0]
	profile := &LiveAccessProfile{
		Name:     asText(row["Профиль"]),
		Comment:  asText(row["Комментарий"]),
		Changed:  asBool(row["Изменен"]),
		Supplied: isFilledUUID(asText(row["Поставляемый"])),
	}

	roles, err := profileRoles(ctx, run, name)
	if err != nil {
		return nil, err
	}
	profile.RoleCount = len(roles)
	if includeRoles || len(roles) <= maxRolesShown {
		profile.Roles = roles
	} else {
		profile.Roles = roles[:maxRolesShown]
		profile.RolesNote = fmt.Sprintf("показано %d из %d: полный список по includeRoles=true", maxRolesShown, len(roles))
	}

	if profile.AccessKinds, err = profileKinds(ctx, run, name); err != nil {
		return nil, err
	}
	if profile.Groups, err = profileGroups(ctx, run, name); err != nil {
		return nil, err
	}
	return profile, nil
}

// profileRoles возвращает имена ролей профиля. Соединение с идентификаторами
// обязательно: в ТЧ лежит ссылка, а не имя.
func profileRoles(ctx context.Context, run queryRunner, name string) ([]string, error) {
	res, err := run.ExecuteQuery(ctx, QueryParams{
		Limit:  5000,
		Params: map[string]any{"ИмяПрофиля": name},
		Text: `ВЫБРАТЬ
	Идент.Имя КАК Роль
ИЗ
	Справочник.ПрофилиГруппДоступа КАК Профиль
	ВНУТРЕННЕЕ СОЕДИНЕНИЕ Справочник.ПрофилиГруппДоступа.Роли КАК ТЧРоли
	ПО ТЧРоли.Ссылка = Профиль.Ссылка
	ЛЕВОЕ СОЕДИНЕНИЕ Справочник.ИдентификаторыОбъектовМетаданных КАК Идент
	ПО ТЧРоли.Роль = Идент.Ссылка
ГДЕ
	Профиль.Наименование = &ИмяПрофиля
УПОРЯДОЧИТЬ ПО
	Роль`,
	})
	if err != nil {
		return nil, fmt.Errorf("роли профиля %q: %w", name, err)
	}
	out := make([]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		if role := asText(row["Роль"]); role != "" {
			out = append(out, role)
		}
	}
	return out, nil
}

func profileKinds(ctx context.Context, run queryRunner, name string) ([]AccessKind, error) {
	res, err := run.ExecuteQuery(ctx, QueryParams{
		Limit:  200,
		Params: map[string]any{"ИмяПрофиля": name},
		Text: `ВЫБРАТЬ
	ТИПЗНАЧЕНИЯ(ТЧВиды.ВидДоступа) КАК ВидДоступа,
	ТЧВиды.ВсеРазрешены КАК ВсеРазрешены,
	ТЧВиды.Предустановленный КАК Предустановленный
ИЗ
	Справочник.ПрофилиГруппДоступа КАК Профиль
	ВНУТРЕННЕЕ СОЕДИНЕНИЕ Справочник.ПрофилиГруппДоступа.ВидыДоступа КАК ТЧВиды
	ПО ТЧВиды.Ссылка = Профиль.Ссылка
ГДЕ
	Профиль.Наименование = &ИмяПрофиля`,
	})
	if err != nil {
		return nil, fmt.Errorf("виды доступа профиля %q: %w", name, err)
	}
	out := make([]AccessKind, 0, len(res.Rows))
	for _, row := range res.Rows {
		out = append(out, AccessKind{
			Kind:       asText(row["ВидДоступа"]),
			AllAllowed: asBool(row["ВсеРазрешены"]),
			Preset:     asBool(row["Предустановленный"]),
		})
	}
	return out, nil
}

// profileGroups собирает группы доступа профиля вместе с участниками и значениями.
// Участник попадает в группу тремя путями, и пропуск любого даёт ложное
// «у пользователя нет этого профиля»: реквизитом Пользователь у личной группы
// поставляемого профиля, строкой ТЧ Пользователи и через группу пользователей.
func profileGroups(ctx context.Context, run queryRunner, name string) ([]AccessGroup, error) {
	res, err := run.ExecuteQuery(ctx, QueryParams{
		Limit:  2000,
		Params: map[string]any{"ИмяПрофиля": name},
		Text: `ВЫБРАТЬ
	Группа.Наименование КАК Группа,
	ПРЕДСТАВЛЕНИЕ(Группа.Пользователь) КАК Личный,
	ПРЕДСТАВЛЕНИЕ(ТЧПольз.Пользователь) КАК Участник
ИЗ
	Справочник.ГруппыДоступа КАК Группа
	ЛЕВОЕ СОЕДИНЕНИЕ Справочник.ГруппыДоступа.Пользователи КАК ТЧПольз
	ПО ТЧПольз.Ссылка = Группа.Ссылка
ГДЕ
	Группа.Профиль.Наименование = &ИмяПрофиля
	И НЕ Группа.ПометкаУдаления
УПОРЯДОЧИТЬ ПО
	Группа`,
	})
	if err != nil {
		return nil, fmt.Errorf("группы доступа профиля %q: %w", name, err)
	}

	order := []string{}
	byName := map[string]*AccessGroup{}
	for _, row := range res.Rows {
		group := asText(row["Группа"])
		if group == "" {
			continue
		}
		g, ok := byName[group]
		if !ok {
			g = &AccessGroup{Name: group}
			byName[group] = g
			order = append(order, group)
		}
		for _, member := range []string{asText(row["Личный"]), asText(row["Участник"])} {
			if member != "" && !hasString(g.Members, member) {
				g.Members = append(g.Members, member)
			}
		}
	}

	values, err := groupValues(ctx, run, name)
	if err != nil {
		return nil, err
	}
	out := make([]AccessGroup, 0, len(order))
	for _, group := range order {
		g := byName[group]
		g.Values = values[group]
		out = append(out, *g)
	}
	return out, nil
}

func groupValues(ctx context.Context, run queryRunner, name string) (map[string][]AccessValue, error) {
	res, err := run.ExecuteQuery(ctx, QueryParams{
		Limit:  2000,
		Params: map[string]any{"ИмяПрофиля": name},
		Text: `ВЫБРАТЬ
	Группа.Наименование КАК Группа,
	ТИПЗНАЧЕНИЯ(ТЧЗначения.ВидДоступа) КАК ВидДоступа,
	ПРЕДСТАВЛЕНИЕ(ТЧЗначения.ЗначениеДоступа) КАК Значение
ИЗ
	Справочник.ГруппыДоступа КАК Группа
	ВНУТРЕННЕЕ СОЕДИНЕНИЕ Справочник.ГруппыДоступа.ЗначенияДоступа КАК ТЧЗначения
	ПО ТЧЗначения.Ссылка = Группа.Ссылка
ГДЕ
	Группа.Профиль.Наименование = &ИмяПрофиля
	И НЕ Группа.ПометкаУдаления`,
	})
	if err != nil {
		return nil, fmt.Errorf("значения доступа профиля %q: %w", name, err)
	}
	out := map[string][]AccessValue{}
	for _, row := range res.Rows {
		group := asText(row["Группа"])
		out[group] = append(out[group], AccessValue{
			Kind:  asText(row["ВидДоступа"]),
			Value: asText(row["Значение"]),
		})
	}
	return out, nil
}

func profileDiff(ctx context.Context, run queryRunner, a, b string) (*ProfileDiff, error) {
	rolesA, err := profileRoles(ctx, run, a)
	if err != nil {
		return nil, err
	}
	rolesB, err := profileRoles(ctx, run, b)
	if err != nil {
		return nil, err
	}
	if len(rolesA) == 0 {
		return nil, fmt.Errorf("профиль %q не найден или без ролей", a)
	}
	if len(rolesB) == 0 {
		return nil, fmt.Errorf("профиль %q не найден или без ролей", b)
	}

	setB := toSet(rolesB)
	setA := toSet(rolesA)
	diff := &ProfileDiff{A: a, B: b}
	for _, role := range rolesA {
		if _, ok := setB[role]; !ok {
			diff.OnlyA = append(diff.OnlyA, role)
		} else {
			diff.Common++
		}
	}
	for _, role := range rolesB {
		if _, ok := setA[role]; !ok {
			diff.OnlyB = append(diff.OnlyB, role)
		}
	}
	sort.Strings(diff.OnlyA)
	sort.Strings(diff.OnlyB)
	diff.Summary = fmt.Sprintf("%s: %d ролей, %s: %d ролей, общих %d, только в %s %d, только в %s %d",
		a, len(rolesA), b, len(rolesB), diff.Common, a, len(diff.OnlyA), b, len(diff.OnlyB))
	return diff, nil
}

// userAccess отвечает, что пользователь получает через группы доступа. Учитываются
// все три пути вхождения в группу.
func userAccess(ctx context.Context, run queryRunner, name string) (*UserAccess, error) {
	res, err := run.ExecuteQuery(ctx, QueryParams{
		Limit:  500,
		Params: map[string]any{"ИмяПользователя": name},
		Text: `ВЫБРАТЬ РАЗЛИЧНЫЕ
	Группа.Наименование КАК Группа,
	Группа.Профиль.Наименование КАК Профиль
ИЗ
	Справочник.ГруппыДоступа КАК Группа
	ЛЕВОЕ СОЕДИНЕНИЕ Справочник.ГруппыДоступа.Пользователи КАК ТЧПольз
	ПО ТЧПольз.Ссылка = Группа.Ссылка
	ЛЕВОЕ СОЕДИНЕНИЕ Справочник.ГруппыПользователей.Состав КАК ТЧСостав
	ПО ТЧСостав.Ссылка = ТЧПольз.Пользователь
ГДЕ
	НЕ Группа.ПометкаУдаления
	И (Группа.Пользователь.Наименование = &ИмяПользователя
		ИЛИ ТЧПольз.Пользователь.Наименование = &ИмяПользователя
		ИЛИ ТЧСостав.Пользователь.Наименование = &ИмяПользователя)
УПОРЯДОЧИТЬ ПО
	Профиль`,
	})
	if err != nil {
		return nil, fmt.Errorf("группы пользователя %q: %w", name, err)
	}

	out := &UserAccess{User: name}
	exists, err := userExists(ctx, run, name)
	if err != nil {
		return nil, err
	}
	out.Found = exists
	if !exists {
		out.Note = "пользователь с таким наименованием не найден в справочнике Пользователи: проверьте написание"
		return out, nil
	}

	for _, row := range res.Rows {
		if group := asText(row["Группа"]); group != "" && !hasString(out.Groups, group) {
			out.Groups = append(out.Groups, group)
		}
		if profile := asText(row["Профиль"]); profile != "" && !hasString(out.Profiles, profile) {
			out.Profiles = append(out.Profiles, profile)
		}
	}
	if len(out.Profiles) == 0 {
		out.Note = "пользователь есть в справочнике, но не входит ни в одну группу доступа: прав сверх встроенных у него нет"
		return out, nil
	}

	roles := map[string]struct{}{}
	for _, profile := range out.Profiles {
		list, err := profileRoles(ctx, run, profile)
		if err != nil {
			return nil, err
		}
		for _, role := range list {
			roles[role] = struct{}{}
		}
	}
	out.Roles = sortedKeys(roles)
	return out, nil
}

func userExists(ctx context.Context, run queryRunner, name string) (bool, error) {
	res, err := run.ExecuteQuery(ctx, QueryParams{
		Limit:  1,
		Params: map[string]any{"ИмяПользователя": name},
		Text: `ВЫБРАТЬ ПЕРВЫЕ 1
	Пользователи.Наименование КАК Наименование
ИЗ
	Справочник.Пользователи КАК Пользователи
ГДЕ
	Пользователи.Наименование = &ИмяПользователя`,
	})
	if err != nil {
		return false, fmt.Errorf("поиск пользователя %q: %w", name, err)
	}
	return len(res.Rows) > 0, nil
}

func asBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(b, "true") || b == "Да"
	}
	return false
}

// isFilledUUID отличает заполненный идентификатор поставляемых данных от нулевого.
func isFilledUUID(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	return strings.Trim(s, "0-") != ""
}

func hasString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func toSet(list []string) map[string]struct{} {
	out := make(map[string]struct{}, len(list))
	for _, item := range list {
		out[item] = struct{}{}
	}
	return out
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// AccessProfiles реализует точку входа со стороны живого источника.
func (s *HTTPSource) AccessProfiles(ctx context.Context, opts AccessProfileOptions) (*AccessProfileReport, error) {
	return AccessProfiles(ctx, s, opts)
}
