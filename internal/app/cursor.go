package app

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// cursorPayload: то, что кодирует opaque-курсор (архитектура
// §21): поколение, позиция ключа сортировки, hash параметров фильтра. Hash
// здесь — просто сама каноническая строка параметров, собранная вызывающим
// сервисом (например "kind=procedure&component=cfg"): сравнение строк с тем
// же результатом, что hash, без лишней зависимости.
type cursorPayload struct {
	Gen    domain.Generation `json:"g"`
	Key    string            `json:"k"`
	Params string            `json:"p"`
}

// EncodeCursor упаковывает курсор одной страницы: поколение, из которого
// вызывающий сервис читал (совпадает с ответом при неизменном индексе),
// позицию ключа сортировки, на которой остановились, и paramsKey — те же
// параметры фильтра, что были у ЭТОГО вызова (сервис отвечает за то, чтобы
// одинаковые параметры давали одинаковый paramsKey).
func EncodeCursor(gen domain.Generation, key, paramsKey string) string {
	data, err := json.Marshal(cursorPayload{Gen: gen, Key: key, Params: paramsKey})
	if err != nil {
		// cursorPayload — три строки, json.Marshal на них не падает никогда;
		// паника здесь означала бы баг в самом типе, а не во входных данных.
		panic(fmt.Sprintf("app: encode cursor: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(data)
}

// DecodeCursor разбирает курсор и проверяет его против ТЕКУЩЕГО вызова:
// generation обязан совпасть с currentGen (после инкремента курсор мёртв),
// а paramsKey обязан совпасть с параметрами этого вызова (курсор с
// чужими параметрами отклоняется). Оба случая: один и тот же
// код cursor_expired: разница внутри не меняет того, что вызывающему делать —
// начать пагинацию заново.
//
// cursor=="" — не ошибка: это первая страница, возвращается пустой key.
func DecodeCursor(cursor string, currentGen domain.Generation, paramsKey string) (string, *Error) {
	if cursor == "" {
		return "", nil
	}
	expired := func(message string) *Error {
		return NewError(CodeCursorExpired, message, "начните пагинацию заново без cursor").
			WithGeneration(currentGen)
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", expired("курсор повреждён и не разбирается")
	}
	var p cursorPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return "", expired("курсор повреждён и не разбирается")
	}
	if p.Gen != currentGen {
		return "", expired(fmt.Sprintf("курсор построен для поколения %s, текущее поколение — %s", p.Gen, currentGen))
	}
	if p.Params != paramsKey {
		return "", expired("курсор построен для других параметров вызова (другой фильтр/лимит)")
	}
	return p.Key, nil
}
