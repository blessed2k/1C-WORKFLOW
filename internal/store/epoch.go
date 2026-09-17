package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Layout одного logical project (ADR-2 §9.1):
//
//	<root>/<stateDir>/index/<project-id>/
//	    pointer.bin              — published-pointer этого проекта
//	    <project-id>.e<N>.sqlite — эпохи этого проекта
//
// Каталог выделен под проект целиком: recovery рассматривает и убирает остатки
// сборок по маске имени проекта. При общем каталоге sweep одного проекта мог бы
// зацепить файл другого, а удалить чужую эпоху как «ничью» хуже, чем оставить
// мусор.
//
// Имя <stateDir> приходит параметром: каталог сгенерированного состояния
// принадлежит workspace, а направление зависимостей не даёт store его
// импортировать. Собственная константа здесь означала бы вторую правду о том,
// где живёт состояние сервера.

// indexSubDir — подкаталог индексов внутри каталога состояния.
const indexSubDir = "index"

// ProjectIndexDir — каталог индекса проекта. Форму идентификатора проверяет
// domain: этот же слаг попадает в манифест и в реестр, и разойтись проверки не
// имеют права — id, годный для файлов индекса, обязан быть годен и для реестра.
func ProjectIndexDir(root, stateDir string, projectID domain.ProjectID) (string, error) {
	if err := projectID.Validate(); err != nil {
		return "", err
	}
	if stateDir == "" {
		return "", errors.New("не задано имя каталога состояния сервера")
	}
	if stateDir == "." || stateDir == ".." || stateDir != filepath.Base(stateDir) ||
		strings.ContainsAny(stateDir, `/\`) {
		return "", fmt.Errorf("каталог состояния %q не является именем каталога", stateDir)
	}
	return filepath.Join(root, stateDir, indexSubDir, string(projectID)), nil
}

// epochName — базовое имя файла эпохи. Payload указателя хранит ИМЕННО его:
// путь в указателе позволил бы увести recovery в чужой каталог.
func epochName(project domain.ProjectID, epoch int) string {
	return fmt.Sprintf("%s.e%d.sqlite", string(project), epoch)
}

func epochFile(dir string, project domain.ProjectID, epoch int) string {
	return filepath.Join(dir, epochName(project, epoch))
}

// epochNameRe — файлы эпох ОДНОГО проекта.
func epochNameRe(project domain.ProjectID) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(string(project)) + `\.e(\d+)\.sqlite$`)
}

// validatePointerPayload — payload обязан быть базовым именем эпохи ЭТОГО
// проекта. Разделители пути, «..» и чужой префикс отвергаются ДО любого
// обращения к файловой системе: повреждённый или подменённый указатель иначе
// увёл бы recovery за пределы каталога проекта.
func validatePointerPayload(project domain.ProjectID, payload string) error {
	if payload == "" {
		return errors.New("пустой payload указателя")
	}
	if payload != filepath.Base(payload) || strings.ContainsAny(payload, `/\`) {
		return fmt.Errorf("payload указателя %q не является базовым именем файла", payload)
	}
	if !epochNameRe(project).MatchString(payload) {
		return fmt.Errorf("payload указателя %q не соответствует формату эпохи проекта %q", payload, project)
	}
	return nil
}

func epochNumber(project domain.ProjectID, name string) (int, bool) {
	m := epochNameRe(project).FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// recoveryReport — что видит сервер при старте (раздел 18.1, четыре случая).
type recoveryReport struct {
	pointerOK bool
	current   string
	seq       uint64
	// orphans — эпохи, про которые ДОКАЗАНО, что они не публиковались: остаток
	// прерванной сборки. Только они подлежат уборке.
	orphans []string
	// kept — эпохи с данными, на которые указатель не смотрит. Они остаются на
	// диске: «указатель на неё не смотрит» перестало быть основанием удалять.
	kept            []string
	needFullRebuild bool
	note            string
	maxEpoch        int
}

// EpochFileInfo — файл эпохи, как его видит восстановление.
type EpochFileInfo struct {
	Name    string `json:"name"`
	Bytes   int64  `json:"bytes"`
	HasData bool   `json:"hasData"`
}

// EpochQuarantineError — карантин: указатель называет эпоху без данных, а рядом
// лежит эпоха с данными. Ничего не удалено и ничего не починено автоматически:
// перевести указатель на соседнюю эпоху значило бы опубликовать сборку, чей
// fingerprint источников мог устареть, — одна тихая беда вместо другой.
type EpochQuarantineError struct {
	PointerPath string          `json:"pointerPath"`
	Target      string          `json:"target"`
	TargetBytes int64           `json:"targetBytes"`
	Epochs      []EpochFileInfo `json:"epochs"`
}

func (e *EpochQuarantineError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "указатель эпохи %s называет %q, а в этом файле нет данных (%d Б); ",
		e.PointerPath, e.Target, e.TargetBytes)
	b.WriteString("рядом лежит эпоха с данными, поэтому работа остановлена и ни один файл не удалён; ")
	b.WriteString("найденные эпохи:")
	for _, ep := range e.Epochs {
		state := "без данных"
		if ep.HasData {
			state = "с данными"
		}
		fmt.Fprintf(&b, " %s %d Б (%s);", ep.Name, ep.Bytes, state)
	}
	b.WriteString(" выход из карантина: пересобрать индекс командой reindex")
	return b.String()
}

// recoverEpochs реализует контракт восстановления (ADR-023).
//
// Указатель остаётся единственным источником истины о том, какая эпоха
// ОПУБЛИКОВАНА: эпоха, на которую он не смотрит, не публикуется автоматически,
// потому что fingerprint источников с момента её сборки мог устареть. Но
// «указатель на неё не смотрит» больше НЕ является основанием её удалить:
// именно это основание 20.08 уничтожило 2.4 ГБ индекса, когда указатель
// назвал пустой e3, а данные лежали в e2.
//
// Удалить можно только эпоху, про которую ДОКАЗАНО, что она не публиковалась:
// остаток прерванной сборки (нулевой файл, не открывается как SQLite, нет
// таблицы meta). Всё остальное попадает в kept и остаётся на диске. Поверх
// критерия стоит инвариант в removeUnpublishedEpoch: файл с данными не
// удаляется, чем бы ни закончилась классификация.
//
// Указатель на эпоху без данных при живой соседней — не повод убирать соседнюю
// и не повод чинить указатель: возвращается EpochQuarantineError.
func recoverEpochs(dir string, project domain.ProjectID, ptr *pointer) (*recoveryReport, error) {
	if err := project.Validate(); err != nil {
		return nil, err
	}
	rep := &recoveryReport{}
	payload, seq, err := ptr.Read()
	switch {
	case err != nil && !errors.Is(err, ErrNoPointer):
		// Техническая причина (права, ввод-вывод). Это НЕ «указатель повреждён»:
		// полный rebuild здесь выбросил бы рабочий индекс из-за временной
		// проблемы с доступом.
		return nil, fmt.Errorf("указатель не прочитан: %w", err)
	case err != nil:
		rep.needFullRebuild = true
		rep.note = "достоверной записи указателя нет: полный rebuild из XML, " +
			"автопубликация validated-эпохи запрещена"
	default:
		if vErr := validatePointerPayload(project, payload); vErr != nil {
			rep.needFullRebuild = true
			rep.note = fmt.Sprintf("указатель отвергнут: %v; полный rebuild из XML", vErr)
			break
		}
		// Указатель мог пережить файл, на который указывает.
		if _, statErr := os.Stat(filepath.Join(dir, payload)); statErr != nil {
			if !os.IsNotExist(statErr) {
				return nil, fmt.Errorf("не удалось проверить файл опубликованной эпохи %q: %w", payload, statErr)
			}
			rep.needFullRebuild = true
			rep.note = fmt.Sprintf("указатель ссылается на %q, но файла нет: полный rebuild из XML", payload)
			break
		}
		rep.pointerOK, rep.current, rep.seq = true, payload, seq
	}
	return finishRecover(dir, project, rep)
}

// finishRecover перечисляет файлы эпох ЭТОГО проекта и раскладывает их на две
// кучи: доказуемые остатки прерванной сборки (orphans, к уборке) и эпохи с
// данными (kept, остаются на диске). Файлы других проектов не рассматриваются
// даже при общем каталоге: удалить чужую опубликованную эпоху как «ничью» хуже,
// чем оставить мусор.
func finishRecover(dir string, project domain.ProjectID, rep *recoveryReport) (*recoveryReport, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var found []EpochFileInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n, ok := epochNumber(project, e.Name())
		if !ok {
			continue
		}
		if n > rep.maxEpoch {
			rep.maxEpoch = n
		}
		path := filepath.Join(dir, e.Name())
		size, sErr := fileSize(path)
		if sErr != nil {
			return nil, sErr
		}
		found = append(found, EpochFileInfo{Name: e.Name(), Bytes: size, HasData: epochHasData(path)})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })

	if qErr := quarantineIfPointerEmpty(dir, rep, found); qErr != nil {
		return nil, qErr
	}
	invalidatePointerToEmptyEpoch(rep, found)
	for _, ep := range found {
		if rep.pointerOK && ep.Name == rep.current {
			continue
		}
		if ep.HasData {
			rep.kept = append(rep.kept, ep.Name)
			continue
		}
		rep.orphans = append(rep.orphans, ep.Name)
	}
	if len(rep.kept) > 0 {
		rep.note = appendNote(rep.note, fmt.Sprintf(
			"эпохи с данными, на которые указатель не смотрит, оставлены на месте: %s",
			strings.Join(rep.kept, ", ")))
	}
	return rep, nil
}

// quarantineIfPointerEmpty ловит случай 20.08: указатель называет эпоху, в
// которой нет данных, а рядом лежит эпоха с данными. Автоматического перевода
// указателя нет по решению спецификации §1 — только остановка с диагностикой.
func quarantineIfPointerEmpty(dir string, rep *recoveryReport, found []EpochFileInfo) error {
	if !rep.pointerOK {
		return nil
	}
	target, targetFound, neighbourWithData := pointerTargetState(rep, found)
	if !targetFound || target.HasData || !neighbourWithData {
		return nil
	}
	return &EpochQuarantineError{
		PointerPath: filepath.Join(dir, pointerName),
		Target:      target.Name,
		TargetBytes: target.Bytes,
		Epochs:      found,
	}
}

// pointerTargetState отвечает на два вопроса о диске относительно указателя, и
// оба нужны обеим веткам разбора: есть ли данные в эпохе, на которую указатель
// смотрит, и есть ли данные хоть где-то ещё. Один проход на оба ответа: два
// независимых обхода того же среза — это два выражения одного понятия, а они в
// этом файле уже однажды стоили индекса.
func pointerTargetState(rep *recoveryReport, found []EpochFileInfo) (target EpochFileInfo, targetFound, neighbourWithData bool) {
	for _, ep := range found {
		if ep.Name == rep.current {
			target, targetFound = ep, true
			continue
		}
		if ep.HasData {
			neighbourWithData = true
		}
	}
	return target, targetFound, neighbourWithData
}

// invalidatePointerToEmptyEpoch — вторая половина случая «указатель на эпоху
// без данных»: соседней эпохи с данными НЕТ. Карантин здесь неуместен, терять
// нечего, но и доверять указателю нельзя. До этой правки хранилище шло
// открывать нулевой файл как готовую базу (файл существует — значит база
// создана), persistent-pragma к нему не применялись, и открытие падало на
// journal_mode="delete". Индекс становился неремонтируемым: reindex,
// единственное названное лечение, сам открывает хранилище и падал там же.
//
// Указатель признаётся недостоверным, и дальше работает обычная ветка
// аварийной сборки: свежая пустая эпоха и полный rebuild из XML. Нулевой файл
// после этого классифицируется как остаток прерванной сборки, а инвариант
// removeUnpublishedEpoch всё равно не даст удалить файл с данными.
func invalidatePointerToEmptyEpoch(rep *recoveryReport, found []EpochFileInfo) {
	if !rep.pointerOK {
		return
	}
	target, targetFound, neighbourWithData := pointerTargetState(rep, found)
	if !targetFound || target.HasData || neighbourWithData {
		return
	}
	name := target.Name
	rep.pointerOK, rep.current, rep.seq = false, "", 0
	rep.needFullRebuild = true
	rep.note = appendNote(rep.note, fmt.Sprintf(
		"указатель называет %q, данных в ней нет и соседних эпох с данными нет: "+
			"указатель отвергнут, полный rebuild из XML", name))
}

// appendNote склеивает строки отчёта восстановления: их может быть несколько,
// и молча затирать предыдущую нельзя.
func appendNote(note, add string) string {
	if note == "" {
		return add
	}
	return note + "; " + add
}

// removeEpoch удаляет файлы эпохи с retry: на Windows открытый дескриптор
// временно блокирует удаление, и это ожидаемое поведение, а не сбой. Порядок
// суффиксов значим: сам файл БД удаляется последним.
func removeEpoch(path string, attempts int, pause time.Duration) error {
	var lastErr error
	for i := 0; i < attempts; i++ {
		lastErr = nil
		for _, suffix := range []string{"-shm", "-wal", ""} {
			if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
				lastErr = err
			}
		}
		if lastErr == nil {
			return nil
		}
		time.Sleep(pause)
	}
	return lastErr
}

func fileSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return fi.Size(), nil
}
