package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeCards(t *testing.T, dir, name string, cards ...APICard) {
	t.Helper()
	if err := WriteAPICards(filepath.Join(dir, name), APICardsHeader{Pack: name, Cards: len(cards)}, cards); err != nil {
		t.Fatal(err)
	}
}

// useCards включает карточки из каталога на время теста.
func useCards(t *testing.T, dir string) {
	t.Helper()
	ConfigureAPICards(dir)
	t.Cleanup(func() { ConfigureAPICards("") })
}

// TestFindAPIUsesCards: метод находится по формулировке задачи из карточки,
// слов которой нет ни в его имени, ни в комментарии; без карточек тот же
// запрос его не находит.
func TestFindAPIUsesCards(t *testing.T) {
	const query = "шапка печатной формы с адресом компании"
	p, _ := apiDocFixture(t, "nocards")
	if item, _ := findAPIItem(t, p, query); len(item.Other) > 0 && item.Other[0].Call == "ПечатьДокументов.СведенияОЮрФизЛице" {
		t.Fatalf("без карточек метод найден первым: тест ничего не докажет (%q)", apiCalls(item.Other))
	}

	dir := t.TempDir()
	writeCards(t, dir, "test.jsonl", APICard{
		Call:    "печатьдокументов.сведенияОЮрФизЛице",
		Tasks:   []string{"адрес и телефон компании для шапки печатной формы"},
		Instead: "запрос к контактной информации организации",
	})
	useCards(t, dir)
	p, _ = apiDocFixture(t, "cards")
	item, resp := findAPIItem(t, p, query)
	if len(item.Other) == 0 || item.Other[0].Call != "ПечатьДокументов.СведенияОЮрФизЛице" {
		t.Errorf("с карточкой other = %q, want первым ПечатьДокументов.СведенияОЮрФизЛице", apiCalls(item.Other))
	}
	if hasWarning(resp.Warnings, "api_cards_unreadable") {
		t.Errorf("предупреждение о карточках при читаемом файле: %+v", resp.Warnings)
	}
}

// TestFindAPIWithBrokenCards: нечитаемый файл карточек поиск не ломает: ответ
// строится без карточек и говорит об этом.
func TestFindAPIWithBrokenCards(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.jsonl"), []byte("{не json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	useCards(t, dir)
	p, _ := apiDocFixture(t, "broken")
	item, resp := findAPIItem(t, p, "наименование организации")
	if len(item.Other) == 0 || item.Other[0].Call != "ПечатьДокументов.НаименованиеОрганизации" {
		t.Errorf("other = %q, want первым ПечатьДокументов.НаименованиеОрганизации", apiCalls(item.Other))
	}
	if !hasWarning(resp.Warnings, "api_cards_unreadable") {
		t.Errorf("нет предупреждения api_cards_unreadable: %+v", resp.Warnings)
	}
}

// TestLoadAPICards: заголовок файла карточкой не считается, метод из двух
// файлов берётся из первого по имени, каталога нет: карточек нет.
func TestLoadAPICards(t *testing.T) {
	dir := t.TempDir()
	writeCards(t, dir, "a.jsonl", APICard{Call: "Модуль.Метод", Tasks: []string{"первая"}})
	writeCards(t, dir, "b.jsonl", APICard{Call: "модуль.метод", Tasks: []string{"вторая"}}, APICard{Call: "Модуль.Другой", Instead: "цикл"})
	cards, err := LoadAPICards(dir)
	if err != nil {
		t.Fatalf("LoadAPICards: %v", err)
	}
	if len(cards) != 2 || cards["модуль.метод"].Tasks[0] != "первая" || cards["модуль.другой"].Instead != "цикл" {
		t.Errorf("карточки = %+v", cards)
	}
	for _, missing := range []string{"", filepath.Join(dir, "нет-такого")} {
		if cards, err := LoadAPICards(missing); err != nil || len(cards) != 0 {
			t.Errorf("LoadAPICards(%q) = %v, %v; want пусто без ошибки", missing, cards, err)
		}
		if packs, err := APICardPacks(missing); err != nil || len(packs) != 0 {
			t.Errorf("APICardPacks(%q) = %v, %v; want пусто без ошибки", missing, packs, err)
		}
	}
}

// TestAPICardPacks: набор называется заголовком файла, файл без заголовка
// именем файла.
func TestAPICardPacks(t *testing.T) {
	dir := t.TempDir()
	if err := WriteAPICards(filepath.Join(dir, "a.jsonl"), APICardsHeader{Pack: "bsp-3.1", Cards: 1}, []APICard{{Call: "Модуль.Метод"}}); err != nil {
		t.Fatalf("WriteAPICards: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.jsonl"), []byte(`{"call":"Модуль.Другой"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	packs, err := APICardPacks(dir)
	if err != nil {
		t.Fatalf("APICardPacks: %v", err)
	}
	if len(packs) != 2 || packs[0].Pack != "bsp-3.1" || packs[0].Cards != 1 || packs[1].Pack != "b" || packs[1].Cards != 0 {
		t.Errorf("наборы = %+v", packs)
	}
	// Тот же набор с другим содержимым: имя и число те же, отпечаток другой.
	before := packs[0].Digest
	if err := WriteAPICards(filepath.Join(dir, "a.jsonl"), APICardsHeader{Pack: "bsp-3.1", Cards: 1}, []APICard{{Call: "Модуль.Метод", Instead: "цикл"}}); err != nil {
		t.Fatalf("WriteAPICards: %v", err)
	}
	if packs, err = APICardPacks(dir); err != nil || len(before) != 64 || packs[0].Digest == before {
		t.Errorf("отпечаток набора не изменился с содержимым: %q и %q, %v", before, packs[0].Digest, err)
	}
}

// TestFindAPIReportsCards: ответ называет, у скольких методов есть карточка:
// по нему видно, подключены ли карточки и подходят ли они конфигурации.
func TestFindAPIReportsCards(t *testing.T) {
	p, _ := apiDocFixture(t, "cards-none")
	if item, _ := findAPIItem(t, p, "наименование организации"); item.Cards != 0 {
		t.Errorf("без карточек cards = %d", item.Cards)
	}
	dir := t.TempDir()
	writeCards(t, dir, "test.jsonl",
		APICard{Call: "ПечатьДокументов.СведенияОЮрФизЛице", Tasks: []string{"адрес компании"}},
		APICard{Call: "Чужой.Метод", Tasks: []string{"метода нет в конфигурации"}})
	useCards(t, dir)
	p, _ = apiDocFixture(t, "cards-one")
	if item, _ := findAPIItem(t, p, "наименование организации"); item.Cards != 1 {
		t.Errorf("cards = %d, want 1: карточка чужого метода в счёт не идёт", item.Cards)
	}
}

// TestWriteAPICardsLeavesNoTempFile: файл карточек пишется через временный и
// переименование; временного файла в каталоге не остаётся.
func TestWriteAPICardsLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	writeCards(t, dir, "a.jsonl", APICard{Call: "Модуль.Метод"})
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "a.jsonl" {
		t.Errorf("каталог после записи: %v, %v", entries, err)
	}
}

// TestOpenStandaloneRequiresRoot: без каталога проекта отдельный индекс не
// открывается и рабочий каталог не создаётся.
func TestOpenStandaloneRequiresRoot(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "ws")
	if _, _, err := OpenStandalone(context.Background(), StandaloneOptions{Root: "  ", Workspace: ws}); err == nil {
		t.Fatal("OpenStandalone без каталога проекта не вернул ошибку")
	}
	if _, err := os.Stat(ws); !os.IsNotExist(err) {
		t.Errorf("рабочий каталог создан без проекта: %v", err)
	}
}
