package main

import (
	"reflect"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

// TestBuildCards: карточка привязывается к методу по id входа, пробелы в
// формулировках приводятся к одному, метод без ответа и метод с пустым
// ответом считаются оставшимися без карточки.
func TestBuildCards(t *testing.T) {
	inputs := []cardIn{
		{ID: methodID("М.Второй"), Call: "М.Второй"},
		{ID: methodID("М.Первый"), Call: "М.Первый"},
		{ID: methodID("М.Пропущен"), Call: "М.Пропущен"},
		{ID: methodID("М.Пустой"), Call: "М.Пустой"},
	}
	outputs := []cardOut{
		{ID: methodID("М.Первый"), Tasks: []string{"  убрать   дубли из массива ", ""}, Instead: " цикл  с проверкой "},
		{ID: methodID("М.Второй"), Tasks: []string{"массив без повторов"}},
		{ID: methodID("М.Пустой"), Tasks: []string{" "}},
	}
	cards, missing, err := buildCards(inputs, outputs)
	if err != nil {
		t.Fatalf("buildCards: %v", err)
	}
	want := []app.APICard{
		{Call: "М.Второй", Tasks: []string{"массив без повторов"}},
		{Call: "М.Первый", Tasks: []string{"убрать дубли из массива"}, Instead: "цикл с проверкой"},
	}
	if !reflect.DeepEqual(cards, want) || missing != 2 {
		t.Errorf("карточки = %+v, без карточки %d; want %+v и 2", cards, missing, want)
	}
}

// TestBuildCardsRejectsForeignAnswers: ответ на метод, которого нет во входе,
// и повторный ответ останавливают сборку.
func TestBuildCardsRejectsForeignAnswers(t *testing.T) {
	inputs := []cardIn{{ID: methodID("М.Метод"), Call: "М.Метод"}}
	for name, outputs := range map[string][]cardOut{
		"чужой метод":     {{ID: "m00000000", Tasks: []string{"что-то"}}},
		"повторный ответ": {{ID: methodID("М.Метод"), Tasks: []string{"а"}}, {ID: methodID("М.Метод"), Tasks: []string{"б"}}},
	} {
		if _, _, err := buildCards(inputs, outputs); err == nil {
			t.Errorf("%s: сборка прошла без ошибки", name)
		}
	}
}

// TestMethodID: идентификатор метода не зависит от регистра выражения вызова
// и различает методы.
func TestMethodID(t *testing.T) {
	if methodID("ОбщегоНазначения.Метод") != methodID("общегоназначения.метод") {
		t.Errorf("идентификатор зависит от регистра")
	}
	if methodID("М.Первый") == methodID("М.Второй") {
		t.Errorf("разные методы получили один идентификатор")
	}
}
