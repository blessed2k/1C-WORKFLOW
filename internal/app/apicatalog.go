package app

import (
	"context"
	"sort"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// APICatalogItem: items[0] ответа Catalog: все методы программного интерфейса
// теми же двумя секциями, что у find_api, без запроса и без ранжирования.
type APICatalogItem struct {
	// BSPVersion: версия библиотеки стандартных подсистем из выгрузки.
	BSPVersion string `json:"bspVersion,omitempty"`
	// BSP и Other упорядочены по выражению вызова.
	BSP   []APIMethodItem `json:"bsp"`
	Other []APIMethodItem `json:"other"`
}

// Catalog отдаёт все методы программного интерфейса: тот же отбор, что у
// FindAPI, но без запроса. Устаревшие входят с пометкой, методы
// переопределяемых модулей не входят.
//
// Каталог целиком в ответ инструмента не идёт: это десятки тысяч методов.
// Потребители: оценка качества find_api (evals/findapi), которой нужен полный
// перечень того, что поиск вообще способен вернуть.
func (s *APIService) Catalog(ctx context.Context) (Response[APICatalogItem], error) {
	op, err := s.projects.Active(ctx)
	if err != nil {
		return Response[APICatalogItem]{}, err
	}
	type txResult struct {
		item APICatalogItem
		gen  domain.Generation
		warn []Warning
	}
	res, snap, err := ReadSnapshot(ctx, op, func(tx *store.ReadTx) (txResult, error) {
		var out txResult
		gen, gerr := tx.Generation()
		if gerr != nil {
			return out, gerr
		}
		out.gen = gen

		rows, lib, warn, rerr := readAPIMethods(tx)
		if rerr != nil {
			return out, rerr
		}
		out.item.BSPVersion = lib.version
		out.warn = warn
		out.item.BSP, out.item.Other = []APIMethodItem{}, []APIMethodItem{}
		for _, r := range rows {
			item, ierr := apiMethodItem(tx, newAPICandidate(r))
			if ierr != nil {
				return out, ierr
			}
			if lib.contains(r) {
				out.item.BSP = append(out.item.BSP, item)
			} else {
				out.item.Other = append(out.item.Other, item)
			}
		}
		for _, section := range [][]APIMethodItem{out.item.BSP, out.item.Other} {
			sort.SliceStable(section, func(i, j int) bool { return section[i].Call < section[j].Call })
		}
		return out, nil
	})
	if err != nil {
		return Response[APICatalogItem]{}, err
	}
	resp := Response[APICatalogItem]{Generation: res.gen, Items: []APICatalogItem{res.item}, TotalCount: 1, Warnings: res.warn}
	return withSnapshot(resp, snap), nil
}
