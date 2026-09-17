package source

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Ключи подобраны так, чтобы каждый запрос ловился ровно одной подстрокой:
// scriptedRunner обходит map, а обход map в Go случайный, поэтому пересекающиеся
// ключи дали бы тест, который иногда зелёный.
const (
	keySettings  = "ИспользоватьУправлениеДоступом"
	keyList      = "ЧислоРолей УБЫВ"
	keyHead      = "И НЕ Профиль.ПометкаУдаления"
	keyRoles     = "Идент.Имя КАК Роль"
	keyKinds     = "ТЧВиды"
	keyGroups    = "КАК Участник"
	keyValues    = "ТЧЗначения"
	keyUserChain = "ТЧСостав"
	keyUserExist = "Справочник.Пользователи КАК Пользователи"
)

func rows(list ...map[string]any) *QueryResult {
	return &QueryResult{Rows: list, Count: len(list)}
}

func settingsRow(management, recordLevel bool) *QueryResult {
	return rows(map[string]any{"УправлениеДоступом": management, "ПоЗаписям": recordLevel})
}

func roleRows(names ...string) *QueryResult {
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"Роль": n})
	}
	return rows(out...)
}

// TestRecordLevelOffIsReported — ради этого инструмент во многом и делался: в базе,
// где ограничение на уровне записей выключено, искать условие RLS бессмысленно, а
// именно на это ушли часы в одной из сессий.
func TestRecordLevelOffIsReported(t *testing.T) {
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		keySettings: settingsRow(true, false),
		keyList:     rows(map[string]any{"Профиль": "Пользователь", "ЧислоРолей": float64(597)}),
	}}

	rep, err := AccessProfiles(context.Background(), runner, AccessProfileOptions{})
	if err != nil {
		t.Fatalf("AccessProfiles: %v", err)
	}
	if rep.Settings.RecordLevel {
		t.Fatal("ограничение по записям должно быть выключено")
	}
	if !strings.Contains(rep.Note, "выключено") {
		t.Fatalf("ожидалось предупреждение про выключенный RLS, получено %q", rep.Note)
	}
	if len(rep.Profiles) != 1 || rep.Profiles[0].RoleCount != 597 {
		t.Fatalf("неверный список профилей: %+v", rep.Profiles)
	}
}

func TestRecordLevelOnHasNoNote(t *testing.T) {
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		keySettings: settingsRow(true, true),
		keyList:     rows(),
	}}

	rep, err := AccessProfiles(context.Background(), runner, AccessProfileOptions{})
	if err != nil {
		t.Fatalf("AccessProfiles: %v", err)
	}
	if rep.Note != "" {
		t.Fatalf("при включённом RLS предупреждения быть не должно, получено %q", rep.Note)
	}
}

func TestProfileDiffSeparatesRoles(t *testing.T) {
	calls := 0
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		keySettings: settingsRow(true, false),
	}}
	// Роли обоих профилей приходят одним и тем же запросом, различаются параметром,
	// поэтому подменяем ответ по счётчику вызовов.
	runner.answers[keyRoles] = roleRows("Общая", "ТолькоA")
	rep, err := AccessProfiles(context.Background(), &diffRunner{
		base:  runner,
		first: roleRows("Общая", "ТолькоA"),
		next:  roleRows("Общая", "ТолькоB1", "ТолькoB2"),
		calls: &calls,
	}, AccessProfileOptions{Diff: []string{"РукСТО", "Кладовщик"}})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if rep.Diff == nil {
		t.Fatal("diff не заполнен")
	}
	if got := rep.Diff.OnlyA; len(got) != 1 || got[0] != "ТолькоA" {
		t.Fatalf("OnlyA = %v", got)
	}
	if len(rep.Diff.OnlyB) != 2 {
		t.Fatalf("OnlyB = %v", rep.Diff.OnlyB)
	}
	if rep.Diff.Common != 1 {
		t.Fatalf("Common = %d, ожидалась 1", rep.Diff.Common)
	}
}

// diffRunner отдаёт разные наборы ролей на первый и последующие вызовы запроса ролей.
type diffRunner struct {
	base        *scriptedRunner
	first, next *QueryResult
	calls       *int
}

func (d *diffRunner) ExecuteQuery(ctx context.Context, p QueryParams) (*QueryResult, error) {
	if strings.Contains(p.Text, keyRoles) {
		*d.calls++
		if *d.calls == 1 {
			return d.first, nil
		}
		return d.next, nil
	}
	return d.base.ExecuteQuery(ctx, p)
}

// TestUserReachedOnlyViaUserGroup — участник входит в группу доступа не строкой
// «Пользователи», а через группу пользователей. Пропуск этого пути даёт ответ
// «у пользователя нет профиля» там, где профиль есть: ложное срабатывание, которое
// отправляет чинить исправное.
func TestUserReachedOnlyViaUserGroup(t *testing.T) {
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		keySettings:  settingsRow(true, false),
		keyUserExist: rows(map[string]any{"Наименование": "Тест Кладовщик"}),
		keyUserChain: rows(map[string]any{"Группа": "ТЕСТ_Кладовщик", "Профиль": "ТЕСТ_Кладовщик"}),
		keyRoles:     roleRows("БазовыеПрава", "СкладскиеОперации"),
	}}

	rep, err := AccessProfiles(context.Background(), runner, AccessProfileOptions{User: "Тест Кладовщик"})
	if err != nil {
		t.Fatalf("userAccess: %v", err)
	}
	u := rep.User
	if u == nil || !u.Found {
		t.Fatalf("пользователь должен быть найден: %+v", u)
	}
	if len(u.Profiles) != 1 || u.Profiles[0] != "ТЕСТ_Кладовщик" {
		t.Fatalf("профили = %v", u.Profiles)
	}
	if len(u.Roles) != 2 {
		t.Fatalf("роли = %v", u.Roles)
	}
	if u.Note != "" {
		t.Fatalf("лишнее примечание: %q", u.Note)
	}
}

// TestUnknownUserIsNotSilentlyEmpty — несуществующее имя обязано отличаться от
// «есть, но без групп»: иначе опечатка в имени читается как отсутствие прав.
func TestUnknownUserIsNotSilentlyEmpty(t *testing.T) {
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		keySettings:  settingsRow(true, false),
		keyUserExist: rows(),
		keyUserChain: rows(),
	}}

	rep, err := AccessProfiles(context.Background(), runner, AccessProfileOptions{User: "Тест Опечатка"})
	if err != nil {
		t.Fatalf("userAccess: %v", err)
	}
	if rep.User.Found {
		t.Fatal("пользователя не существует, Found должен быть false")
	}
	if !strings.Contains(rep.User.Note, "не найден") {
		t.Fatalf("примечание не объясняет причину: %q", rep.User.Note)
	}
	if len(rep.User.Roles) != 0 {
		t.Fatalf("у несуществующего пользователя не может быть ролей: %v", rep.User.Roles)
	}
}

func TestUserWithoutAccessGroups(t *testing.T) {
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		keySettings:  settingsRow(true, false),
		keyUserExist: rows(map[string]any{"Наименование": "Новичок"}),
		keyUserChain: rows(),
	}}

	rep, err := AccessProfiles(context.Background(), runner, AccessProfileOptions{User: "Новичок"})
	if err != nil {
		t.Fatalf("userAccess: %v", err)
	}
	if !rep.User.Found {
		t.Fatal("пользователь есть в справочнике")
	}
	if !strings.Contains(rep.User.Note, "не входит ни в одну группу") {
		t.Fatalf("примечание = %q", rep.User.Note)
	}
}

// TestProfileDetailCapsRoles — 600 ролей в ответе съедают контекст сессии,
// ради чего в этой порции и дозировали вывод.
func TestProfileDetailCapsRoles(t *testing.T) {
	many := make([]string, 0, 120)
	for i := 0; i < 120; i++ {
		many = append(many, fmt.Sprintf("Роль%03d", i))
	}
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		keySettings: settingsRow(true, false),
		keyHead:     rows(map[string]any{"Профиль": "ТЕСТ_РукСТО", "Поставляемый": "00000000-0000-0000-0000-000000000000"}),
		keyRoles:    roleRows(many...),
		keyKinds:    rows(map[string]any{"ВидДоступа": "Организация", "ВсеРазрешены": true}),
		keyGroups:   rows(map[string]any{"Группа": "ТЕСТ_РукСТО", "Участник": "Тест РукСТО"}),
		keyValues:   rows(),
	}}

	rep, err := AccessProfiles(context.Background(), runner, AccessProfileOptions{Profile: "ТЕСТ_РукСТО"})
	if err != nil {
		t.Fatalf("profileDetail: %v", err)
	}
	p := rep.Profiles[0]
	if p.RoleCount != 120 {
		t.Fatalf("RoleCount = %d, ожидалось 120", p.RoleCount)
	}
	if len(p.Roles) != maxRolesShown {
		t.Fatalf("показано ролей %d, ожидалось %d", len(p.Roles), maxRolesShown)
	}
	if !strings.Contains(p.RolesNote, "includeRoles") {
		t.Fatalf("примечание не подсказывает, как получить полный список: %q", p.RolesNote)
	}
	if p.Supplied {
		t.Fatal("нулевой идентификатор поставляемых данных не делает профиль поставляемым")
	}
	// Вид доступа берётся из ТИПЗНАЧЕНИЯ: это ИМЯ ТИПА пустой ссылки, а не
	// представление элемента, которое у пустой ссылки всегда пустое.
	if len(p.AccessKinds) != 1 || p.AccessKinds[0].Kind != "Организация" {
		t.Fatalf("виды доступа = %+v", p.AccessKinds)
	}
	if len(p.Groups) != 1 || len(p.Groups[0].Members) != 1 {
		t.Fatalf("группы = %+v", p.Groups)
	}
}

func TestProfileDetailFullRoleList(t *testing.T) {
	many := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		many = append(many, fmt.Sprintf("Роль%03d", i))
	}
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		keySettings: settingsRow(true, false),
		keyHead:     rows(map[string]any{"Профиль": "П", "Поставляемый": "9c1f1e64-0000-4000-8000-000000000001"}),
		keyRoles:    roleRows(many...),
		keyKinds:    rows(),
		keyGroups:   rows(),
		keyValues:   rows(),
	}}

	rep, err := AccessProfiles(context.Background(), runner, AccessProfileOptions{Profile: "П", IncludeRoles: true})
	if err != nil {
		t.Fatalf("profileDetail: %v", err)
	}
	if len(rep.Profiles[0].Roles) != 60 {
		t.Fatalf("при includeRoles=true должен прийти весь список, пришло %d", len(rep.Profiles[0].Roles))
	}
	if !rep.Profiles[0].Supplied {
		t.Fatal("заполненный идентификатор означает поставляемый профиль")
	}
}

func TestMissingProfileIsAnError(t *testing.T) {
	runner := &scriptedRunner{answers: map[string]*QueryResult{
		keySettings: settingsRow(true, false),
		keyHead:     rows(),
	}}

	_, err := AccessProfiles(context.Background(), runner, AccessProfileOptions{Profile: "Нет такого"})
	if err == nil {
		t.Fatal("ожидалась ошибка про ненайденный профиль")
	}
	if !strings.Contains(err.Error(), "не найден") {
		t.Fatalf("ошибка не объясняет причину: %v", err)
	}
}

func TestIsFilledUUID(t *testing.T) {
	cases := map[string]bool{
		"":                                     false,
		"00000000-0000-0000-0000-000000000000": false,
		"9c1f1e64-0000-4000-8000-000000000001": true,
	}
	for in, want := range cases {
		if got := isFilledUUID(in); got != want {
			t.Errorf("isFilledUUID(%q) = %v, ожидалось %v", in, got, want)
		}
	}
}
