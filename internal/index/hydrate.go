package index

import (
	"context"
	"fmt"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Гидратация корпуса (ADR-028). Резидентный корпус живёт в памяти процесса и
// наполняется только индексацией, поэтому после перезапуска он пуст, и
// precheckChangedCount объявлял изменившимися ВСЕ файлы проекта: на реальной
// выгрузке это фоновая полная пересборка и stale_index на ровном месте после
// каждого старта. Всё нужное для фингерпринта уже лежит в индексе —
// source_file(rel_path,size,mtime_ns,content_hash,parser_version), — и
// восстанавливается отсюда при первом обращении к компоненту.
//
// Разобранные факты сюда НЕ тянутся: гидратированная запись несёт только
// фингерпринт, чего хватает precheck-у и отбору toRead, и физически не
// годится buildEnvInput (инвариант там же).

// ensureHydrated отдаёт резидентный корпус компонента, при первом обращении
// восстановив его из source_file активной эпохи.
//
// Вызывать с захваченным opMu и ВНЕ открытой write-транзакции store: гидратация
// открывает собственную read-транзакцию.
func (s *Service) ensureHydrated(ctx context.Context, id domain.ComponentID) *componentCorpus {
	c := s.corpusFor(id)
	if c.hydrationDone {
		return c
	}
	// Отметка ставится независимо от исхода: пустой индекс, эпоха в карантине
	// и нечитаемый source_file — не повод бить в store на каждом precheck.
	// Компонент в этом случае индексируется как в первый раз (R12.1);
	// диагностика у каждого из трёх исходов своя, см. ниже.
	c.hydrationDone = true
	if len(c.files) > 0 {
		// Корпус уже наполнен пайплайном этого процесса — восстанавливать
		// нечего, и затирать разобранные факты фингерпринтом нельзя.
		return c
	}

	rows, err := s.sourceFilesOf(ctx, id)
	if err == nil && len(rows) == 0 {
		// Пустой индекс отказом гидратации НЕ считается: store ответил, просто
		// компонент ещё ни разу не индексировался — штатное первое обращение,
		// а не сбой (ADR-028 п.6). Молчать о нём всё же нельзя: дальше весь
		// компонент будет прочитан с диска, и причину этого видно здесь.
		s.appendDiagnostic(domain.Diagnostic{
			Code:      "index_corpus_not_hydrated",
			Severity:  domain.SeverityInfo,
			Component: id,
			Message: fmt.Sprintf(
				"в индексе нет ни одного файла компонента %s — компонент индексируется впервые", id),
		})
		return c
	}
	if err != nil {
		s.appendDiagnostic(domain.Diagnostic{
			Code:      "index_corpus_hydration_failed",
			Severity:  domain.SeverityWarning,
			Component: id,
			Message: fmt.Sprintf(
				"корпус компонента %s не восстановлен из индекса (%v) — компонент будет проиндексирован заново", id, err),
		})
		return c
	}
	for _, r := range rows {
		c.files[r.RelPath] = &fileRecord{
			relPath:       r.RelPath,
			size:          r.Size,
			mtimeNS:       r.MtimeNS,
			contentHash:   r.ContentHash,
			parserVersion: r.ParserVersion,
			hydrated:      true,
		}
	}
	return c
}

// sourceFilesOf — одна read-транзакция за строками source_file компонента.
func (s *Service) sourceFilesOf(ctx context.Context, id domain.ComponentID) ([]store.SourceFileState, error) {
	var rows []store.SourceFileState
	err := s.st.Read(ctx, func(tx *store.ReadTx) error {
		var readErr error
		rows, readErr = tx.SourceFilesByComponent(string(id))
		return readErr
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// appendDiagnostic докладывает диагностику, возникшую вне прогона пайплайна
// (гидратация): её видно через Status, пока следующий Reindex не заменит
// список своим. Персистентный список эпохи она НЕ вытесняет — Status сливает
// оба, пока прогон в этом процессе не доведён до конца (s.reindexedHere).
func (s *Service) appendDiagnostic(d domain.Diagnostic) {
	s.stateMu.Lock()
	s.lastDiagnostics = append(s.lastDiagnostics, d)
	s.stateMu.Unlock()
}
