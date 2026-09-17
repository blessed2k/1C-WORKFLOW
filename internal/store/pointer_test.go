package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// Указатель публикует монотонно и читает последнюю публикацию.
func TestPointerPublishesMonotonically(t *testing.T) {
	p := newPointer(t.TempDir())
	if _, _, err := p.Read(); !errors.Is(err, ErrNoPointer) {
		t.Fatalf("пустой указатель дал %v, ожидался ErrNoPointer", err)
	}
	var lastSeq uint64
	for i := 1; i <= 5; i++ {
		name := epochName("project", i)
		seq, err := p.Publish(name)
		if err != nil {
			t.Fatalf("публикация: %v", err)
		}
		if seq <= lastSeq {
			t.Fatalf("seq не растёт: %d после %d", seq, lastSeq)
		}
		lastSeq = seq
		pay, got, err := p.Read()
		if err != nil {
			t.Fatalf("чтение: %v", err)
		}
		if pay != name || got != seq {
			t.Fatalf("прочитано %q/%d, ожидалось %q/%d", pay, got, name, seq)
		}
	}
}

// Обрыв питания ровно в момент публикации портит только НЕактивный слот:
// активный остаётся целым, и указатель не теряется и не подменяется неполной
// записью.
func TestPointerSurvivesTornWrite(t *testing.T) {
	dir := t.TempDir()
	p := newPointer(dir)
	for i := 1; i <= 2; i++ {
		if _, err := p.Publish(epochName("project", i)); err != nil {
			t.Fatal(err)
		}
	}
	active, _, err := p.Read()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, pointerName))
	if err != nil {
		t.Fatal(err)
	}
	// Следующая публикация пошла бы в НЕактивный слот: имитируем обрыв на
	// половине записи.
	slots := [2]slotState{}
	for i := 0; i < 2; i++ {
		seq, pay, ok := decodeSlot(data[i*slotSize : (i+1)*slotSize])
		slots[i] = slotState{seq: seq, pay: pay, valid: ok}
	}
	target := 0
	if best(slots) == 0 {
		target = 1
	}
	copy(data[target*slotSize:], encodeSlot(3, epochName("project", 3))[:slotSize/2])
	if err := os.WriteFile(filepath.Join(dir, pointerName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, err := p.Read()
	if err != nil {
		t.Fatalf("после оборванной записи указатель нечитаем: %v", err)
	}
	if got != active {
		t.Fatalf("оборванная запись подменила указатель: %q вместо %q", got, active)
	}
}

// Payload указателя обязан быть базовым именем эпохи ЭТОГО проекта: путь, «..»
// и чужой префикс отвергаются до любого обращения к файловой системе.
func TestPointerPayloadRejectsTraversal(t *testing.T) {
	bad := []string{
		"",
		"../other.e1.sqlite",
		"sub/project.e1.sqlite",
		`sub\project.e1.sqlite`,
		"other.e1.sqlite",
		"project.sqlite",
		"project.eX.sqlite",
	}
	for _, payload := range bad {
		if err := validatePointerPayload("project", payload); err == nil {
			t.Errorf("payload %q принят, хотя обязан быть отвергнут", payload)
		}
	}
	if err := validatePointerPayload("project", "project.e7.sqlite"); err != nil {
		t.Errorf("корректный payload отвергнут: %v", err)
	}
}

// Форма идентификатора проекта одна на весь сервер: то, что отвергает domain,
// обязано отвергнуть и хранилище. Разойдись они — id, годный для файлов
// индекса, не записался бы в реестр, и проект существовал бы наполовину.
func TestProjectIDAcceptedIdenticallyWithDomain(t *testing.T) {
	ids := []string{
		"project", "ut_demo", "ut-demo.1", // домен принимает
		"", ".", "..", "..x", "_x", "Cfg", "a/b", `a\b`, "проект", "with space",
		"0123456789012345678901234567890123456789012345678901234567890123456789",
	}
	for _, id := range ids {
		byDomain := domain.ValidateSlug(id) == nil
		_, err := ProjectIndexDir(t.TempDir(), testStateDir, domain.ProjectID(id))
		byStore := err == nil
		if byDomain != byStore {
			t.Errorf("идентификатор %q: domain принимает=%v, store принимает=%v", id, byDomain, byStore)
		}
		if !byStore {
			continue
		}
		s, oErr := Open(t.TempDir(), Options{ProjectID: domain.ProjectID(id), StateDirName: testStateDir})
		if oErr != nil {
			t.Errorf("Open с идентификатором %q: %v", id, oErr)
			continue
		}
		s.Close()
	}
}

// Каталог состояния сервера приходит параметром, а не собирается внутри store:
// пустое или составное имя — отказ, а не молчаливое умолчание.
func TestStateDirNameIsRequiredAndFlat(t *testing.T) {
	for _, name := range []string{"", "a/b", `a\b`, ".."} {
		if _, err := ProjectIndexDir(t.TempDir(), name, "project"); err == nil {
			t.Errorf("каталог состояния %q принят", name)
		}
	}
	if _, err := Open(t.TempDir(), Options{ProjectID: "project"}); err == nil {
		t.Error("Open без имени каталога состояния прошёл")
	}
}

// makeEpochFileWithMeta кладёт по пути файл, который epochHasData признаёт
// эпохой с данными: заголовок SQLite плюс таблица meta. Полное хранилище тут
// разворачивать незачем — проверяется классификация файлов, а не их содержимое.
func makeEpochFileWithMeta(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open(driverName, dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(),
		`CREATE TABLE meta(key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// Recovery видит только эпохи СВОЕГО проекта: при общем каталоге sweep иначе
// снёс бы опубликованную эпоху соседа как «ничью».
func TestRecoverIgnoresOtherProjectEpochs(t *testing.T) {
	dir := t.TempDir()
	p := newPointer(dir)
	if _, err := p.Publish(epochName("project", 1)); err != nil {
		t.Fatal(err)
	}
	// Эпоха, на которую смотрит указатель, обязана БЫТЬ эпохой с данными:
	// иначе сцена вырождается в «данных нет нигде», где указатель отвергается
	// сам (invalidatePointerToEmptyEpoch), и проверка перестаёт быть про
	// чужие проекты. Остальные два файла — заведомый мусор, им так и надо.
	makeEpochFileWithMeta(t, filepath.Join(dir, epochName("project", 1)))
	for _, name := range []string{
		epochName("project", 2),
		epochName("other", 1),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("db"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rep, err := recoverEpochs(dir, "project", p)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.pointerOK || rep.current != epochName("project", 1) {
		t.Fatalf("указатель: ok=%v current=%q", rep.pointerOK, rep.current)
	}
	if len(rep.orphans) != 1 || rep.orphans[0] != epochName("project", 2) {
		t.Fatalf("orphan-ы %v, ожидалась только эпоха 2 своего проекта", rep.orphans)
	}
}

// Повреждённый указатель, ссылающийся в никуда, обязан приводить к честному
// «нужен полный rebuild», а не к выбору «последней validated-эпохи»: отметка
// validated означает «готова», но НЕ «была опубликована» (запрет ревью №5).
func TestBothSlotsCorruptedMeansFullRebuild(t *testing.T) {
	dir := t.TempDir()
	p := newPointer(dir)
	if _, err := p.Publish(epochName("project", 1)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pointerName), []byte("мусор"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Read(); !errors.Is(err, ErrNoPointer) {
		t.Fatalf("повреждённый указатель прочитался: %v", err)
	}
	// Рядом лежит файл эпохи, который не открывается как SQLite: доказуемый
	// остаток прерванной сборки, только он и подлежит уборке (ADR-023).
	if err := os.WriteFile(filepath.Join(dir, epochName("project", 2)), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, err := recoverEpochs(dir, "project", p)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.needFullRebuild {
		t.Error("recovery не потребовала полный rebuild")
	}
	if len(rep.orphans) != 1 {
		t.Errorf("unpointed-эпоха не помечена orphan: %v", rep.orphans)
	}
}

// Указатель, переживший свой файл эпохи (ручное удаление, сбой диска), — это не
// «работаем на ней», а тот же случай, что и отсутствие указателя.
func TestPointerToMissingEpochMeansRebuild(t *testing.T) {
	dir := t.TempDir()
	p := newPointer(dir)
	if _, err := p.Publish(epochName("project", 3)); err != nil {
		t.Fatal(err)
	}
	rep, err := recoverEpochs(dir, "project", p)
	if err != nil {
		t.Fatal(err)
	}
	if rep.pointerOK || !rep.needFullRebuild {
		t.Fatalf("указатель на несуществующий файл: ok=%v rebuild=%v", rep.pointerOK, rep.needFullRebuild)
	}
}

// Повреждён активный слот — работает второй: указатель хранит ДВЕ последние
// публикации, и предыдущая эпоха, пока её файл на месте, остаётся last known
// good. Состояние на диске здесь ровно то, что оставляет падение между записью
// указателя и уборкой старой эпохи.
func TestRecoveryFallsBackToSecondSlot(t *testing.T) {
	root := t.TempDir()
	opts := Options{ProjectID: "project", StateDirName: testStateDir}
	s, err := Open(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Вторая эпоха публикуется, но старый файл остаётся на диске.
	if _, err := s.buildEpoch(ctx, 2, buildDeliberate, nil); err != nil {
		t.Fatalf("сборка эпохи 2: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	dir, err := ProjectIndexDir(root, testStateDir, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, epochName("project", 1))); err != nil {
		t.Fatalf("файл эпохи 1 не сохранился, тест ничего не проверяет: %v", err)
	}

	// Портим слот с бо́льшим seq — тот, что указывает на эпоху 2.
	path := filepath.Join(dir, pointerName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var slots [2]slotState
	for i := 0; i < 2; i++ {
		seq, pay, ok := decodeSlot(data[i*slotSize : (i+1)*slotSize])
		slots[i] = slotState{seq: seq, pay: pay, valid: ok}
	}
	active := best(slots)
	if slots[active].pay != epochName("project", 2) {
		t.Fatalf("активный слот указывает на %q, ожидалась эпоха 2", slots[active].pay)
	}
	for i := active * slotSize; i < (active+1)*slotSize; i++ {
		data[i] = 0
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(root, opts)
	if err != nil {
		t.Fatalf("открытие после порчи активного слота: %v", err)
	}
	defer s2.Close()
	st, err := s2.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Epoch != 1 {
		t.Errorf("работаем на эпохе %d, ожидалась 1 из второго слота", st.Epoch)
	}
	if st.NeedsFullRebuild {
		t.Error("восстановление из второго слота потребовало полный rebuild, хотя эпоха 1 цела")
	}
	// Эпоха 2 не опубликована, но в её файле есть данные, поэтому она остаётся
	// на диске (ADR-023) и видна в статусе как оставленная.
	if _, err := os.Stat(filepath.Join(dir, epochName("project", 2))); err != nil {
		t.Errorf("эпоха 2 с данными удалена при старте: %v", err)
	}
	if len(st.KeptEpochs) != 1 || st.KeptEpochs[0] != epochName("project", 2) {
		t.Errorf("оставленная эпоха 2 не отражена в статусе: %v", st.KeptEpochs)
	}
}

// Оба слота уничтожены: хранилище открывается на новой пустой эпохе и честно
// требует полной сборки из XML.
func TestOpenWithDestroyedPointerRebuilds(t *testing.T) {
	root := t.TempDir()
	opts := Options{ProjectID: "project", StateDirName: testStateDir}
	s, err := Open(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var f fixture
	if err := s.Write(ctx, func(tx *WriteTx) error { return seedFixture(tx, &f) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	dir, _ := ProjectIndexDir(root, testStateDir, "project")
	if err := os.WriteFile(filepath.Join(dir, pointerName), []byte("мусор"), 0o644); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(root, opts)
	if err != nil {
		t.Fatalf("открытие с уничтоженным указателем: %v", err)
	}
	defer s2.Close()
	st, err := s2.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.NeedsFullRebuild {
		t.Error("уничтоженный указатель не привёл к требованию полной сборки")
	}
	if st.Epoch != 2 {
		t.Errorf("эпоха %d, ожидалась 2: старая признана orphan, публикуется новая", st.Epoch)
	}
	if n := countRows(t, s2, "symbol", ""); n != 0 {
		t.Errorf("новая эпоха не пуста: символов %d", n)
	}
	// Файл старой эпохи не удаляется: указатель уничтожен, значит доказательства
	// того, что эпоха 1 не публиковалась, нет, а в её файле лежат данные
	// (ADR-023). Работа идёт на новой пустой эпохе, старая просто лежит рядом.
	if _, err := os.Stat(filepath.Join(dir, epochName("project", 1))); err != nil {
		t.Errorf("эпоха 1 с данными удалена при старте: %v", err)
	}
	if len(st.KeptEpochs) != 1 || st.KeptEpochs[0] != epochName("project", 1) {
		t.Errorf("оставленная эпоха 1 не отражена в статусе: %v", st.KeptEpochs)
	}
}
