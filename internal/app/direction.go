package app

import (
	"fmt"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// directionWord: одно слово direction и его смысл в обоих графах:
// граф вызовов (trace_call_graph) понимает callers|callees, объектный граф
// (object_graph, graph-режим) понимает in|out|both. Слова одного графа
// принимаются другим как синонимы: callers = in, callees = out. Пустой call
// означает, что слово графу вызовов не подходит (both).
type directionWord struct {
	call   string
	object string
}

// directionWords: единственная таблица направлений; пустое слово даёт
// умолчание каждого графа.
var directionWords = map[string]directionWord{
	"":                      {call: "callees", object: store.EdgeDirectionBoth},
	"callers":               {call: "callers", object: store.EdgeDirectionIn},
	"callees":               {call: "callees", object: store.EdgeDirectionOut},
	store.EdgeDirectionIn:   {call: "callers", object: store.EdgeDirectionIn},
	store.EdgeDirectionOut:  {call: "callees", object: store.EdgeDirectionOut},
	store.EdgeDirectionBoth: {object: store.EdgeDirectionBoth},
}

// normalizeCallDirection разбирает direction графа вызовов. Неизвестное
// значение даёт ошибку ввода с перечнем допустимых.
func normalizeCallDirection(raw string) (string, *Error) {
	if w, ok := directionWords[strings.ToLower(strings.TrimSpace(raw))]; ok && w.call != "" {
		return w.call, nil
	}
	return "", unknownDirection(raw, "допустимо callers или callees (синонимы: in = callers, out = callees)")
}

// normalizeDirection разбирает direction объектного графа.
func normalizeDirection(raw string) (string, *Error) {
	if w, ok := directionWords[strings.ToLower(strings.TrimSpace(raw))]; ok {
		return w.object, nil
	}
	return "", unknownDirection(raw, "допустимо in, out или both (синонимы: callers = in, callees = out)")
}

func unknownDirection(raw, hint string) *Error {
	return NewError(CodeInvalidArgument, fmt.Sprintf("direction %q неизвестен", raw), hint)
}
