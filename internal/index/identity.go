package index

import (
	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/meta"
)

// Идентичность узлов индекса (архитектура §14/§15): строки, которыми store
// адресует свой node.identity_key. Собраны в одном месте, чтобы EnvInput
// (identity объектов/членов передаётся насквозь через ObjectRef.IdentityKey/
// MemberRef.IdentityKey) и publish (сама вставка через store.WriteTx)
// всегда считали identity_key одинаково.

func moduleIdentityKey(component domain.ComponentID, modulePath string) string {
	return string(component) + "\x00module\x00" + domain.NormalizeModulePath(modulePath)
}

func symbolIdentityKey(uid domain.SymbolUID) string {
	return string(uid)
}

func metadataObjectIdentityKey(component domain.ComponentID, mtype, nameNorm string) string {
	return string(component) + "\x00object\x00" + mtype + "\x00" + nameNorm
}

func metadataMemberIdentityKey(objectKey string, m meta.MetadataMemberFact) string {
	return objectKey + "\x00member\x00" + m.Kind + "\x00" + m.ParentNorm + "\x00" + m.NameNorm
}

func formIdentityKey(component domain.ComponentID, formKey string) string {
	return string(component) + "\x00form\x00" + formKey
}
