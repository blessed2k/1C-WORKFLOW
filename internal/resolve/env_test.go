package resolve

import (
	"sync"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// TestEnvConcurrentBuiltinLookup — Env разделяется между воркерами
// инкремента (доккомент lookupBuiltin): много горутин одновременно резолвят
// platform-имена через один и тот же Env. Без мьютекса вокруг кэша builtin
// это гонка на map — тест существует, чтобы go test -race имел что ловить,
// а не проходил зелёным только потому, что ничего не выполнялось параллельно.
func TestEnvConcurrentBuiltinLookup(t *testing.T) {
	builtins := loadBuiltins(t)
	env := mustEnv(t, EnvInput{Component: testComponent}, builtins)

	names := []string{"СтрНайти", "Сообщить", "ТекущаяДата", "НачатьТранзакцию", "СтрШаблон"}
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				name := names[(g+i)%len(names)]
				raw := RawRef{Form: FormUnqualified, NameNorm: domain.NormalizeName(name), NameDisplay: name,
					CallerModulePath: "CommonModules/X/Ext/Module.bsl"}
				res := Resolve(raw, env)
				if res.Resolution != domain.ResolutionResolved || res.TargetClass != domain.TargetPlatform {
					t.Errorf("goroutine %d: %s -> resolution=%s targetClass=%s, ожидался resolved/platform",
						g, name, res.Resolution, res.TargetClass)
				}
			}
		}(g)
	}
	wg.Wait()
}
