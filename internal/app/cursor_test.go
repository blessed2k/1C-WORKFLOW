package app

import (
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// TestCursorRoundTrip: страница, закодированная EncodeCursor, декодируется
// DecodeCursor обратно в тот же ключ сортировки при тех же generation и
// параметрах — это то, что делает пагинацию вообще возможной.
func TestCursorRoundTrip(t *testing.T) {
	gen := domain.NewGeneration(1, 7)
	cur := EncodeCursor(gen, "symbol:ПолучитьЦену", "kind=procedure")
	key, err := DecodeCursor(cur, gen, "kind=procedure")
	if err != nil {
		t.Fatalf("DecodeCursor: %v", err)
	}
	if key != "symbol:ПолучитьЦену" {
		t.Fatalf("key = %q, want %q", key, "symbol:ПолучитьЦену")
	}
}

// TestCursorEmptyIsFirstPage: пустой курсор — не ошибка, это первая страница.
func TestCursorEmptyIsFirstPage(t *testing.T) {
	key, err := DecodeCursor("", domain.NewGeneration(1, 1), "kind=procedure")
	if err != nil {
		t.Fatalf("DecodeCursor(\"\") = %v, want nil error", err)
	}
	if key != "" {
		t.Fatalf("key = %q, want \"\"", key)
	}
}

// TestCursorExpiredAfterGenerationChange: курсор валиден, пока его
// generation текущий; после инкремента (смена generation) — cursor_expired.
func TestCursorExpiredAfterGenerationChange(t *testing.T) {
	cur := EncodeCursor(domain.NewGeneration(1, 7), "k1", "q=x")
	_, err := DecodeCursor(cur, domain.NewGeneration(1, 8), "q=x")
	if err == nil {
		t.Fatal("DecodeCursor после смены generation должен вернуть ошибку")
	}
	if err.Code != CodeCursorExpired {
		t.Fatalf("code = %s, want %s", err.Code, CodeCursorExpired)
	}
	if err.Generation != domain.NewGeneration(1, 8) {
		t.Errorf("ошибка должна нести ТЕКУЩЕЕ generation (e1.g8), получено %s", err.Generation)
	}
}

// TestCursorExpiredOnForeignParams: «cursor с чужими
// параметрами отклоняется». Тот же generation, другой фильтр — тоже
// cursor_expired, а не тихая подмена страницы чужого запроса.
func TestCursorExpiredOnForeignParams(t *testing.T) {
	gen := domain.NewGeneration(1, 7)
	cur := EncodeCursor(gen, "k1", "q=x")
	_, err := DecodeCursor(cur, gen, "q=y")
	if err == nil {
		t.Fatal("DecodeCursor с чужими параметрами должен вернуть ошибку")
	}
	if err.Code != CodeCursorExpired {
		t.Fatalf("code = %s, want %s", err.Code, CodeCursorExpired)
	}
}

// TestCursorExpiredOnGarbage: произвольная строка на месте курсора — тоже
// cursor_expired, а не паника и не 500-я на транспорте.
func TestCursorExpiredOnGarbage(t *testing.T) {
	_, err := DecodeCursor("не-курсор-совсем", domain.NewGeneration(1, 1), "")
	if err == nil {
		t.Fatal("DecodeCursor(мусор) должен вернуть ошибку")
	}
	if err.Code != CodeCursorExpired {
		t.Fatalf("code = %s, want %s", err.Code, CodeCursorExpired)
	}
}
