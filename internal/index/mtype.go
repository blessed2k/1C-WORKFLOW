package index

import "github.com/blessed2k/1C-WORKFLOW/internal/domain"

// ownerTypeToMType переводит имя папки-коллекции выгрузки во множественном
// числе (bsl.ModuleInfo.OwnerType: "Catalogs", "Documents", ...) в MType
// объекта метаданных в единственном числе (parse/meta.MetadataObjectFact.MType:
// "Catalog", "Document", ...). Словарь один на все слои, domain.MetaKinds;
// индекс берёт из него только виды с признаком ModuleOwner. Регистр каталога
// учитывается: OwnerType приходит так, как его пишет выгрузка.
func ownerTypeToMType(ownerType string) (string, bool) {
	k, ok := domain.MetaKindByDumpDir(ownerType)
	if !ok || !k.ModuleOwner {
		return "", false
	}
	return k.MType, true
}
