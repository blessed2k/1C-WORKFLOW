package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
)

// makeEpochWithData создаёт настоящий файл эпохи с данными и отдаёт его путь:
// хранилище открывается, пишет компонент и закрывается.
func makeEpochWithData(t *testing.T, root string, project domain.ProjectID) string {
	t.Helper()
	opts := Options{ProjectID: project, StateDirName: testStateDir}
	s, err := Open(root, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.UpsertComponent(Component{ID: "cfg", Kind: "configuration", Root: "."})
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	path := s.currentPath()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

// incidentScene собирает состояние диска, с которого начался инцидент 20.08:
// данные в эпохе 1, файл эпохи 2 нулевого размера, указатель на эпоху 2.
// Отдаёт пути обоих файлов и размер живой эпохи до открытия хранилища.
func incidentScene(t *testing.T, root string, project domain.ProjectID) (dataPath, emptyPath string, dataBytes int64) {
	t.Helper()
	dataPath = makeEpochWithData(t, root, project)
	fi, err := os.Stat(dataPath)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(dataPath)
	emptyPath = filepath.Join(dir, epochName(project, 2))
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newPointer(dir).Publish(epochName(project, 2)); err != nil {
		t.Fatal(err)
	}
	return dataPath, emptyPath, fi.Size()
}

// openIncidentStore открывает хранилище на этой сцене и отдаёт ошибку открытия.
// Хранилище закрывается за тестом: Open мог успеть создать соединения до отказа.
func openIncidentStore(t *testing.T, root string, project domain.ProjectID) error {
	t.Helper()
	s, err := Open(root, Options{ProjectID: project, StateDirName: testStateDir})
	if s != nil {
		t.Cleanup(func() { s.Close() })
	}
	return err
}

// Инвариант поверх классификации: файл эпохи с данными не удаляется ни при
// каком исходе классификации. Проверяется на самой уборке, а не на
// классификации: даже если recovery ошибётся и назовёт живую эпоху остатком,
// данные обязаны пережить эту ошибку.
func TestRemoveUnpublishedEpochRefusesFileWithData(t *testing.T) {
	root := t.TempDir()
	path := makeEpochWithData(t, root, "project")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	err = removeUnpublishedEpoch(path, epochRemoveAttempts, epochRemovePause)
	if !errors.Is(err, errEpochHasData) {
		t.Fatalf("уборка эпохи с данными вернула %v, ожидался отказ errEpochHasData", err)
	}
	after, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("файл эпохи с данными удалён вопреки инварианту: %v", statErr)
	}
	if after.Size() != before.Size() {
		t.Errorf("размер файла эпохи изменился: было %d, стало %d", before.Size(), after.Size())
	}
}

// Обратная половина того же инварианта: остаток прерванной сборки (файл, не
// открывающийся как SQLite) убирается, иначе уборка перестала бы работать вовсе.
func TestRemoveUnpublishedEpochRemovesLeftover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, epochName("project", 7))
	if err := os.WriteFile(path, []byte("недописанный остаток"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeUnpublishedEpoch(path, epochRemoveAttempts, epochRemovePause); err != nil {
		t.Fatalf("остаток прерванной сборки не убран: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("остаток прерванной сборки остался на диске: %v", err)
	}
}

// Критерий уборки, отдельно от инварианта: основанием для удаления перестало
// быть «указатель на неё не смотрит». Эпоха с данными, на которую указатель не
// смотрит, остаётся на диске и попадает в kept, а не в orphans.
func TestRecoverKeepsUnpointedEpochWithData(t *testing.T) {
	root := t.TempDir()
	const project = domain.ProjectID("project")
	s, err := Open(root, Options{ProjectID: project, StateDirName: testStateDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *WriteTx) error {
		return tx.UpsertComponent(Component{ID: "cfg", Kind: "configuration", Root: "."})
	}); err != nil {
		t.Fatal(err)
	}
	// Эпоха 2 собрана и опубликована, затем указатель возвращён на эпоху 1:
	// на диске две эпохи с данными, указатель смотрит на первую.
	if _, err := s.buildEpoch(ctx, 2, buildDeliberate, nil); err != nil {
		t.Fatalf("сборка эпохи 2: %v", err)
	}
	if _, err := s.ptr.Publish(epochName(project, 1)); err != nil {
		t.Fatal(err)
	}
	dir := s.dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	rep, err := recoverEpochs(dir, project, newPointer(dir))
	if err != nil {
		t.Fatalf("recoverEpochs: %v", err)
	}
	if !rep.pointerOK || rep.current != epochName(project, 1) {
		t.Fatalf("указатель: ok=%v current=%q", rep.pointerOK, rep.current)
	}
	if len(rep.orphans) != 0 {
		t.Errorf("эпоха с данными классифицирована как orphan: %v", rep.orphans)
	}
	if len(rep.kept) != 1 || rep.kept[0] != epochName(project, 2) {
		t.Errorf("kept=%v, ожидалась только эпоха 2", rep.kept)
	}
}

// Регрессия ровно на инцидент 20.08: указатель смотрел на пустой e3, данные
// лежали в e2, и первый же Store.start снёс 2.4 ГБ индекса как orphan.
// Проверяется не текст ошибки, а факт: файл с данными пережил открытие
// хранилища и не изменился ни на байт.
func TestStartKeepsDataEpochWhenPointerNamesEmptyOne(t *testing.T) {
	root := t.TempDir()
	const project = domain.ProjectID("project")
	dataPath, emptyPath, before := incidentScene(t, root, project)

	if err := openIncidentStore(t, root, project); err == nil {
		t.Error("Open прошёл молча, хотя указатель называет пустую эпоху при живой соседней")
	}

	after, statErr := os.Stat(dataPath)
	if statErr != nil {
		t.Fatalf("эпоха с данными удалена при открытии хранилища: %v", statErr)
	}
	if after.Size() != before {
		t.Errorf("размер эпохи с данными изменился: было %d Б, стало %d Б", before, after.Size())
	}
	if _, err := os.Stat(emptyPath); err != nil {
		t.Errorf("пустая эпоха, на которую смотрит указатель, тоже не должна удаляться: %v", err)
	}
}

// Карантин: указатель на эпоху без данных при живой соседней даёт остановку с
// диагностикой, а не уборку. Текст ошибки обязан отвечать на три вопроса —
// куда смотрит указатель, что вообще лежит на диске и как из карантина выйти.
func TestOpenQuarantinesPointerToEmptyEpoch(t *testing.T) {
	root := t.TempDir()
	const project = domain.ProjectID("project")
	dataPath, emptyPath, dataBytes := incidentScene(t, root, project)
	dir := filepath.Dir(dataPath)

	openErr := openIncidentStore(t, root, project)
	var q *EpochQuarantineError
	if !errors.As(openErr, &q) {
		t.Fatalf("Open вернул %v, ожидался EpochQuarantineError", openErr)
	}
	if q.PointerPath != filepath.Join(dir, pointerName) {
		t.Errorf("путь указателя %q, ожидался %q", q.PointerPath, filepath.Join(dir, pointerName))
	}
	if q.Target != epochName(project, 2) {
		t.Errorf("цель указателя %q, ожидалась %q", q.Target, epochName(project, 2))
	}
	if len(q.Epochs) != 2 {
		t.Fatalf("в диагностике %d эпох, ожидались обе: %+v", len(q.Epochs), q.Epochs)
	}
	byName := map[string]EpochFileInfo{}
	for _, ep := range q.Epochs {
		byName[ep.Name] = ep
	}
	live := byName[epochName(project, 1)]
	if !live.HasData || live.Bytes != dataBytes {
		t.Errorf("эпоха с данными описана как %+v, ожидались данные и размер %d", live, dataBytes)
	}
	if empty := byName[epochName(project, 2)]; empty.HasData || empty.Bytes != 0 {
		t.Errorf("пустая эпоха описана как %+v, ожидались 0 Б без данных", empty)
	}

	text := openErr.Error()
	for _, want := range []string{
		q.PointerPath,
		epochName(project, 1),
		epochName(project, 2),
		"reindex",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в тексте ошибки нет %q: %s", want, text)
		}
	}
	if !strings.Contains(text, itoa(int(dataBytes))) {
		t.Errorf("в тексте ошибки нет размера живой эпохи %d: %s", dataBytes, text)
	}

	// Указатель не переведён на соседнюю эпоху: карантин, а не починка.
	if pay, _, pErr := newPointer(dir).Read(); pErr != nil || pay != epochName(project, 2) {
		t.Errorf("указатель после карантина: payload=%q err=%v, ожидалась нетронутая эпоха 2", pay, pErr)
	}

	// Ни один файл не удалён: карантин — это остановка, а не уборка.
	for _, p := range []string{dataPath, emptyPath, filepath.Join(dir, pointerName)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("файл %s удалён при карантине: %v", p, err)
		}
	}
}

// Сцена мака 22.08: указатель называет эпоху без данных, а соседней эпохи с
// данными рядом НЕТ. Карантин тут ни при чём, терять нечего — и всё же до
// правки Open падал: нулевой файл СУЩЕСТВУЕТ, поэтому открывался как готовая
// база, persistent-pragma к нему не применялись, и проверка упиралась в
// journal_mode="delete". Индекс становился неремонтируемым: reindex,
// единственное лечение по тексту карантина, сам открывает хранилище и падает
// той же ошибкой. Правильный исход — указатель признан недостоверным,
// хранилище поднимается на свежей эпохе и планирует полную пересборку.
func TestOpenRebuildsWhenPointerNamesOnlyEmptyEpoch(t *testing.T) {
	root := t.TempDir()
	const project = domain.ProjectID("project")
	dir, err := ProjectIndexDir(root, testStateDir, project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Ровно то, что осталось на маке: нулевая эпоха 3 и указатель на неё.
	if err := os.WriteFile(filepath.Join(dir, epochName(project, 3)), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newPointer(dir).Publish(epochName(project, 3)); err != nil {
		t.Fatal(err)
	}

	s, err := Open(root, Options{ProjectID: project, StateDirName: testStateDir})
	if err != nil {
		t.Fatalf("Open на пустой эпохе без соседей: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if !s.needsFullRebuild {
		t.Error("хранилище поднялось без needsFullRebuild: пересборка из XML обязана быть запланирована")
	}
	// Пригодно к работе, а не только к открытию: reindex будет писать сюда.
	if err := s.Write(context.Background(), func(tx *WriteTx) error {
		return tx.UpsertComponent(Component{ID: "cfg", Kind: "configuration", Root: "."})
	}); err != nil {
		t.Fatalf("запись в поднятое хранилище: %v", err)
	}
}

// Ветка «схема из будущего» — не ротация: преемник здесь ПУСТАЯ эпоха с
// needsFullRebuild, а удаляемый файл остаётся единственной копией данных.
// Значит, инвариант обязан действовать и на ней: файл старой эпохи переживает
// открытие и виден как оставленный.
func TestStartKeepsOldEpochOnSchemaFromFuture(t *testing.T) {
	root := t.TempDir()
	const project = domain.ProjectID("project")
	opts := Options{ProjectID: project, StateDirName: testStateDir}
	s, err := Open(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if err := tx.UpsertComponent(Component{ID: "cfg", Kind: "configuration", Root: "."}); err != nil {
			return err
		}
		return tx.SetMeta(metaSchemaVersion, "99")
	}); err != nil {
		t.Fatal(err)
	}
	oldPath := s.currentPath()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(oldPath)
	if err != nil {
		t.Fatal(err)
	}

	s2, err := Open(root, opts)
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}
	t.Cleanup(func() { s2.Close() })
	st, err := s2.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Epoch != 2 || !st.NeedsFullRebuild {
		t.Fatalf("эпоха %d, needsFullRebuild=%v, ожидались 2 и true", st.Epoch, st.NeedsFullRebuild)
	}
	after, statErr := os.Stat(oldPath)
	if statErr != nil {
		t.Fatalf("эпоха с данными удалена веткой «схема из будущего»: %v", statErr)
	}
	if after.Size() != before.Size() {
		t.Errorf("размер старой эпохи изменился: было %d Б, стало %d Б", before.Size(), after.Size())
	}
	if len(st.KeptEpochs) != 1 || st.KeptEpochs[0] != epochName(project, 1) {
		t.Errorf("оставленная эпоха 1 не отражена в статусе: %v", st.KeptEpochs)
	}
	if st.RecoveryNote == "" {
		t.Error("отказ уборки не объяснён в RecoveryNote")
	}
}

// Сборка не пишет поверх опубликованной эпохи: номер, который называет
// указатель, отменяет сборку ошибкой вместо удаления файла.
func TestBuildEpochRefusesPublishedNumber(t *testing.T) {
	root := t.TempDir()
	const project = domain.ProjectID("project")
	s, err := Open(root, Options{ProjectID: project, StateDirName: testStateDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	path := s.currentPath()

	if _, err := s.buildEpoch(context.Background(), s.epoch, buildDeliberate, nil); err == nil {
		t.Fatal("сборка поверх опубликованной эпохи прошла")
	} else if !strings.Contains(err.Error(), epochName(project, 1)) {
		t.Errorf("ошибка не называет опубликованную эпоху: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("файл опубликованной эпохи удалён отменённой сборкой: %v", err)
	}
}

// Нечитаемый указатель — это ровно случай «доказательства непубликации нет»:
// даже сознательная пересборка не имеет права снести существующий файл с
// данными под номером собираемой эпохи.
//
// Стенд собирается производственным путём (Open, затем Rebuild): указатель
// портится уже под работающим хранилищем — единственный способ, которым
// нечитаемый указатель вообще доходит до сборки, ведь Open на таком указателе
// не поднимается.
func TestBuildEpochKeepsDataWhenPointerUnreadable(t *testing.T) {
	root := t.TempDir()
	const project = domain.ProjectID("project")
	dataPath := makeEpochWithData(t, root, project)
	dir := filepath.Dir(dataPath)
	// Копия эпохи 1 под номером 2: неопубликованный файл с данными ровно там,
	// куда пойдёт следующая пересборка.
	unpublished := filepath.Join(dir, epochName(project, 2))
	copyEpochFile(t, dataPath, unpublished)

	s, err := Open(root, Options{ProjectID: project, StateDirName: testStateDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	// Каталог на месте файла указателя: os.ReadFile отдаёт ошибку доступа, а не
	// «указателя нет», то есть про публикацию не известно ничего.
	ptrPath := filepath.Join(dir, pointerName)
	if err := os.Remove(ptrPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ptrPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, rErr := newPointer(dir).Read(); rErr == nil || errors.Is(rErr, ErrNoPointer) {
		t.Fatalf("каталог на месте указателя прочитался как %v: сцена не собралась, "+
			"и проверка инварианта не состоялась", rErr)
	}

	if err := s.Rebuild(context.Background(), func(tx *WriteTx) error { return nil }); err == nil {
		t.Fatal("пересборка при нечитаемом указателе прошла поверх эпохи с данными")
	}
	if _, err := os.Stat(unpublished); err != nil {
		t.Fatalf("эпоха с данными удалена сборкой при нечитаемом указателе: %v", err)
	}
	if got := componentsInEpochFile(t, unpublished); got != 1 {
		t.Errorf("данные эпохи потеряны: компонентов %d, ожидался 1", got)
	}
}

// componentsInEpochFile читает файл эпохи напрямую и строго на чтение: проверка
// «файл цел» обязана смотреть на содержимое, а не на совпадение размеров —
// заново созданная пустая эпоха весит ровно столько же.
func componentsInEpochFile(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open(driverName, dsn(path)+"?mode=ro")
	if err != nil {
		t.Fatalf("открытие эпохи %s: %v", path, err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM component`).Scan(&n); err != nil {
		t.Fatalf("чтение component из %s: %v", path, err)
	}
	return n
}

// Единственная ветка, где решает именно таблица meta, а не заголовок файла:
// SQLite-база с таблицами, но без схемы эпохи. Это остаток прерванной сборки —
// createConn успел создать файл, applySchema до meta не дошёл. Без этой
// проверки поломка пробы деградировала бы в «данные есть» незаметно.
func TestEpochHasDataFalseForSQLiteWithoutMeta(t *testing.T) {
	path := filepath.Join(t.TempDir(), epochName("project", 1))
	db, err := sql.Open(driverName, dsn(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(),
		`CREATE TABLE component(id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if ok, hErr := hasSQLiteHeader(path); hErr != nil || !ok {
		t.Fatalf("файл не выглядит базой SQLite (ok=%v err=%v), тест ничего не проверяет", ok, hErr)
	}
	if epochHasData(path) {
		t.Error("файл без таблицы meta признан эпохой с данными")
	}
	if err := removeUnpublishedEpoch(path, epochRemoveAttempts, epochRemovePause); err != nil {
		t.Errorf("остаток без таблицы meta не убран: %v", err)
	}
}

// copyEpochFile делает побайтовую копию файла эпохи под другим номером: копия
// валидной эпохи — это валидная эпоха с данными, только неопубликованная.
func copyEpochFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Сборка со старта — аварийная, а не сознательная: преемник пустой, и терять
// под его номером чужие данные нельзя. Сцена: эпоха 1 со схемой из будущего и
// неопубликованная эпоха 2 с данными. Проверяется содержимое, а не размер:
// заново созданная пустая эпоха занимает то же имя и почти тот же объём.
func TestStartKeepsUnpublishedDataEpochOnSchemaFromFuture(t *testing.T) {
	root := t.TempDir()
	const project = domain.ProjectID("project")
	opts := Options{ProjectID: project, StateDirName: testStateDir}
	s, err := Open(root, opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if err := tx.UpsertComponent(Component{ID: "cfg", Kind: "configuration", Root: "."}); err != nil {
			return err
		}
		return tx.SetMeta(metaSchemaVersion, "99")
	}); err != nil {
		t.Fatal(err)
	}
	dir := s.dir
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	unpublished := filepath.Join(dir, epochName(project, 2))
	copyEpochFile(t, filepath.Join(dir, epochName(project, 1)), unpublished)
	if got := componentsInEpochFile(t, unpublished); got != 1 {
		t.Fatalf("в неопубликованной эпохе %d компонентов, тест ничего не проверяет", got)
	}

	s2, err := Open(root, opts)
	if err != nil {
		t.Fatalf("повторное открытие: %v", err)
	}
	t.Cleanup(func() { s2.Close() })
	st, err := s2.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.NeedsFullRebuild {
		t.Error("после несовместимой схемы не запрошена полная сборка индекса")
	}
	if st.EpochPath == unpublished {
		t.Fatalf("аварийная сборка заняла номер эпохи с данными: %s", st.EpochPath)
	}
	if got := componentsInEpochFile(t, unpublished); got != 1 {
		t.Errorf("данные неопубликованной эпохи 2 уничтожены: компонентов %d, ожидался 1", got)
	}
	if !containsString(st.KeptEpochs, epochName(project, 2)) {
		t.Errorf("эпоха 2 с данными не отражена как оставленная: %v", st.KeptEpochs)
	}
	if containsString(st.RemovedOrphans, epochName(project, 2)) {
		t.Errorf("эпоха 2 с данными числится удалённой: %v", st.RemovedOrphans)
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Аварийная сборка отличается от сознательной судьбой чужого файла под её
// номером: пустой преемник права уничтожить данные не имеет, даже когда
// указатель прочитан и называет другую эпоху опубликованной.
func TestBuildEmergencyEpochRefusesNumberWithData(t *testing.T) {
	root := t.TempDir()
	const project = domain.ProjectID("project")
	dataPath := makeEpochWithData(t, root, project)
	unpublished := filepath.Join(filepath.Dir(dataPath), epochName(project, 2))
	copyEpochFile(t, dataPath, unpublished)

	s, err := Open(root, Options{ProjectID: project, StateDirName: testStateDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if published, _, pErr := s.ptr.Read(); pErr != nil || published != epochName(project, 1) {
		t.Fatalf("указатель: payload=%q err=%v, сцена требует читаемого указателя на эпоху 1", published, pErr)
	}

	if _, err := s.buildEpoch(context.Background(), 2, buildEmergency, nil); err == nil {
		t.Fatal("аварийная сборка заняла номер эпохи с данными")
	}
	if got := componentsInEpochFile(t, unpublished); got != 1 {
		t.Errorf("данные эпохи 2 уничтожены аварийной сборкой: компонентов %d, ожидался 1", got)
	}
}
