package index

import (
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
)

// resolveFile строит и разрешает RawRef одного модуля против env.
func resolveFile(env resolve.Env, mod *bsl.Module, relPath string) []resolvedRef {
	raws := resolve.BuildRawRefs(relPath, mod)
	if len(raws) == 0 {
		return nil
	}
	out := make([]resolvedRef, 0, len(raws))
	for _, raw := range raws {
		out = append(out, resolvedRef{raw: raw, result: resolve.Resolve(raw, env)})
	}
	return out
}

// resolveAndUpdate разрешает relPaths (все BSL-файлы корпуса, если relPaths
// nil) против env и обновляет corpus.resolved/keyIndex через setResolved.
// Возвращает relPath-ы, чей исход разрешения РЕАЛЬНО изменился — именно они
// нуждаются в republish (18.4: «для refs вне affected set Env не изменился —
// результат совпадает с clean rebuild», значит вне возвращённого множества
// ничего пересчитывать не нужно).
func resolveAndUpdate(env resolve.Env, corpus *componentCorpus, relPaths []string) []string {
	if relPaths == nil {
		relPaths = sortedFileKeys(corpus)
	}
	var changed []string
	for _, rel := range relPaths {
		rec := corpus.files[rel]
		if rec == nil || rec.bslModule == nil {
			if _, had := corpus.resolved[rel]; had {
				corpus.dropResolved(rel)
			}
			continue
		}
		newRefs := resolveFile(env, rec.bslModule, rel)
		oldRefs := corpus.resolved[rel]
		if changedResolution(oldRefs, newRefs) {
			changed = append(changed, rel)
		}
		corpus.setResolved(rel, newRefs)
	}
	return changed
}

// changedResolution сообщает, различаются ли два набора результатов
// разрешения одного файла по исходу (не по порядку внутри файла — Resolve
// детерминирован над одним и тем же Env, поэтому порядок стабилен).
func changedResolution(a, b []resolvedRef) bool {
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i].result.Resolution != b[i].result.Resolution ||
			a[i].result.TargetClass != b[i].result.TargetClass ||
			a[i].result.TargetUID != b[i].result.TargetUID ||
			a[i].result.TargetKey != b[i].result.TargetKey ||
			a[i].result.Confidence != b[i].result.Confidence ||
			a[i].result.CallKind != b[i].result.CallKind {
			return true
		}
	}
	return false
}
