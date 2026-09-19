package graphweb

import (
	"embed"
	"net/http"
)

// spaFS: HTML-страница и JS-приложение SPA: один
// HTML с инлайновым CSS, один JS-файл. Оба пишутся руками, без node-
// тулчейна, поэтому не нуждаются в отдельной директории vendor.
//
//go:embed assets/index.html assets/app.js
var spaFS embed.FS

// vendorFS — cytoscape.js версии 3.34.1, лицензия MIT.
//
// Источник: https://cdn.jsdelivr.net/npm/cytoscape@3.34.1/dist/cytoscape.min.js
// Скачано 20.08.2026 с указанного адреса. SHA-256
// файла и полный текст лицензии — assets/vendor/cytoscape-LICENSE.txt рядом.
// Тест TestVendoredCytoscapeIsRealLibrary (assets_test.go) сверяет размер,
// версию в шапке файла и текст лицензии — не позволяет молча подменить
// вендоренный файл заглушкой.
//
//go:embed assets/vendor/cytoscape.min.js assets/vendor/cytoscape-LICENSE.txt
var vendorFS embed.FS

// serveEmbedded отдаёт один файл встроенной ФС с фиксированным Content-Type
// (embed.FS не знает MIME по расширению так, как это делает http.FileServer
// для ОС-файлов — тип задаём явно, единообразно с writeJSON в errors.go).
func serveEmbedded(fs embed.FS, name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(name)
		if err != nil {
			http.Error(w, "asset not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Write(data)
	}
}

// registerAssets вешает на mux маршруты раздела 8.1, отвечающие за отдачу
// SPA (GET / -> SPA, GET /assets/cytoscape.min.js): статика,
// без обращения к ObjectGraphService и без параметра project — это ровно
// то, что отличает их от маршрутов api/*, зарегистрированных в NewHandler.
func registerAssets(mux *http.ServeMux) {
	mux.HandleFunc("GET /", serveEmbedded(spaFS, "assets/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /app.js", serveEmbedded(spaFS, "assets/app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /assets/cytoscape.min.js", serveEmbedded(vendorFS, "assets/vendor/cytoscape.min.js", "text/javascript; charset=utf-8"))
}
