package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"
)

// sqliteHeaderMagic — первые байты любого файла базы SQLite. Дешёвая отсечка до
// открытия соединения: недописанный остаток сборки этого заголовка не имеет.
var sqliteHeaderMagic = []byte("SQLite format 3\x00")

// errEpochHasData — уборка остановлена инвариантом: в файле эпохи есть данные.
var errEpochHasData = errors.New("файл эпохи содержит данные: удаление запрещено")

// epochHasData отвечает на единственный вопрос: доказуемо ли, что файл — остаток
// прерванной сборки, а не эпоха с данными.
//
// Проверка сознательно асимметрична, и направление отказа выбрано в пользу
// сохранения: «данных нет» возвращается ТОЛЬКО когда это доказано (нулевой
// размер, отсутствие заголовка SQLite, отсутствие таблицы meta). Любая
// неопределённость — ошибка stat, нечитаемый файл, файл, который не открывается
// как база, — читается как «данные есть», потому что цена ложного «нет» это
// уничтоженный индекс, а цена ложного «да» — лишний файл на диске.
func epochHasData(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		// В том числе os.IsNotExist: удалять нечего, и «нет данных» здесь
		// означало бы разрешение на уборку несуществующего.
		return !os.IsNotExist(err)
	}
	if fi.Size() == 0 {
		return false
	}
	ok, err := hasSQLiteHeader(path)
	if err != nil {
		return true
	}
	if !ok {
		return false
	}
	has, err := hasMetaTable(path)
	if err != nil {
		return true
	}
	return has
}

func hasSQLiteHeader(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, len(sqliteHeaderMagic))
	n, err := f.Read(buf)
	if err != nil && n < len(buf) {
		return false, err
	}
	return bytes.Equal(buf[:n], sqliteHeaderMagic), nil
}

// hasMetaTable открывает файл СТРОГО на чтение (mode=ro): проба обязана быть
// наблюдателем и не может ни создать файл, ни доводить чужой WAL.
func hasMetaTable(path string) (bool, error) {
	db, err := sql.Open(driverName, dsn(path)+"?mode=ro")
	if err != nil {
		return false, err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), metaProbeTimeout)
	defer cancel()
	var name string
	err = db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='meta'`).Scan(&name)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// metaProbeTimeout ограничивает пробу: она идёт на пути открытия хранилища, и
// зависнуть на чужом заблокированном файле ей нельзя.
const metaProbeTimeout = 5 * time.Second

// removeUnpublishedEpoch — единственная уборка на пути восстановления.
//
// Инвариант поверх классификации (ADR-023): файл эпохи, в котором есть данные,
// не удаляется ни при каком исходе классификации. Классификация может ошибиться
// — 20.08 она ошиблась и стоила 2.4 ГБ индекса, — поэтому последняя проверка
// стоит здесь, у самого os.Remove, а не только в критерии.
func removeUnpublishedEpoch(path string, attempts int, pause time.Duration) error {
	if epochHasData(path) {
		return fmt.Errorf("%s: %w", path, errEpochHasData)
	}
	return removeEpoch(path, attempts, pause)
}
