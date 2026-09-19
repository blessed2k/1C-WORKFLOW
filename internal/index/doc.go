// Package index собирает пайплайн индексации 1С:
// discover -> fingerprint -> parse -> normalize -> resolve -> derive graph ->
// validate -> publish (архитектура §17), поверх internal/store,
// internal/resolve, internal/parse/* и internal/workspace.
//
// Пакет — единственный писатель индекса: он строит resolve.EnvInput из
// разобранных фактов (в памяти, а не через SQL-выборки — internal/store не
// выставляет их) и публикует результат одной транзакцией
// (internal/store уже даёт атомарность и last-known-good, index здесь
// только вызывающий).
//
// # Публикуемые факты
//
// Публикуются: source_file/blob/diagnostic, module (+module_context/
// module_code), symbol/parameter, reference/call_edge/reference_candidate/
// resolution_dep, metadata_object/metadata_member, form/form_declaration/
// form_structure/form_element/form_command, handler_binding,
// event_subscription, scheduled_job, role/role_right, register_access,
// query (текст/span/staticity), query_reference (только static-литералы —
// resolve.DeriveQueryReference группирует их по литералу, публикация в
// publishderive.go),
// dependency_edge(field-typed-by).
//
// НЕ публикуется, честно (не имитация готовности):
//   - query_reference для partial-текстов (собранных через query.GapMarker
//     из нескольких BSL-фрагментов конкатенации) — сборка полного текста по
//     фрагментам одного выражения не сделана, только группировка
//     static-результата по литералу (см. doc-комментарий
//     resolve.DeriveQueryReference).
//   - dependency_edge(subsystem-contains, exchange-plan-contains,
//     test-references-symbol, epf-uses-object) — три вида не реализованы
//     resolve.DeriveDependencyEdges вовсе (не хватает parse/meta.Content
//     подсистем/планов обмена и cross-component Env), реализован и
//     публикуется только field-typed-by.
package index
