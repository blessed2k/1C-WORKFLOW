package bsl

import "github.com/blessed2k/1C-WORKFLOW/internal/domain"

// Коллекции менеджеров 1С (как они пишутся в коде, русское и английское имя)
// и каталоги выгрузки берутся из общего словаря видов domain.MetaKinds.
// Для парсера это синтаксический словарь; проверка того, что объект с таким
// именем существует, остаётся за резолвером.

// lookupCollection возвращает канонический тип объекта метаданных (каталог
// коллекции выгрузки, "Catalogs") по имени коллекции менеджеров, как оно
// написано в коде.
func lookupCollection(name string) (string, bool) {
	k, ok := domain.MetaKindByCollection(name)
	return k.DumpDir, ok
}

// isRegisterCollection: у коллекции есть наборы записей и менеджеры записи.
func isRegisterCollection(dir string) bool {
	k, ok := domain.MetaKindByDumpDir(dir)
	return ok && k.IsRegister
}
