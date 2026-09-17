package resolve

import (
	"broken/internal/app"
	"broken/internal/store"
)

// Derive сам открывает базу и берёт транзакцию — это уже не «типы и
// константы», а добыча данных: резолвер обязан работать поверх фактов,
// переданных ему в памяти.
func Derive(path string) {
	db, _ := store.Open(path)
	tx := store.ReadTx{}
	_, _ = db, tx
	_ = app.Lookup
}
