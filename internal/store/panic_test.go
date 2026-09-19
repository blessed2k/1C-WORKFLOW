package store

import (
	"context"
	"testing"
)

// Паника вызывающего не должна выводить хранилище из строя: транзакция
// откатывается, соединение писателя остаётся пригодным, и следующая запись
// проходит. Иначе одна паника в парсере превращала бы каждую последующую запись
// в «cannot start a transaction within a transaction» до перезапуска сервера.
func TestWriteSurvivesPanicOfCaller(t *testing.T) {
	s, f := seeded(t)
	ctx := context.Background()

	func() {
		defer func() {
			if p := recover(); p == nil {
				t.Fatal("паника не долетела до вызывающего")
			}
		}()
		s.Write(ctx, func(tx *WriteTx) error { //nolint:errcheck // паника вместо возврата
			if _, err := tx.InsertSymbol(Symbol{
				IdentityKey: "symbol:cfg:CommonModules/X/п", ComponentID: fxComponent,
				UID: "uid-п", ModuleID: f.moduleID, OriginFileID: f.fileModuleBSL,
				Kind: "procedure", NameNorm: "п", NameDisplay: "П",
			}); err != nil {
				return err
			}
			panic("сбой парсера")
		})
	}()

	if n := countRows(t, s, "symbol", "name_norm='п'"); n != 0 {
		t.Error("символ из аварийной транзакции выжил")
	}
	// Кэш подготовленных выражений живёт одну транзакцию и закрывается и на
	// панике: иначе выражения пережили бы свой ROLLBACK.
	if s.writer.stmts != nil {
		t.Error("кэш подготовленных выражений пережил панику вызывающего")
	}
	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.SetMeta("после-паники", "1")
	}); err != nil {
		t.Fatalf("после паники запись невозможна: %v", err)
	}
	assertValid(t, s, "после паники в write-транзакции")
}

// То же для чтения: соединение с незакрытой транзакцией не возвращается в пул,
// а уничтожается и заменяется — пул сохраняет размер и продолжает работать.
func TestReadSurvivesPanicOfCaller(t *testing.T) {
	s := openTestStore(t, Options{ReaderPoolSize: 2})
	func() {
		defer func() {
			if p := recover(); p == nil {
				t.Fatal("паника не долетела до вызывающего")
			}
		}()
		s.Read(context.Background(), func(tx *ReadTx) error { //nolint:errcheck // паника вместо возврата
			if _, err := tx.GenerationNumber(); err != nil {
				return err
			}
			panic("сбой инструмента")
		})
	}()

	if got := s.readers.Stats().Discarded; got != 1 {
		t.Errorf("уничтожено соединений %d, ожидалось 1", got)
	}
	for i := 0; i < 3; i++ {
		if err := s.Read(context.Background(), func(tx *ReadTx) error {
			_, err := tx.GenerationNumber()
			return err
		}); err != nil {
			t.Fatalf("чтение %d после паники: %v", i+1, err)
		}
	}
	if got := s.readers.Stats().Size; got != 2 {
		t.Errorf("размер пула после замены %d, ожидался 2", got)
	}
}
