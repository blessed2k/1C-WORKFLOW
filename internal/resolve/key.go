package resolve

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// KeyHash — хэш ключа разрешения (архитектура §18.4). Формат непрозрачен для
// потребителей: сравнивается только на равенство, хранится строкой в
// resolution_dep.key_hash.
type KeyHash string

// ScopeKind — вид области поиска имени, часть состава ключа разрешения
// (§18.4/§19.1): локальный модуль, глобальные общие модули (как единое
// пространство), экспорт конкретного общего модуля, менеджерный модуль
// объекта, platform builtins.
type ScopeKind string

const (
	// ScopeLocalModule — символы модуля, в котором стоит ссылка.
	ScopeLocalModule ScopeKind = "local-module"
	// ScopeGlobalCommon — экспортные имена ВСЕХ глобальных (Global=true)
	// общих модулей компонента как одно пространство имён: появление нового
	// глобального модуля с тем же экспортным именем обязано переразрешить
	// все ссылки, консультировавшие этот ключ, поэтому владелец в ключе не
	// участвует.
	ScopeGlobalCommon ScopeKind = "global-common"
	// ScopeModuleExport — квалифицированный вызов конкретного общего модуля Q.
	ScopeModuleExport ScopeKind = "module-export"
	// ScopeManagerModule — менеджерный модуль объекта метаданных O.
	ScopeManagerModule ScopeKind = "manager-module"
	// ScopePlatform — глобальный контекст платформы (internal/syntax).
	ScopePlatform ScopeKind = "platform"
)

// resolutionKey — состав ключа разрешения (§18.4): (component_id,
// layer/view, вид области, владелец области, qualifier_norm, name_norm).
// Owner заполняется только у ScopeModuleExport (имя общего модуля) и
// ScopeManagerModule (mtype+имя объекта метаданных); у ScopeLocalModule
// Owner — путь модуля (identity локального модуля, не имя ссылки); у
// ScopeGlobalCommon и ScopePlatform Owner пуст — это ключи всего компонента.
type resolutionKey struct {
	Component domain.ComponentID
	Layer     domain.Layer
	Scope     ScopeKind
	Owner     string
	Qualifier string
	Name      string
}

// Hash считает стабильный хэш ключа. Составляющие разделяются нулевым
// байтом — он не встречается в нормализованных именах и путях 1С, поэтому
// разные разбиения полей не могут дать одну и ту же строку под хеш (тот же
// приём, что и domain.NewSymbolUID).
func (k resolutionKey) Hash() KeyHash {
	h := sha256.New()
	parts := []string{
		string(k.Component),
		string(k.Layer.Component),
		itoa(k.Layer.ApplyOrder),
		string(k.Scope),
		k.Owner,
		k.Qualifier,
		k.Name,
	}
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return KeyHash(hex.EncodeToString(sum[:16]))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
