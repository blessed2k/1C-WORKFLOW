package resolve

import (
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
)

// RegisterAccessResult — один доступ к регистру (store.RegisterAccess без
// file_id/symbol_id — их знает только вызывающий, у него источник файла и
// владеющий символ). Mode различает read/write/movement/clear ровно так,
// как их уже различил bsl.RegisterAccess (парсер); резолвер
// добавляет только object_id через поиск объекта метаданных по имени.
type RegisterAccessResult struct {
	RegisterNameNorm string
	Mode             bsl.RegisterMode
	Kind             bsl.RegisterAccessKind
	Static           bool
	Confidence       domain.Confidence
	Span             domain.Span

	// ObjectKey/ObjectResolved — identity_key объекта метаданных (регистра),
	// если он нашёлся в Env.Objects; ObjectResolved=false — object_id
	// остаётся NULL (soft target, §15), доступ к регистру от этого не
	// теряется.
	ObjectKey      string
	ObjectResolved bool
}

// DeriveRegisterAccess переводит bsl.Module.RegisterAccesses в результаты с
// разрешённым object_id. Движения.X (MetaType == "") не называет вид
// регистра — резолвер ищет объект среди всех регистровых MType
// (registerMTypes); при неоднозначности (совпадение имени в двух видах
// регистров сразу) объект остаётся неразрешённым, а не угадывается.
func DeriveRegisterAccess(mod *bsl.Module, env Env) []RegisterAccessResult {
	if mod == nil {
		return nil
	}
	out := make([]RegisterAccessResult, 0, len(mod.RegisterAccesses))
	for _, ra := range mod.RegisterAccesses {
		res := RegisterAccessResult{
			RegisterNameNorm: domain.NormalizeName(mod.Name(ra.NameSpan)),
			Mode:             ra.Mode,
			Kind:             ra.Kind,
			Static:           ra.Static,
			Confidence:       ra.Confidence,
			Span:             ra.Span,
		}
		var obj ObjectRef
		var ok bool
		if ra.MetaType == "" {
			obj, ok = env.ObjectByAnyRegisterKind(res.RegisterNameNorm)
		} else if mtype, known := bslCollectionToMType(ra.MetaType); known {
			obj, ok = env.Object(mtype, res.RegisterNameNorm)
		}
		if ok {
			res.ObjectKey = obj.IdentityKey
			res.ObjectResolved = true
		}
		out = append(out, res)
	}
	return out
}
