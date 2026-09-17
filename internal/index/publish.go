package index

import (
	"encoding/json"
	"fmt"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/parse/bsl"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// publishInput — всё, что нужно publishFiles для одного прохода write-
// транзакции (§17 п.8): республикуемые файлы (изменённые + затронутые
// resolve-дельтой) и удалённые.
type publishInput struct {
	project   domain.ProjectID
	component domain.ComponentID
	layer     domain.Layer

	corpus  *componentCorpus
	raw     map[string][]byte // relPath -> сырые байты, только для republish
	resolve map[string][]resolvedRef
	// env — тот же Env, что построил resolve-шаг: derive-функции таска 08
	// (DeriveRegisterAccess/DeriveHandlerBinding/DeriveQueryReference/
	// DeriveDependencyEdges) вызываются здесь же, при публикации, потому что
	// им нужен Env, а не только уже посчитанные resolvedRef.
	env resolve.Env

	// tunables — пороги атрибуции объектного графа (§5): доезжают сюда из
	// флагов -graph-* через index.Config. Нулевая структура нормализуется
	// самим дериватором.
	tunables resolve.ObjectEdgeTunables

	republish []string // отсортированные relPath, для которых нужен свежий INSERT
	removed   []string // отсортированные relPath, чьи факты нужно только удалить
}

// txNodeCache — карта identity_key -> node_id, живущая на ВСЮ write-
// транзакцию (не на файл): заполняется по мере вставки узлов в проходе 1
// (символы, объекты метаданных, члены, формы, элементы, команды) и читается
// во всех последующих шагах ВМЕСТО tx.NodeID для целей ВНУТРИ этой же
// транзакции. Без неё publishReference звал бы tx.NodeID (SELECT) на каждую
// ссылку и каждого ambiguous-кандидата — ~1.8 млн лишних SELECT на полной
// пересборке ut_demo (найдено ревью craft). tx.NodeID остаётся резервом
// ТОЛЬКО для целей вне этой транзакции (узлы уже опубликованных файлов из
// прошлых поколений, не тронутых этим инкрементом).
type txNodeCache struct {
	byKey map[string]int64
}

func newTxNodeCache() *txNodeCache { return &txNodeCache{byKey: make(map[string]int64)} }

func (c *txNodeCache) remember(identityKey string, id int64) { c.byKey[identityKey] = id }

// lookup отдаёт id узла по identity_key: сперва из кэша транзакции, при
// промахе — через tx.NodeID (узел из файла, не тронутого этим инкрементом).
func (c *txNodeCache) lookup(tx *store.WriteTx, identityKey string) (int64, bool, error) {
	if id, ok := c.byKey[identityKey]; ok {
		return id, true, nil
	}
	id, ok, err := tx.NodeID(identityKey)
	if err != nil {
		return 0, false, err
	}
	if ok {
		c.byKey[identityKey] = id
	}
	return id, ok, nil
}

func (c *txNodeCache) lookupSymbol(tx *store.WriteTx, uid domain.SymbolUID) (int64, bool, error) {
	return c.lookup(tx, symbolIdentityKey(uid))
}

// publishOutcome — что удалось опубликовать, для validate-шага и Result.
type publishOutcome struct {
	diagnostics []domain.Diagnostic
	symbolCount int
	fileCount   int
	counts      publishCounts
}

// modulePublishState — то, что первый проход publishFiles насчитал для
// файла-модуля и что нужно второму проходу (ссылки ссылаются на символы,
// в том числе из ДРУГИХ файлов той же транзакции — символ-цель обязан уже
// существовать в БД к моменту вставки reference, поэтому вставка символов
// ВСЕХ файлов транзакции идёт раньше вставки ссылок ЛЮБОГО файла).
type modulePublishState struct {
	fileID         int64
	moduleID       int64
	methodSymbolID map[int]int64
}

// txState — сквозное состояние одной write-транзакции, которое проходу 2 и
// derive-публикации (formы/подписки/задания/роли уже в проходе 1, но register_access/
// handler_binding/query_reference/dependency_edge — только после прохода 1,
// см. publishDerivedFacts) нужно знать про проход 1: кэш узлов символов и
// identity объектов метаданных/членов (для DeriveDependencyEdges и
// DeriveQueryReference — их FromKey/ObjectKey/MemberKey уже строка identity_key,
// но узел нужно найти по ней так же, как по symbol_uid).
type txState struct {
	nodes    *txNodeCache
	roleFile map[string]int64 // component+"\x00"+roleNameNorm -> file_id роли (для role_right.origin_file_id логики, см. publishmeta2.go)

	// fileID — relPath -> id файла, вставленного проходом 1. Проходу 2 и
	// публикации объектного графа id нужен там, где у них на руках только
	// путь (XML документа не несёт modulePublishState), а второй SELECT
	// SourceFileID за уже полученным id — лишний.
	fileID map[string]int64

	// staleFiles — id файлов, снесённых этой транзакцией (переопубликованных
	// и удалённых), в том виде, в каком они были ДО удаления. По ним видно,
	// была ли переопубликована вся цепочка снесённого ребра.
	staleFiles map[int64]struct{}
	// staleEdges — рёбра, снесённые этой транзакцией по файловой зависимости,
	// снятые ДО удаления. publishedEdges — то, что публикация вернула. Их
	// разность и есть связи, пропавшие с карты в этом прогоне.
	staleEdges     []store.ObjectDataEdgeDeps
	publishedEdges map[store.ObjectDataEdgeKey]struct{}
	// carriedStale — признаки устаревшей атрибуции прошлых прогонов, чья
	// причина не снята: их снимет уборка бейджей, и переставить их обязан
	// тот же проход.
	carriedStale map[staleBadgeKey]int64
	// republishedOwners — владельцы, чьи СОБСТВЕННЫЕ модули переопубликованы
	// этой транзакцией. Их атрибуция посчитана заново от начала, поэтому
	// отсутствие ребра у них честное, а не устаревшее.
	republishedOwners map[int64]struct{}

	// registerAccess — строки register_access, вставленные ЭТОЙ транзакцией
	// (то есть строки republish-нутых файлов), в том же виде, в каком их
	// читает store. Публикация объектного графа берёт их отсюда, а не
	// вторым SELECT-ом: на полной пересборке это разница между одним
	// проходом и запросом на каждый символ.
	registerAccess []store.RegisterAccessRow

	// counts — сколько строк каждого вида факта РЕАЛЬНО вставлено успешным
	// tx.InsertXxx (не len() входного среза до цикла): счётчик по месту
	// вставки ловит именно пропажу факта при публикации — тот класс регрессии,
	// который snapshot corpus/resolved (property-тест) не видит, потому что
	// сравнивает состояние ДО publish, не то, что реально осело в store
	// (найдено ревью мутацией publishRegisterAccess). См. publishcounts.go.
	counts publishCounts
}

// publishCounts — сколько строк каждого нового (этот раунд правок) вида
// факта вставлено в store. Считается ТОЛЬКО в publishXxx-функциях, в момент
// успешного tx.InsertXxx — не пересчитывается из resolve.DeriveXxx отдельно,
// иначе счётчик страховал бы сам себя.
type publishCounts struct {
	registerAccess int
	handlerBinding int
	roleRight      int
	eventSub       int
	scheduledJob   int
	query          int
	queryReference int
	formElement    int
	formCommand    int
	dependencyEdge int
	objectEdge     int
	objectBadge    int
}

// publishFiles пишет одну write-транзакцию (§17 п.8, §18.1): удаляет старые
// факты изменённых/удалённых файлов, вставляет новые. COMMIT делает сам
// store.Write/store.Rebuild — здесь только шаги (1)-(4) раздела 15,
// оговорённые как «дело вызывающего».
func publishFiles(tx *store.WriteTx, in publishInput) (publishOutcome, error) {
	var out publishOutcome

	all := make([]string, 0, len(in.republish)+len(in.removed))
	all = append(all, in.republish...)
	all = append(all, in.removed...)
	staleIDs := make([]int64, 0, len(all))
	for _, rel := range all {
		id, ok, err := tx.SourceFileID(string(in.component), rel)
		if err != nil {
			return out, fmt.Errorf("поиск файла %s: %w", rel, err)
		}
		if ok {
			staleIDs = append(staleIDs, id)
		}
	}
	ts := &txState{nodes: newTxNodeCache(), roleFile: make(map[string]int64),
		fileID:         make(map[string]int64, len(in.republish)),
		staleFiles:     make(map[int64]struct{}, len(staleIDs)),
		publishedEdges: make(map[store.ObjectDataEdgeKey]struct{}),
	}
	for _, id := range staleIDs {
		ts.staleFiles[id] = struct{}{}
	}

	// Состав сносимых рёбер снимается ДО удаления и одним запросом: после
	// DELETE сравнивать «было» будет не с чем, а сравнить обязательно —
	// связь, снесённая по файловой зависимости и не восстановленная
	// атрибуцией этого же прогона, иначе исчезла бы с карты молча.
	staleEdges, err := tx.ObjectDataEdgesDependingOnFiles(staleIDs...)
	if err != nil {
		return out, fmt.Errorf("состав сносимых объектных рёбер: %w", err)
	}
	ts.staleEdges = staleEdges

	// Рёбра объектного графа сносятся ДО файлов и обязательно раньше них
	// (ADR-24): каскад по object_data_edge_dep.file_id снял бы строки
	// зависимостей вместе с файлом и оставил ребро без единой зависимости,
	// то есть неудаляемым никаким последующим прогоном.
	if err := tx.DeleteObjectEdgesForFiles(staleIDs...); err != nil {
		return out, fmt.Errorf("удаление объектных рёбер: %w", err)
	}
	for _, id := range staleIDs {
		if err := tx.DeleteSourceFiles(id); err != nil {
			return out, fmt.Errorf("удаление фактов файла %d: %w", id, err)
		}
	}

	// roleRightFileID — fileID файлов Rights.xml, republish-нутых проходом 1,
	// нужен проходу 2 (publishRoleRights туда переехал, см. ниже) — без него
	// пришлось бы повторно бить SourceFileID SELECT-ом за id, который уже
	// был получен вставкой в проходе 1.
	roleRightFileID := make(map[string]int64)

	// Проход 1: файлы, объекты метаданных, identity модулей, символы, формы,
	// подписки, задания, роли — все identity-факты, что не ссылаются на
	// символы/объекты ДРУГИХ файлов той же транзакции.
	modState := make(map[string]modulePublishState, len(in.republish))
	for _, rel := range in.republish {
		rec := in.corpus.files[rel]
		if rec == nil {
			continue
		}
		data := in.raw[rel]
		hash, err := tx.PutBlob(data)
		if err != nil {
			return out, fmt.Errorf("blob %s: %w", rel, err)
		}
		fileID, err := tx.InsertSourceFile(store.SourceFile{
			ComponentID: string(in.component), RelPath: rel,
			Size: rec.size, MtimeNS: rec.mtimeNS, ContentHash: hash, ParserVersion: ParserVersion,
		})
		if err != nil {
			return out, fmt.Errorf("source_file %s: %w", rel, err)
		}
		out.fileCount++
		ts.fileID[rel] = fileID

		for _, d := range rec.diagnostics {
			if err := tx.InsertDiagnostic(toStoreDiagnostic(d, fileID, string(in.component))); err != nil {
				return out, fmt.Errorf("diagnostic %s: %w", rel, err)
			}
			out.diagnostics = append(out.diagnostics, d)
		}

		if rec.metaFacts.Object != nil {
			if err := publishMetadataObject(tx, in, ts, rel, fileID, rec); err != nil {
				return out, err
			}
		}
		if rec.bslModule != nil {
			st, n, diags, err := publishModuleSymbols(tx, in, ts, rel, fileID, rec)
			if err != nil {
				return out, fmt.Errorf("symbols %s: %w", rel, err)
			}
			modState[rel] = st
			out.symbolCount += n
			out.diagnostics = append(out.diagnostics, diags...)
		}
		if rec.metaFacts.FormStructure != nil {
			if err := publishFormStructure(tx, in, ts, rel, fileID, rec); err != nil {
				return out, fmt.Errorf("form_structure %s: %w", rel, err)
			}
		}
		if rec.metaFacts.Subscription != nil {
			if err := publishEventSubscription(tx, in, ts, rel, fileID, rec); err != nil {
				return out, fmt.Errorf("event_subscription %s: %w", rel, err)
			}
		}
		if rec.metaFacts.RoleRights != nil {
			roleRightFileID[rel] = fileID
		}
	}

	// Проход 2: ссылки и derive-факты — цели уже существуют (свои и чужие
	// файлы той же транзакции, символы/объекты неизменённых файлов уже
	// опубликованы раньше). Формы — отдельная ветка: обработчики формы
	// ссылаются на символы её Module.bsl, а тот в проходе 1 по сортировке
	// пути обрабатывается ПОСЛЕ своего Form.xml (".xml" < "/Ext/Form/..."),
	// поэтому handler_binding ждёт того же прохода 2, что и обычные ссылки.
	//
	// publishRoleRights — тоже здесь, а не в проходе 1, по той же причине:
	// resolveRoleObjectNode ищет object_id ЧУЖОГО объекта метаданных
	// (Rights.xml почти всегда лежит алфавитно раньше объекта, на который
	// ссылается его право, например "Roles/..." раньше "Subsystems/...").
	// На инкременте поверх УЖЕ существующего store (в отличие от чистого
	// full-рибилда в пустую эпоху) этот объект в момент прохода 1 может быть
	// удалён (шаг (2) выше, DeleteSourceFiles) и ЕЩЁ не переопубликован —
	// его node сохраняет стабильный id (identity переживает файл), но
	// metadata_object-строка, на которую в реальности ссылается
	// role_right.object_id, временно отсутствует: INSERT падает на FOREIGN
	// KEY (role_right.object_id REFERENCES metadata_object(id), не node).
	// Публикация в проходе 2 даёт то же гарантированное «все цели этой
	// транзакции уже вставлены», что и обычным ссылкам.
	for _, rel := range in.republish {
		rec := in.corpus.files[rel]
		if rec == nil {
			continue
		}
		if rec.bslModule != nil {
			st := modState[rel]
			ownerDiags, err := publishModuleOwner(tx, in, ts, rel, rec, st)
			if err != nil {
				return out, err
			}
			out.diagnostics = append(out.diagnostics, ownerDiags...)
			if err := publishModuleReferences(tx, in, ts, rel, st); err != nil {
				return out, fmt.Errorf("references %s: %w", rel, err)
			}
			if err := publishDerivedModuleFacts(tx, in, ts, rel, st); err != nil {
				return out, fmt.Errorf("derived %s: %w", rel, err)
			}
		}
		if rec.metaFacts.FormStructure != nil {
			if err := publishHandlerBindingsForForm(tx, in, ts, rel, rec); err != nil {
				return out, fmt.Errorf("handler_binding %s: %w", rel, err)
			}
		}
		if rec.metaFacts.RoleRights != nil {
			if err := publishRoleRights(tx, in, ts, rel, roleRightFileID[rel], rec); err != nil {
				return out, fmt.Errorf("role_rights %s: %w", rel, err)
			}
		}
	}
	if err := publishDependencyEdges(tx, in, ts); err != nil {
		return out, fmt.Errorf("dependency_edge: %w", err)
	}
	if err := publishObjectDataEdges(tx, in, ts, modState); err != nil {
		return out, fmt.Errorf("object_data_edge: %w", err)
	}
	out.counts = ts.counts
	return out, nil
}

func toStoreDiagnostic(d domain.Diagnostic, fileID int64, componentID string) store.Diagnostic {
	return store.Diagnostic{
		FileID: fileID, ComponentID: componentID,
		Severity: string(d.Severity), Code: d.Code, Message: d.Message, Span: d.Span,
	}
}

// publishMetadataObject вставляет объект метаданных и его члены, а для
// общего модуля — ещё и аспект module_context на identity BSL-модуля,
// построенную по платформенной конвенции пути (см. commonModuleBSLPath):
// правка ТОЛЬКО XML общего модуля не трогает source_file его Module.bsl,
// поэтому identity модуля и symbol_uid не меняются (R32.3).
func publishMetadataObject(tx *store.WriteTx, in publishInput, ts *txState, rel string, fileID int64, rec *fileRecord) error {
	obj := rec.metaFacts.Object
	objKey := metadataObjectIdentityKey(in.component, obj.MType, obj.NameNorm)
	propsJSON, err := json.Marshal(obj.Props)
	if err != nil {
		return fmt.Errorf("props %s: %w", rel, err)
	}
	objID, err := tx.EnsureMetadataObject(store.MetadataObject{
		IdentityKey: objKey, ComponentID: string(in.component), UUID: obj.UUID, MType: obj.MType,
		NameNorm: obj.NameNorm, NameDisplay: obj.NameDisplay, Synonym: obj.Synonym,
		FileID: fileID, PropsJSON: string(propsJSON), Layer: layerName(in.layer),
	})
	if err != nil {
		return fmt.Errorf("metadata_object %s: %w", rel, err)
	}
	ts.nodes.remember(objKey, objID)
	for _, m := range rec.metaFacts.Members {
		typesJSON, err := json.Marshal(m.Types)
		if err != nil {
			return fmt.Errorf("member types %s: %w", rel, err)
		}
		memberKey := metadataMemberIdentityKey(objKey, m)
		memberID, err := tx.InsertMetadataMember(store.MetadataMember{
			IdentityKey: memberKey, ComponentID: string(in.component),
			ObjectID: objID, OriginFileID: fileID, Kind: m.Kind, NameNorm: m.NameNorm,
			NameDisplay: m.NameDisplay, TypesJSON: string(typesJSON),
		})
		if err != nil {
			return fmt.Errorf("metadata_member %s/%s: %w", rel, m.NameNorm, err)
		}
		ts.nodes.remember(memberKey, memberID)
	}
	if len(rec.metaFacts.FormDecls) > 0 {
		if err := publishFormDecls(tx, in, ts, rel, fileID, objID, rec.metaFacts.FormDecls); err != nil {
			return err
		}
	}
	if obj.MType == "ScheduledJob" && rec.metaFacts.ScheduledJob != nil {
		if err := publishScheduledJob(tx, in, ts, rel, fileID, rec); err != nil {
			return fmt.Errorf("scheduled_job %s: %w", rel, err)
		}
	}
	if obj.MType == "Role" && rec.metaFacts.Role != nil {
		if err := publishRole(tx, in, ts, rel, fileID, objID, rec); err != nil {
			return fmt.Errorf("role %s: %w", rel, err)
		}
	}

	if obj.MType == "CommonModule" && rec.metaFacts.ModuleRegistry != nil {
		bslPath := commonModuleBSLPath(obj.NameDisplay)
		// Та же строка module, что напишет publishModuleSymbols, когда до
		// Module.bsl этого общего модуля дойдёт очередь: EnsureModule —
		// безусловный upsert по всем колонкам, поэтому обе точки идут
		// через moduleRecord. ModuleInfo здесь собирается из объекта, а не
		// из пути: XML владельца — единственный источник NameDisplay, и
		// владелец у общего модуля это он сам.
		moduleID, err := tx.EnsureModule(moduleRecord(in, bslPath, bsl.ModuleInfo{
			Kind: bsl.ModuleCommon, OwnerType: "CommonModules",
			OwnerName: obj.NameDisplay, OwnerNameNorm: obj.NameNorm,
		}, objID))
		if err != nil {
			return fmt.Errorf("module (common, XML) %s: %w", rel, err)
		}
		propsJSON, err := json.Marshal(rec.metaFacts.ModuleRegistry)
		if err != nil {
			return fmt.Errorf("module_context props %s: %w", rel, err)
		}
		if err := tx.PutModuleContext(moduleID, fileID, string(propsJSON)); err != nil {
			return fmt.Errorf("module_context %s: %w", rel, err)
		}
	}
	return nil
}

// moduleRecord — identity-строка модуля кода. Одна на обе точки вызова
// EnsureModule для одного и того же Module.bsl (проход 1 без владельца,
// проход 2 с владельцем): EnsureModule — безусловный upsert по всем
// колонкам, поэтому разошедшиеся литералы затирали бы поля друг друга
// (тот же довод, что у формы в publishforms.go).
func moduleRecord(in publishInput, rel string, info bsl.ModuleInfo, ownerObjectID int64) store.Module {
	return store.Module{
		IdentityKey: moduleIdentityKey(in.component, rel), ComponentID: string(in.component),
		Kind: string(info.Kind), OwnerObjectID: ownerObjectID,
		NameNorm: info.OwnerNameNorm, NameDisplay: info.OwnerName,
	}
}

// publishModuleOwner дописывает module.owner_object_id (§3, истории 8 и 9)
// после прохода 1: объект-владелец публикуется своим XML-файлом в том же
// проходе и не обязан идти раньше своего Module.bsl, поэтому резолв ключа
// владельца ждёт того же «после прохода 1», по которому уже живут
// register_access, handler_binding, query_reference и dependency_edge.
//
// Три исхода, и они разные:
//   - OwnerNameNorm пуст — модуль приложения, сеанса или внешнего
//     соединения: он лежит вне коллекции выгрузки (bsl.ClassifyModule),
//     объекта-владельца у него нет, NULL это правильный ответ;
//   - коллекция известна, а объекта в индексе нет — NULL молча: висячий id
//     хуже пустого, и это неполнота выгрузки, а не наша;
//   - коллекция НЕ известна словарю ownerTypeToMType — диагностика:
//     словарь ведётся руками, и его пробел обязан быть виден в обычном
//     прогоне, а не только на реальной выгрузке.
//
// Граница диагностики закрывает не весь пробел словаря: НЕВЕРНОЕ значение в
// нём (так «WebServices» долго указывало на несуществующий «WSDefinition»)
// в рантайме неотличимо от «объекта просто нет в выгрузке» и остаётся
// молчаливым. Отличить их здесь нечем: диагностика на «владелец не найден»
// шумела бы на любой частичной выгрузке, где объекта нет по-настоящему.
// Ловит такое только TestRealDumpModuleOwnerFilled на полной выгрузке.
//
// Диагностика возвращается вызывающему тем же путём, что и у
// publishModuleSymbols: вставляется здесь, а в ComponentResult.Diagnostics
// её добавляет publishFiles.
func publishModuleOwner(tx *store.WriteTx, in publishInput, ts *txState, rel string, rec *fileRecord, st modulePublishState) ([]domain.Diagnostic, error) {
	info := rec.moduleInfo
	if info.OwnerNameNorm == "" {
		return nil, nil
	}
	mtype, known := ownerTypeToMType(info.OwnerType)
	if !known {
		diag := domain.Diagnostic{
			Code: "index_module_owner_unknown_collection", Severity: domain.SeverityWarning,
			Message: fmt.Sprintf("коллекция выгрузки %q не переводится в вид объекта метаданных — владелец модуля не определён", info.OwnerType),
			File:    rel,
		}
		if err := tx.InsertDiagnostic(toStoreDiagnostic(diag, st.fileID, string(in.component))); err != nil {
			return nil, fmt.Errorf("diagnostic (владелец модуля) %s: %w", rel, err)
		}
		return []domain.Diagnostic{diag}, nil
	}
	ownerID, found, err := ts.nodes.lookup(tx, metadataObjectIdentityKey(in.component, mtype, info.OwnerNameNorm))
	if err != nil {
		return nil, fmt.Errorf("владелец модуля %s: %w", rel, err)
	}
	if !found {
		return nil, nil
	}
	if _, err := tx.EnsureModule(moduleRecord(in, rel, info, ownerID)); err != nil {
		return nil, fmt.Errorf("module (владелец) %s: %w", rel, err)
	}
	return nil, nil
}

// commonModuleBSLPath строит путь Module.bsl общего модуля по имени объекта
// — платформенная конвенция выгрузки (CommonModules/<Имя>/Ext/Module.bsl),
// та же, что распознаёт bsl.ClassifyModule в обратную сторону.
func commonModuleBSLPath(nameDisplay string) string {
	return "CommonModules/" + nameDisplay + "/Ext/Module.bsl"
}

func layerName(l domain.Layer) string {
	if l.IsBase() {
		return "base"
	}
	return string(l.Component)
}

// publishModuleSymbols вставляет identity модуля и его символы/параметры.
// Ссылки вставляются отдельно, после того как символы ВСЕХ файлов
// транзакции опубликованы (см. publishFiles). Диагностики, обнаруженные
// только здесь (index_duplicate_symbol_uid — раньше в pass 1 rec.diagnostics
// её ещё нет, см. комментарий у InsertDiagnostic ниже), возвращаются
// отдельным срезом и добавляются вызывающим в out.diagnostics — тем же
// путём, каким уже идёт rec.diagnostics (publishFiles, до вызова этой
// функции), чтобы ComponentResult.Diagnostics не занижал то, что реально
// осело в store (см. reactivation_test.go, doc-комментарий находки).
func publishModuleSymbols(tx *store.WriteTx, in publishInput, ts *txState, rel string, fileID int64, rec *fileRecord) (modulePublishState, int, []domain.Diagnostic, error) {
	// Владелец здесь ещё не известен: его объект метаданных может
	// публиковаться позже своего Module.bsl в том же проходе 1. Колонку
	// дописывает publishModuleOwner после прохода 1.
	moduleID, err := tx.EnsureModule(moduleRecord(in, rel, rec.moduleInfo, 0))
	if err != nil {
		return modulePublishState{}, 0, nil, fmt.Errorf("module: %w", err)
	}
	if err := tx.PutModuleCode(moduleID, fileID); err != nil {
		return modulePublishState{}, 0, nil, fmt.Errorf("module_code: %w", err)
	}

	symbols := buildSymbols(in.project, in.component, in.layer, rel, rec.bslModule)
	st := modulePublishState{fileID: fileID, moduleID: moduleID, methodSymbolID: make(map[int]int64, len(rec.bslModule.Methods))}
	// insertedByUID — дедупликация по symbol_uid внутри файла: реальная
	// выгрузка содержит методы/переменные, объявленные дважды под взаимно
	// исключающими ветками #Если/#Иначе (например, обычное и управляемое
	// приложение) — компилируется ровно одна ветка, но парсер (таск 05)
	// видит обе текстово, и domain.NewSymbolUID сознательно не включает
	// span (§14: uid = hash(project,component,module_path,name_norm)).
	// Без дедупликации node.identity_key/symbol.id ловят UNIQUE constraint
	// (найдено на реальной выгрузке ut_demo). Упрощение: побеждает первое
	// по тексту файла объявление, остальные помечаются diagnostic и не
	// получают своей строки symbol — их вызовы резолвятся на ту же id.
	insertedByUID := make(map[domain.SymbolUID]int64, len(symbols))
	var diags []domain.Diagnostic
	for i, sym := range symbols {
		if dupID, dup := insertedByUID[sym.UID]; dup {
			diag := domain.Diagnostic{
				Code: "index_duplicate_symbol_uid", Severity: domain.SeverityWarning,
				Message: fmt.Sprintf("имя %q объявлено в файле повторно (вероятно, взаимоисключающие ветки #Если) — учтено первое объявление", sym.NameDisplay),
				File:    rel, Span: sym.Span,
			}
			// Диагностики файла уже вставлены раньше (publishFiles, проход
			// 1, до вызова этой функции) — эта обнаруживается только здесь,
			// поэтому вставляется отдельно, а не через rec.diagnostics. В
			// возвращаемый diags кладём наравне с rec.diagnostics — вызывающий
			// (publishFiles) добавит их в out.diagnostics тем же путём.
			if err := tx.InsertDiagnostic(toStoreDiagnostic(diag, fileID, string(in.component))); err != nil {
				return modulePublishState{}, 0, nil, fmt.Errorf("diagnostic (duplicate symbol) %s: %w", rel, err)
			}
			diags = append(diags, diag)
			if i < len(rec.bslModule.Methods) {
				st.methodSymbolID[i] = dupID
			}
			continue
		}
		id, err := tx.InsertSymbol(store.Symbol{
			IdentityKey: symbolIdentityKey(sym.UID), ComponentID: string(in.component), UID: string(sym.UID),
			ModuleID: moduleID, OriginFileID: fileID, Kind: string(sym.Kind), NameNorm: sym.NameNorm,
			NameDisplay: sym.NameDisplay, IsExport: sym.Export, Directive: sym.Directive, IsAsync: sym.Async,
			Span: sym.Span, Signature: signatureOf(sym),
		})
		if err != nil {
			return modulePublishState{}, 0, nil, fmt.Errorf("symbol %s: %w", sym.NameDisplay, err)
		}
		insertedByUID[sym.UID] = id
		ts.nodes.remember(symbolIdentityKey(sym.UID), id)
		for _, p := range sym.Params {
			if err := tx.InsertParameter(id, store.Parameter{
				Ord: p.Index, Name: p.NameDisplay, ByVal: p.ByValue, DefaultExpr: p.Default,
			}); err != nil {
				return modulePublishState{}, 0, nil, fmt.Errorf("parameter %s: %w", p.NameDisplay, err)
			}
		}
		if i < len(rec.bslModule.Methods) {
			st.methodSymbolID[i] = id
		}
	}
	return st, len(insertedByUID), diags, nil
}

// signatureOf собирает короткую сигнатуру символа для FTS/выдачи —
// упрощение: имена параметров через запятую, без типов (типы BSL не
// аннотируются в исходнике).
func signatureOf(s domain.Symbol) string {
	sig := s.NameDisplay + "("
	for i, p := range s.Params {
		if i > 0 {
			sig += ", "
		}
		sig += p.NameDisplay
	}
	return sig + ")"
}

// refCallMethodIndexes возвращает Method-индекс (в терминах mod.Methods) для
// каждого RefCall в том же порядке, в котором resolve.BuildRawRefs строит
// RawRef из mod.References: обе функции проходят mod.References по порядку и
// пропускают всё, кроме RefCall, поэтому i-й элемент здесь соответствует i-й
// записи BuildRawRefs. Упрощение: RawRef (таск 08) не несёт Method-индекс
// сам, реконструкция — минимальная дублирующая функция, а не второй парсер.
func refCallMethodIndexes(mod *bsl.Module) []int {
	out := make([]int, 0, len(mod.References))
	for _, ref := range mod.References {
		if ref.Kind != bsl.RefCall {
			continue
		}
		out = append(out, ref.Method)
	}
	return out
}

// publishModuleReferences вставляет reference/call_edge/reference_candidate/
// resolution_dep файла rel. Требует, чтобы символы ВСЕХ файлов транзакции
// (не только rel) были уже вставлены — иначе ссылка на символ соседнего
// файла той же транзакции не найдёт свою цель.
func publishModuleReferences(tx *store.WriteTx, in publishInput, ts *txState, rel string, st modulePublishState) error {
	rec := in.corpus.files[rel]
	refs := in.resolve[rel]
	methodOf := refCallMethodIndexes(rec.bslModule)
	for i, rr := range refs {
		var callerSymbolID int64
		if i < len(methodOf) {
			if id, ok := st.methodSymbolID[methodOf[i]]; ok {
				callerSymbolID = id
			}
		}
		refID, targetSymbolID, err := publishReference(tx, in, ts, st.fileID, callerSymbolID, rr)
		if err != nil {
			return fmt.Errorf("reference: %w", err)
		}
		if callerSymbolID != 0 {
			kind := rr.result.CallKind
			if kind == "" {
				// FormQualifiedModule на неопознанном квалификаторе
				// (resolve.resolveModuleCall, ветка dynamic) не
				// проставляет CallKind — резолвер посчитал это вне
				// рамок §15 call_edge.kind, здесь ближайший подходящий
				// смысл готового словаря resolve.CallEdgeKind.
				kind = resolve.CallDynamic
			}
			if err := tx.InsertCallEdge(store.CallEdge{
				CallerID: callerSymbolID, CalleeID: targetSymbolID,
				CalleeNameNorm: rr.raw.NameNorm, QualifierNorm: rr.raw.QualifierNorm,
				Kind: string(kind), Resolution: string(rr.result.Resolution),
				Confidence: float64(rr.result.Confidence), RefID: refID,
			}); err != nil {
				return fmt.Errorf("call_edge: %w", err)
			}
		}
	}
	return nil
}

// publishReference вставляет одну ссылку (reference/reference_candidate/
// resolution_dep) и возвращает её id и id узла-символа цели (0, если цель —
// не символ или ссылка не разрешена). target_node_id кандидатов ambiguous
// ищется по identity — кандидат без узла (символ ещё не проиндексирован в
// этой транзакции — не должно случаться после двухпроходной публикации, но
// защита дешева) пропускается: сам факт ambiguous от этого не рушится.
func publishReference(tx *store.WriteTx, in publishInput, ts *txState, fileID, callerSymbolID int64, rr resolvedRef) (refID int64, targetSymbolID int64, err error) {
	r := store.Reference{
		FileID: fileID, FromSymbolID: callerSymbolID, Kind: "call",
		QualifierNorm: rr.raw.QualifierNorm, NameNorm: rr.raw.NameNorm,
		Resolution: string(rr.result.Resolution), Confidence: float64(rr.result.Confidence),
		Layer: layerName(in.layer), Span: rr.raw.Span,
	}
	if rr.result.Resolution == domain.ResolutionResolved {
		switch rr.result.TargetClass {
		case domain.TargetSymbol:
			id, ok, lookupErr := ts.nodes.lookupSymbol(tx, rr.result.TargetUID)
			if lookupErr != nil {
				return 0, 0, lookupErr
			}
			if ok {
				r.TargetClass = "symbol"
				r.TargetSymbolID = id
				targetSymbolID = id
			} else {
				r.Resolution = string(domain.ResolutionUnresolved)
			}
		case domain.TargetPlatform:
			r.TargetClass = "platform"
			r.PlatformKey = rr.result.TargetKey
		}
	}
	refID, err = tx.InsertReference(r)
	if err != nil {
		return 0, 0, err
	}
	for i, c := range rr.result.Candidates {
		if c.TargetClass != domain.TargetSymbol {
			continue
		}
		nodeID, ok, lookupErr := ts.nodes.lookupSymbol(tx, c.TargetUID)
		if lookupErr != nil {
			return 0, 0, lookupErr
		}
		if !ok {
			continue
		}
		if err := tx.InsertReferenceCandidate(refID, nodeID, i+1, c.Reason); err != nil {
			return 0, 0, err
		}
	}
	for _, k := range rr.result.ConsultedKeys {
		if err := tx.InsertResolutionDep(string(k), refID); err != nil {
			return 0, 0, err
		}
	}
	return refID, targetSymbolID, nil
}
