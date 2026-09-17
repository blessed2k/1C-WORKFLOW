package graphweb

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/blessed2k/1C-WORKFLOW/internal/app"
)

// Коды ошибок, которых нет в app.ErrorCode: они чисто транспортные (плохой
// путь/запрос, неоднозначный или неизвестный проект), поэтому заведены
// здесь, а не в internal/app. app.ErrorCode — это `type ErrorCode string`
// (internal/app/errors.go), новые значения того же типа заводить можно —
// конверт ошибки (код/сообщение/подсказка) при этом остаётся ровно тем же,
// что у всех остальных инструментов индексного слоя, и это единственная
// причина переиспользовать *app.Error, а не заводить свой тип конверта.
const (
	codeBadRequest       app.ErrorCode = "bad_request"
	codeUnknownProject   app.ErrorCode = "unknown_project"
	codeAmbiguousProject app.ErrorCode = "ambiguous_project"
	codeInternal         app.ErrorCode = "internal"
)

// badRequest — конструктор ошибки для плохого запроса транспорта (не
// найденный/не разобранный путь или query-параметр).
func badRequest(message, hint string) *app.Error {
	return app.NewError(codeBadRequest, message, hint)
}

// statusForCode переводит machine-код actionable-ошибки в HTTP-статус.
// Разделение «объект не найден» (404) от «объект есть, ответ просто пуст»
// (200, пустые items) держится на том, что второе — вообще не ошибка: методы
// ObjectGraphService отдают его как обычный успешный Response (см.
// TestNeighborsEmptyVsNotFound в handler_test.go).
func statusForCode(code app.ErrorCode) int {
	switch code {
	case app.CodeNotFound:
		return http.StatusNotFound
	case app.CodeNoActiveProject, app.CodeIndexNotFresh:
		return http.StatusServiceUnavailable
	case app.CodeCursorExpired, app.CodeResourceExpired:
		return http.StatusGone
	case app.CodePathOutsideWorkspace, app.CodeComponentNotRegistered,
		app.CodeInvalidArgument,
		codeBadRequest, codeUnknownProject, codeAmbiguousProject:
		return http.StatusBadRequest
	default:
		return http.StatusBadRequest
	}
}

// respondErr сериализует err как *app.Error (конверт code/message/hint —
// тот же, что читает MCP-клиент из err.Error(), см. doc-комментарий
// app.Error) и подбирает HTTP-статус. Ошибка, не являющаяся *app.Error, —
// такого сегодня не бывает на пути ObjectGraphService (все его отказы —
// *app.Error, internal/app/errors.go), но обёртка не должна паниковать,
// если это когда-нибудь перестанет быть так, поэтому 500 и codeInternal —
// честный запасной путь, а не «не должно случиться».
func respondErr(w http.ResponseWriter, err error) {
	var aerr *app.Error
	if !errors.As(err, &aerr) {
		aerr = app.NewError(codeInternal, err.Error(), "")
		writeJSON(w, http.StatusInternalServerError, aerr)
		return
	}
	writeJSON(w, statusForCode(aerr.Code), aerr)
}

// writeJSON — единственная точка сериализации ответа: и успешных
// app.Response[T], и ошибок *app.Error проходят через неё, поэтому оба
// несут одинаковый Content-Type и одинаковую обработку сбоя кодирования.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
