package main

import (
	"broken/internal/app"
	"broken/internal/effective"
	"broken/internal/index"
	"broken/internal/parse/bsl"
	"broken/internal/resolve"
	"broken/internal/store"
)

// Serve тянет хранилище, парсер, резолвер, индекс и наложение слоёв
// расширений напрямую, минуя app.
func Serve() {
	_ = app.Lookup
	_ = effective.StoreSource
	_ = store.Row{}
	_ = bsl.Parse
	_ = resolve.NewEnv
	_ = index.Reindex
}
