package arch

import (
	"fmt"
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// Имена запретов. Они попадают в текст упавшего теста, поэтому короткие.
const (
	RuleDomainStdlibOnly = "domain-only-stdlib"
	RuleSQLOnlyInStore   = "sql-only-in-store"
	RuleCmdThroughApp    = "cmd-through-app"
	RuleNoLegacyInNew    = "no-legacy-in-new"
	RulePortableCommon   = "portable-common-code"
	RuleRetrieveNotApp   = "retrieve-not-app"
	// RuleResolveStoreSurface — internal/resolve берёт из internal/store
	// только объявленную поверхность (типы фактов и константы схемы) и не
	// зовёт слои, стоящие над ним.
	RuleResolveStoreSurface = "resolve-store-surface"
)

// поверхностьStoreВResolve — то, за чем internal/resolve имеет право ходить в
// internal/store: типы строк-фактов и константы схемы, то есть словарь, а не
// доступ к базе. Атрибуция объектного графа (resolve/objectedges.go, веха В1)
// принимает store.RegisterAccessRow и заполняет kind/provenance ребра
// константами схемы — переписывать те же строковые литералы у себя было бы
// вторым источником правды. Всё остальное в store (Open, ReadTx, WriteTx,
// запросы) для resolve закрыто: пакет обязан оставаться чистой логикой поверх
// фактов, переданных в памяти (D02).
//
// Предел проверки: она смотрит на квалификатор `store.` в исходнике и не
// разбирает алиасы импорта. Тот же класс предела, что у сканера литералов в
// CheckPortability: это внутренний архитектурный гард, а не security-граница.
var поверхностьStoreВResolve = map[string]bool{
	"RegisterAccessRow":      true,
	"EdgeWritesRegister":     true,
	"EdgeReadsRegister":      true,
	"EdgeWritesDeclared":     true,
	"EdgeReadsQuery":         true,
	"EdgeRefAttribute":       true,
	"EdgeCreates":            true,
	"EdgeProvenanceCode":     true,
	"EdgeProvenanceDeclared": true,
}

// надResolve — слои, которые зовут резолвер, а не наоборот: cmd -> app ->
// {retrieve, index, store, resolve}. internal/effective (наложение слоёв
// расширений) тоже зовёт resolve.DeriveIntercepts и стоит над ним.
var надResolve = []string{"internal/app", "internal/index", "internal/retrieve", "internal/effective"}

// новыеПакеты — индексное ядро. Ему нельзя опираться на старый слой.
var новыеПакеты = []string{
	"internal/domain", "internal/store", "internal/parse", "internal/resolve",
	"internal/index", "internal/app", "internal/retrieve", "internal/workspace",
	"internal/graphweb", "internal/effective",
}

// старыйСлой — пакеты, существовавшие до индексного ядра. internal/syntax в
// список не входит намеренно: реестр platform builtins новому коду разрешён
// (граница зафиксирована в interfaces.md).
var старыйСлой = []string{
	"internal/source", "internal/onec", "internal/standards", "internal/validate",
}

// запрещеноВCmd — то, до чего транспорт не дотягивается напрямую: между ними
// обязан стоять internal/app. internal/effective в списке потому, что его
// StoreSource принимает *store.ReadTx: транспорт с ним в руках обходил бы app.
var запрещеноВCmd = []string{"internal/store", "internal/parse", "internal/resolve", "internal/index", "internal/effective"}

// Check прогоняет все запреты разом.
func Check(m *Module) []Violation {
	var нарушения []Violation
	нарушения = append(нарушения, CheckDomainImports(m)...)
	нарушения = append(нарушения, CheckSQLIsolation(m)...)
	нарушения = append(нарушения, CheckCommandLayering(m)...)
	нарушения = append(нарушения, CheckLegacyIsolation(m)...)
	нарушения = append(нарушения, CheckPortability(m)...)
	нарушения = append(нарушения, CheckRetrieveNotApp(m)...)
	нарушения = append(нарушения, CheckResolveStoreSurface(m)...)
	return нарушения
}

// CheckDomainImports: internal/domain не зависит ни от чего, кроме стандартной
// библиотеки. Сущности индекса не должны знать ни про MCP SDK, ни про SQLite —
// иначе инварианты уезжают в транспорт и в хранилище.
func CheckDomainImports(m *Module) []Violation {
	var нарушения []Violation
	for _, пакет := range m.UnderPrefix("internal/domain") {
		for _, файл := range пакет.Files {
			for _, импорт := range файл.Imports {
				if m.Stdlib(импорт) {
					continue
				}
				if rel, свой := m.Rel(импорт); свой && подПрефиксом(rel, "internal/domain") {
					continue
				}
				нарушения = append(нарушения, Violation{
					Rule:    RuleDomainStdlibOnly,
					File:    файл.Rel,
					Import:  импорт,
					Message: "domain обязан обходиться стандартной библиотекой",
				})
			}
		}
	}
	return нарушения
}

// CheckSQLIsolation: SQL и драйвер живут только в internal/store. Запрос,
// написанный мимо store, обходит эпохи, пул читателей и единственного писателя.
func CheckSQLIsolation(m *Module) []Violation {
	var нарушения []Violation
	for i := range m.Packages {
		пакет := &m.Packages[i]
		if подПрефиксом(пакет.Rel, "internal/store") {
			continue
		}
		for _, файл := range пакет.Files {
			for _, импорт := range файл.Imports {
				if !sqlИмпорт(импорт) {
					continue
				}
				нарушения = append(нарушения, Violation{
					Rule:    RuleSQLOnlyInStore,
					File:    файл.Rel,
					Import:  импорт,
					Message: "SQL и драйвер SQLite допустимы только в internal/store",
				})
			}
		}
	}
	return нарушения
}

// CheckCommandLayering: cmd/mcp1c ходит в индекс через internal/app. Прямой
// импорт хранилища, парсеров или резолвера возвращает бизнес-логику в транспорт.
func CheckCommandLayering(m *Module) []Violation {
	var нарушения []Violation
	for _, пакет := range m.UnderPrefix("cmd/mcp1c") {
		for _, файл := range пакет.Files {
			for _, импорт := range файл.Imports {
				rel, свой := m.Rel(импорт)
				if !свой {
					continue
				}
				for _, запрещённый := range запрещеноВCmd {
					if подПрефиксом(rel, запрещённый) {
						нарушения = append(нарушения, Violation{
							Rule:    RuleCmdThroughApp,
							File:    файл.Rel,
							Import:  импорт,
							Message: "транспорт обращается к индексу только через internal/app",
						})
					}
				}
			}
		}
	}
	return нарушения
}

// CheckRetrieveNotApp: internal/retrieve не тянет internal/app. Направление
// зависимостей — cmd -> app -> {retrieve, index, store, resolve}: app зовёт
// retrieve (get_context_for_task), не наоборот. Обратный импорт создал бы
// цикл на app, если он когда-нибудь начнёт звать retrieve напрямую, и
// нарушил бы правило «retrieve — библиотека поверх resolve/store, не поверх
// оркестрации» (найдено ревью при работе над D08 — effective-слияние в
// retrieve обязано строиться на internal/resolve, не копировать app/effective.go
// и не импортировать его; гард на это раньше не был заведён).
func CheckRetrieveNotApp(m *Module) []Violation {
	var нарушения []Violation
	for _, пакет := range m.UnderPrefix("internal/retrieve") {
		for _, файл := range пакет.Files {
			for _, импорт := range файл.Imports {
				rel, свой := m.Rel(импорт)
				if !свой {
					continue
				}
				if подПрефиксом(rel, "internal/app") {
					нарушения = append(нарушения, Violation{
						Rule:    RuleRetrieveNotApp,
						File:    файл.Rel,
						Import:  импорт,
						Message: "internal/retrieve не должен зависеть от internal/app — направление зависимостей обратное",
					})
				}
			}
		}
	}
	return нарушения
}

// CheckResolveStoreSurface: internal/resolve остаётся чистой логикой поверх
// фактов в памяти. Импорт internal/store ему разрешён, но ровно за
// поверхностью поверхностьStoreВResolve — типами фактов и константами схемы;
// любое другое обращение к store (открыть базу, взять транзакцию, сходить
// запросом) означает, что резолвер начал сам добывать данные, и направление
// зависимостей поехало. Импорты слоёв надResolve запрещены целиком.
//
// Правило заведено ревью таска 06 вехи В1: до него единственным основанием
// нового ребра resolve -> store был комментарий в doc.go, то есть вопрос
// вкуса, а не красный тест.
func CheckResolveStoreSurface(m *Module) []Violation {
	var нарушения []Violation
	for _, пакет := range m.UnderPrefix("internal/resolve") {
		for _, файл := range пакет.Files {
			storeИмпортирован := false
			for _, импорт := range файл.Imports {
				rel, свой := m.Rel(импорт)
				if !свой {
					continue
				}
				if подПрефиксом(rel, "internal/store") {
					storeИмпортирован = true
					continue
				}
				for _, верхний := range надResolve {
					if подПрефиксом(rel, верхний) {
						нарушения = append(нарушения, Violation{
							Rule:    RuleResolveStoreSurface,
							File:    файл.Rel,
							Import:  импорт,
							Message: "resolve не зависит от слоёв, которые его зовут: направление cmd -> app -> {index, retrieve, store, resolve}",
						})
					}
				}
			}
			if !storeИмпортирован {
				continue
			}
			нарушения = append(нарушения, обращенияКStore(файл)...)
		}
	}
	return нарушения
}

// обращенияКStore ловит всё, что resolve берёт из store сверх объявленной
// поверхности.
func обращенияКStore(файл File) []Violation {
	var нарушения []Violation
	ast.Inspect(файл.syntax, func(узел ast.Node) bool {
		селектор, ок := узел.(*ast.SelectorExpr)
		if !ок {
			return true
		}
		пакет, ок := селектор.X.(*ast.Ident)
		if !ок || пакет.Name != "store" || поверхностьStoreВResolve[селектор.Sel.Name] {
			return true
		}
		нарушения = append(нарушения, Violation{
			Rule: RuleResolveStoreSurface,
			File: позицияФайла(файл, селектор.Pos()),
			Message: fmt.Sprintf("resolve обращается к store.%s: разрешены только типы фактов и константы схемы, "+
				"а не доступ к базе", селектор.Sel.Name),
		})
		return true
	})
	return нарушения
}

// CheckLegacyIsolation: индексное ядро не тянет старый слой. Исключение одно —
// internal/syntax, реестр platform builtins.
func CheckLegacyIsolation(m *Module) []Violation {
	var нарушения []Violation
	for _, новый := range новыеПакеты {
		for _, пакет := range m.UnderPrefix(новый) {
			for _, файл := range пакет.Files {
				for _, импорт := range файл.Imports {
					rel, свой := m.Rel(импорт)
					if !свой {
						continue
					}
					for _, старый := range старыйСлой {
						if подПрефиксом(rel, старый) {
							нарушения = append(нарушения, Violation{
								Rule:    RuleNoLegacyInNew,
								File:    файл.Rel,
								Import:  импорт,
								Message: "новый код не опирается на старый слой; из старого разрешён только internal/syntax",
							})
						}
					}
				}
			}
		}
	}
	return нарушения
}

// Имена под-запретов для PortabilityExceptions — конкретнее, чем Violation.Rule
// (тот всегда RulePortableCommon для всей проверки портируемости разом): запись
// в PortabilityExceptions называет ровно то, от чего освобождён файл, а не
// освобождает его от CheckPortability целиком.
const (
	// ExceptionUnixLiteral — файлу разрешено держать в исходнике unix-only
	// строковый литерал ("/tmp", "~/" и их производные) текстом.
	ExceptionUnixLiteral = "unix-literal"
)

// PortabilityExceptions — явный и аудируемый список: какому файлу какое
// конкретное под-нарушение разрешено, а не файл целиком освобождён от всего
// подряд. rules.go держит образцы запрещённых литералов ("/tmp", "~/") для
// собственных нужд гарда (строкаUnixOnly ниже) — это единственная причина
// исключения, поэтому единственная разрешённая запись — ExceptionUnixLiteral.
// От syscall и от склейки путей (прямой и через переменную) rules.go
// подчиняется CheckPortability наравне с любым другим файлом.
var PortabilityExceptions = map[string][]string{
	"internal/arch/rules.go": {ExceptionUnixLiteral},
}

// исключён отвечает, разрешено ли файлу конкретное под-нарушение.
func исключён(файл string, подНарушение string) bool {
	for _, разрешённое := range PortabilityExceptions[файл] {
		if разрешённое == подНарушение {
			return true
		}
	}
	return false
}

// CheckPortability: macOS и Windows равноправны. В общем коде — ни syscall, ни
// склейки путей через литерал со слэшем, ни /tmp и ~/. Платформенная специфика
// живёт в файлах с суффиксом платформы или под build tag: их Go отбирает сам.
func CheckPortability(m *Module) []Violation {
	var нарушения []Violation
	for i := range m.Packages {
		for _, файл := range m.Packages[i].Files {
			if файл.Platform != "" || файл.BuildTag {
				continue
			}
			нарушения = append(нарушения, непортируемыеИмпорты(файл)...)
			нарушения = append(нарушения, непортируемыеЛитералы(файл, исключён(файл.Rel, ExceptionUnixLiteral))...)
		}
	}
	return нарушения
}

// непортируемыеИмпорты ловит syscall и golang.org/x/sys вне платформенных файлов.
func непортируемыеИмпорты(файл File) []Violation {
	var нарушения []Violation
	for _, импорт := range файл.Imports {
		if импорт != "syscall" && !strings.HasPrefix(импорт, "golang.org/x/sys/") {
			continue
		}
		нарушения = append(нарушения, Violation{
			Rule:    RulePortableCommon,
			File:    файл.Rel,
			Import:  импорт,
			Message: "системные вызовы — только в файле с суффиксом платформы или под build tag",
		})
	}
	return нарушения
}

// пакетыФайловойСистемы — те, чьи аргументы точно являются путями на диске.
// Проверка привязана к ним намеренно: слэш в литерале сам по себе ничего не
// значит (URI, XML, текст сообщения), значение имеет слэш в пути к файлу.
var пакетыФайловойСистемы = map[string]bool{"os": true, "filepath": true, "exec": true, "ioutil": true}

// непортируемыеЛитералы ловит unix-only пути и склейку пути со слэшем там, где
// результат уходит в файловую систему.
//
// предел: сканер ловит только прямые строковые литералы; обфускация через
// runes/конкатенацию не отслеживается — тот же класс предела, что и у склейки
// путей через переменную. Погоня за произвольной обфускацией несоразмерна
// стоимости для внутреннего архитектурного гарда: это не security-граница.
//
// unixЛитералРазрешён — файлу разрешено (см. PortabilityExceptions,
// ExceptionUnixLiteral) держать unix-only литерал текстом; остальные проверки
// этой функции (сравнение разделителя, склейка пути) исключению не подчиняются.
func непортируемыеЛитералы(файл File, unixЛитералРазрешён bool) []Violation {
	var нарушения []Violation
	переменные := склейкиПутейВПеременных(файл.syntax)
	ast.Inspect(файл.syntax, func(узел ast.Node) bool {
		if литерал, ок := узел.(*ast.BasicLit); ок {
			if unixЛитералРазрешён {
				return true
			}
			if значение, ок := строкаЛитерала(литерал); ок && строкаUnixOnly(значение) {
				нарушения = append(нарушения, Violation{
					Rule:    RulePortableCommon,
					File:    позицияФайла(файл, литерал.Pos()),
					Message: fmt.Sprintf("литерал %q предполагает unix-раскладку файловой системы", значение),
				})
			}
			return true
		}
		if сравнение, ок := узел.(*ast.BinaryExpr); ок && сравнениеРазделителя(сравнение) {
			нарушения = append(нарушения, Violation{
				Rule:    RulePortableCommon,
				File:    позицияФайла(файл, сравнение.Pos()),
				Message: "разделитель пути сравнивается со слэшем: на Windows это ложь, а не истина",
			})
			return true
		}
		вызов, ок := узел.(*ast.CallExpr)
		if !ок || !вызовФайловойСистемы(вызов) {
			return true
		}
		for _, аргумент := range вызов.Args {
			нарушения = append(нарушения, склейкиПути(файл, аргумент)...)
			нарушения = append(нарушения, склейкаЧерезПеременную(файл, аргумент, переменные)...)
		}
		return true
	})
	return нарушения
}

// сравнениеРазделителя ловит os.PathSeparator == '/' и его родню: код,
// написанный в предположении, что разделитель всегда слэш.
func сравнениеРазделителя(сравнение *ast.BinaryExpr) bool {
	if сравнение.Op != token.EQL && сравнение.Op != token.NEQ {
		return false
	}
	разделитель, слэш := false, false
	for _, операнд := range []ast.Expr{сравнение.X, сравнение.Y} {
		switch выражение := операнд.(type) {
		case *ast.SelectorExpr:
			пакет, ок := выражение.X.(*ast.Ident)
			if ок && (пакет.Name == "os" || пакет.Name == "filepath") &&
				(выражение.Sel.Name == "PathSeparator" || выражение.Sel.Name == "Separator") {
				разделитель = true
			}
		case *ast.BasicLit:
			if выражение.Value == `'/'` || выражение.Value == `"/"` {
				слэш = true
			}
		}
	}
	return разделитель && слэш
}

// вызовФайловойСистемы узнаёт вызов вида os.ReadFile, filepath.Walk, exec.Command.
func вызовФайловойСистемы(вызов *ast.CallExpr) bool {
	селектор, ок := вызов.Fun.(*ast.SelectorExpr)
	if !ок {
		return false
	}
	пакет, ок := селектор.X.(*ast.Ident)
	return ок && пакетыФайловойСистемы[пакет.Name]
}

// склейкиПути ищет внутри аргумента конкатенацию с литералом-куском пути.
func склейкиПути(файл File, аргумент ast.Expr) []Violation {
	var нарушения []Violation
	ast.Inspect(аргумент, func(узел ast.Node) bool {
		выражение, ок := узел.(*ast.BinaryExpr)
		if !ок || выражение.Op != token.ADD {
			return true
		}
		for _, операнд := range []ast.Expr{выражение.X, выражение.Y} {
			литерал, ок := операнд.(*ast.BasicLit)
			if !ок {
				continue
			}
			значение, ок := строкаЛитерала(литерал)
			if !ок || !путьСоСлэшем(значение) {
				continue
			}
			нарушения = append(нарушения, Violation{
				Rule:    RulePortableCommon,
				File:    позицияФайла(файл, литерал.Pos()),
				Message: fmt.Sprintf("путь к файлу склеивается с литералом %q: нужен filepath.Join", значение),
			})
		}
		return true
	})
	return нарушения
}

// местоСклейки — где и с каким литералом переменная была собрана через `:=`.
type местоСклейки struct {
	значение string
	поз      token.Pos
}

// склейкиПутейВПеременных ищет во всём файле присваивания вида
// `p := dir + "/sub/x.bin"` — склейка не встроена прямо в аргумент вызова, а
// заведена в переменную строкой раньше. Предел: один шаг присваивания назад и
// без учёта области видимости — переменная разбирается по имени в пределах
// всего файла, а не функции. Чего это не ловит: цепочку из двух и более
// присваиваний (`q := p; os.ReadFile(q)`) и одноимённые переменные в разных
// функциях считает одной и той же — оправдано тем, что это гард, а не
// компилятор: ложный срабатывание здесь дешевле пропуска.
func склейкиПутейВПеременных(файл ast.Node) map[string][]местоСклейки {
	места := map[string][]местоСклейки{}
	ast.Inspect(файл, func(узел ast.Node) bool {
		присваивание, ок := узел.(*ast.AssignStmt)
		if !ок || присваивание.Tok != token.DEFINE || len(присваивание.Lhs) != len(присваивание.Rhs) {
			return true
		}
		for i, rhs := range присваивание.Rhs {
			имя, ок := присваивание.Lhs[i].(*ast.Ident)
			if !ок || имя.Name == "_" {
				continue
			}
			выражение, ок := rhs.(*ast.BinaryExpr)
			if !ок || выражение.Op != token.ADD {
				continue
			}
			for _, операнд := range []ast.Expr{выражение.X, выражение.Y} {
				литерал, ок := операнд.(*ast.BasicLit)
				if !ок {
					continue
				}
				значение, ок := строкаЛитерала(литерал)
				if !ок || !путьСоСлэшем(значение) {
					continue
				}
				места[имя.Name] = append(места[имя.Name], местоСклейки{значение: значение, поз: литерал.Pos()})
			}
		}
		return true
	})
	return места
}

// склейкаЧерезПеременную проверяет, не является ли аргумент вызова
// переменной, собранной складыванием пути строкой раньше (см. склейкиПутейВПеременных).
func склейкаЧерезПеременную(файл File, аргумент ast.Expr, места map[string][]местоСклейки) []Violation {
	идент, ок := аргумент.(*ast.Ident)
	if !ок {
		return nil
	}
	найденные, есть := места[идент.Name]
	if !есть {
		return nil
	}
	var нарушения []Violation
	for _, место := range найденные {
		нарушения = append(нарушения, Violation{
			Rule: RulePortableCommon,
			File: позицияФайла(файл, место.поз),
			Message: fmt.Sprintf(
				"путь к файлу склеивается с литералом %q через переменную %s: нужен filepath.Join",
				место.значение, идент.Name),
		})
	}
	return нарушения
}

// строкаЛитерала разворачивает строковый литерал.
func строкаЛитерала(литерал *ast.BasicLit) (string, bool) {
	if литерал.Kind != token.STRING {
		return "", false
	}
	значение, err := strconv.Unquote(литерал.Value)
	if err != nil {
		return "", false
	}
	return значение, true
}

// строкаUnixOnly — литерал, который на Windows не значит ничего. Держит
// образцы запрещённого текстом ("/tmp", "~/") — поэтому сам rules.go занесён в
// PortabilityExceptions под ExceptionUnixLiteral, явной записью, а не
// обфускацией литерала: так исключение видно при чтении файла, а не спрятано
// в коде, который выглядит как рабочий приём для обхода проверки.
func строкаUnixOnly(значение string) bool {
	return значение == "/tmp" || strings.HasPrefix(значение, "/tmp/") ||
		значение == "~" || strings.HasPrefix(значение, "~/")
}

// путьСоСлэшем отличает кусок пути от куска URI и от обычного текста: в URI
// слэш — часть синтаксиса и на Windows остаётся слэшем, а текст с пробелами и
// переводами строк путём не является.
func путьСоСлэшем(значение string) bool {
	if !strings.Contains(значение, "/") {
		return false
	}
	if strings.Contains(значение, "://") || strings.Contains(значение, "{") {
		return false
	}
	return !strings.ContainsAny(значение, " \t\n\r<>")
}

// позицияФайла возвращает "файл:строка" — по нему сразу видно, куда смотреть.
func позицияФайла(файл File, позиция token.Pos) string {
	if файл.fset == nil {
		return файл.Rel
	}
	return fmt.Sprintf("%s:%d", файл.Rel, файл.fset.Position(позиция).Line)
}

// sqlИмпорт узнаёт SQL-зависимости.
func sqlИмпорт(импорт string) bool {
	if импорт == "database/sql" || strings.HasPrefix(импорт, "database/sql/") {
		return true
	}
	return strings.Contains(strings.ToLower(импорт), "sqlite")
}

// подПрефиксом — путь пакета лежит в поддереве prefix.
func подПрефиксом(rel, prefix string) bool {
	return rel == prefix || strings.HasPrefix(rel, prefix+"/")
}
