package index

import "time"

// StageTiming — длительность одного этапа конвейера Reindex. Порядок в
// срезе Result.Stages — порядок появления этапа в коде (discover раньше
// parse раньше publish), не алфавитный: так его удобно читать человеку.
type StageTiming struct {
	Name       string
	DurationMS int64
}

// stageAccum копит длительность именованных этапов за один вызов Reindex.
// Прогон по нескольким компонентам манифеста (comp == "") суммирует
// одноимённые этапы по всем обработанным компонентам в одну строку — это
// картина «куда уходит время всего прогона», не разбивка на каждый
// компонент отдельно (в реестре проектов сегодня нет ни одного с более чем
// одним компонентом, различать их выводом было бы решением без потребителя).
type stageAccum struct {
	order []string
	total map[string]time.Duration
}

func newStageAccum() *stageAccum {
	return &stageAccum{total: make(map[string]time.Duration)}
}

// add копит длительность под именем этапа. Порядок появления имени
// фиксируется при первой записи — вывод stages() детерминирован, не зависит
// от итерации map.
func (a *stageAccum) add(name string, d time.Duration) {
	if _, ok := a.total[name]; !ok {
		a.order = append(a.order, name)
	}
	a.total[name] += d
}

// mark — сахар для типичного вызова add(name, now().Sub(since)); now — та
// же инъецируемая точка времени, что и у остального Service (§Config.Now),
// чтобы сложение с общим Duration не зависело от смешения двух часов.
func (a *stageAccum) mark(now func() time.Time, name string, since time.Time) {
	a.add(name, now().Sub(since))
}

// stages отдаёт накопленное в порядке появления, плюс синтетический
// "commit" — остаток общей длительности reindex сверх суммы измеренных
// этапов. В commit попадает всё, что происходит вокруг runComponent внутри
// store.Write/store.Rebuild, но не внутри него самого: открытие транзакции,
// проверка миграции схемы, а после цикла по компонентам — blob GC,
// orphan-sweep, генерация нового поколения, физическая фиксация. Отдельным
// этапом это не измеряется: internal/store — код на пути целостности эпох
// (см. CLAUDE.md, «Восстановление эпох…»), и вставлять в него счётчики ради
// одного этого прогона: не тот случай, где стоит трогать внутренности.
//
// commit считается из уже округлённых до мс значений остальных этапов, а не
// из точной суммы time.Duration: независимое усечение каждого этапа в
// Milliseconds() теряет свою долю миллисекунды у каждого — при семи этапах
// это до 6 мс расхождения, если считать остаток от точной суммы, а не от
// уже округлённой. commit забирает ровно эту потерю на себя, поэтому сумма
// DurationMS всех строк, включая commit, всегда равна total.Milliseconds()
// ровно — не приближённо.
func (a *stageAccum) stages(total time.Duration) []StageTiming {
	out := make([]StageTiming, 0, len(a.order)+1)
	var sumMS int64
	for _, name := range a.order {
		ms := a.total[name].Milliseconds()
		sumMS += ms
		out = append(out, StageTiming{Name: name, DurationMS: ms})
	}
	commitMS := total.Milliseconds() - sumMS
	if commitMS < 0 {
		// Не должно случаться: сумма усечённых частей не может превысить
		// усечение целого. Отрицательного здесь не бывает при исправном
		// коде, но неотрицательное поле в MCP-ответе дешевле, чем гонка за
		// доказательством этого в каждой точке вызова.
		commitMS = 0
	}
	out = append(out, StageTiming{Name: "commit", DurationMS: commitMS})
	return out
}
