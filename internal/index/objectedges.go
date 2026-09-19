package index

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/blessed2k/1C-WORKFLOW/internal/domain"
	"github.com/blessed2k/1C-WORKFLOW/internal/resolve"
	"github.com/blessed2k/1C-WORKFLOW/internal/store"
)

// Файл — публикация объектного графа (§4 и §5 спецификации В1): рёбра между
// объектами метаданных и бейджи их узлов. Правила атрибуции живут в
// internal/resolve и здесь не повторяются: этот файл отвечает за
// вход дериватора, запись результата и инкрементальность.
//
// Два источника рёбер, и они не смешиваются:
//   - декларация метаданных документа (RegisterRecords) -> writes-declared,
//     provenance metadata-declared, достоверность НИЖЕ кодовой;
//   - статические факты register_access -> writes-register/reads-register,
//     provenance code, достоверность из цепочки атрибуции.

// codeFactConfidence — потолок достоверности кодового факта: точный факт
// (Provenance exact) несёт единицу, цепочка атрибуции только снижает её.
// Объявлен здесь, чтобы «строго ниже кодового факта» было выражено числом, а
// не комментарием.
const codeFactConfidence = 1.0

// declaredMovementConfidence — достоверность ребра writes-declared (§4).
// Декларация метаданных — это обещание платформы, а не наблюдение за кодом:
// документ может не писать объявленный регистр ни в одной ветке. Поэтому она
// строго ниже codeFactConfidence и отдельно названа, чтобы будущая калибровка
// меняла одно число, а не искала литерал по коду.
const declaredMovementConfidence = 0.5

// BadgeAttributionStale — бейдж «связь этого объекта снесена пересборкой и в
// том же прогоне не восстановлена». Правило то же, что у has-dynamic и
// attribution-truncated: пустое место на карте обязано быть отличимо от
// честного «связи нет».
//
// Живёт здесь, а не рядом с двумя своими соседями в internal/resolve, потому
// что ставит его ПУБЛИКАТОР и знает о нём только он: дериватор сравнивает
// цепочки, а «было — стало» между двумя публикациями видно лишь тому, кто
// пишет в store. Снимается полной пересборкой (в новой эпохе сносить нечего)
// и обычной пересборкой владельца, если связь вернулась.
const BadgeAttributionStale = "attribution-stale"

// edgeEvidence — сериализованное поле object_data_edge.evidence. У кодового
// ребра это цепочка атрибуции, у декларированного — файл XML и имя регистра
// ровно как оно записано в метаданных.
type edgeEvidence struct {
	Chain            []resolve.ChainStep `json:"chain,omitempty"`
	XMLFile          string              `json:"xmlFile,omitempty"`
	DeclaredRegister string              `json:"declaredRegister,omitempty"`
}

func marshalEvidence(e edgeEvidence) (string, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// publishDeclaredMovementEdges строит рёбра writes-declared по
// meta.Facts.Document.RegisterRecords документов, republish-нутых этой
// транзакцией (§4).
//
// Регистр, объявленный в XML, но отсутствующий в конфигурации, ребра не даёт:
// висячее ребро на несуществующий узел хуже отсутствующего.
func publishDeclaredMovementEdges(tx *store.WriteTx, in publishInput, ts *txState) error {
	for _, rel := range in.republish {
		rec := in.corpus.files[rel]
		if rec == nil || rec.metaFacts.Object == nil || rec.metaFacts.Document == nil {
			continue
		}
		if len(rec.metaFacts.Document.RegisterRecords) == 0 {
			continue
		}
		fileID, ok := ts.fileID[rel]
		if !ok {
			continue
		}
		obj := rec.metaFacts.Object
		fromID, ok, err := ts.nodes.lookup(tx, metadataObjectIdentityKey(in.component, obj.MType, obj.NameNorm))
		if err != nil {
			return fmt.Errorf("документ %s: %w", rel, err)
		}
		if !ok {
			continue
		}
		seen := make(map[int64]struct{}, len(rec.metaFacts.Document.RegisterRecords))
		for _, ref := range rec.metaFacts.Document.RegisterRecords {
			mtype, name, ok := splitMDObjectRef(ref)
			if !ok {
				continue
			}
			toID, ok, err := ts.nodes.lookup(tx, metadataObjectIdentityKey(in.component, mtype, domain.NormalizeName(name)))
			if err != nil {
				return fmt.Errorf("регистр %s документа %s: %w", ref, rel, err)
			}
			if !ok {
				continue // объявлен, но такого объекта в конфигурации нет
			}
			if _, dup := seen[toID]; dup {
				continue
			}
			seen[toID] = struct{}{}
			evidence, err := marshalEvidence(edgeEvidence{XMLFile: rel, DeclaredRegister: ref})
			if err != nil {
				return fmt.Errorf("evidence %s: %w", rel, err)
			}
			if _, err := tx.InsertObjectDataEdge(store.ObjectDataEdge{
				FromObjectID: fromID, ToObjectID: toID,
				Kind: store.EdgeWritesDeclared, Layer: layerName(in.layer),
				Provenance: store.EdgeProvenanceDeclared, Confidence: declaredMovementConfidence,
				Mode: "movement", Evidence: evidence, FileIDs: []int64{fileID},
			}); err != nil {
				return fmt.Errorf("writes-declared %s -> %s: %w", rel, ref, err)
			}
			ts.rememberEdge(fromID, toID, store.EdgeWritesDeclared, layerName(in.layer))
			ts.counts.objectEdge++
		}
	}
	return nil
}

// splitMDObjectRef разбирает ссылку метаданных вида
// «AccumulationRegister.ТоварыОрганизаций» на вид и имя объекта. Вид в XML уже
// записан в единственном числе, то есть ровно так же, как MType объекта
// метаданных, — переводить его словарём ownerTypeToMType не нужно.
func splitMDObjectRef(ref string) (mtype, name string, ok bool) {
	mtype, name, found := strings.Cut(strings.TrimSpace(ref), ".")
	if !found || mtype == "" || name == "" {
		return "", "", false
	}
	return mtype, name, true
}

// publishObjectDataEdges — единственная точка публикации объектного графа,
// вызывается из publishFiles. Отдельного реестра derive-шагов в проекте нет и
// заводить его ради одного шага не нужно (§5): новый шаг — это функция плюс
// вызов.
func publishObjectDataEdges(tx *store.WriteTx, in publishInput, ts *txState, modState map[string]modulePublishState) error {
	if err := publishDeclaredMovementEdges(tx, in, ts); err != nil {
		return err
	}
	if err := publishCodeObjectEdges(tx, in, ts, modState); err != nil {
		return err
	}
	// Последним шагом, когда известно и снесённое, и опубликованное.
	return publishStaleAttributionBadges(tx, ts)
}

// rememberEdge запоминает опубликованную связь для сверки со снесённой.
// Слой нормализуется так же, как его нормализует store при вставке
// (пусто = base), иначе «было — стало» разошлось бы на пустой строке.
func (ts *txState) rememberEdge(fromID, toID int64, kind, layer string) {
	ts.publishedEdges[store.ObjectDataEdgeKey{
		FromObjectID: fromID, ToObjectID: toID, Kind: kind, Layer: layer,
	}] = struct{}{}
}

func layerOrBaseName(layer string) string {
	if layer == "" {
		return "base"
	}
	return layer
}

// publishStaleAttributionBadges вешает признак на владельцев, чья связь
// снесена пересборкой и в этом же прогоне не восстановлена (признак attribution-stale, ADR-026).
//
// Ребро сносится по файловой зависимости: правка ЛЮБОГО звена цепочки убирает
// его целиком, а построить заново публикация может только то, до чего дошла
// атрибуция. Если хоть один файл цепочки в этот прогон не переопубликовывался,
// его символы и разрешённые вызовы могли не восстановиться, и отсутствие
// ребра означает не «связи нет», а «атрибуция устарела». Отличить одно от
// другого по самой карте нечем — поэтому объект получает видимый признак.
//
// Ребро, ВСЯ цепочка которого переопубликована этой транзакцией, признака не
// даёт: его отсутствие — честный результат разбора нового текста, то есть
// связь действительно убрана из кода.
func publishStaleAttributionBadges(tx *store.WriteTx, ts *txState) error {
	if len(ts.staleEdges) == 0 && len(ts.carriedStale) == 0 {
		return nil
	}
	lost := make(map[staleBadgeKey]int64, len(ts.carriedStale))
	// Признак прошлых прогонов, чья причина не снята: уборка бейджей стёрла
	// его вместе с остальными, и переставить его обязан этот же проход.
	// Для владельцев ВНЕ scope строку никто не стирал (DeleteObjectBadges
	// зовётся только по scope) — carryStaleBadges всё равно кладёт её сюда,
	// и InsertObjectBadge ниже просто перезапишет ту же строку суммой, а не
	// создаст задвоение (см. staleEdgeLosses и вызов carryStaleBadges в
	// publishCodeObjectEdges).
	for k, n := range ts.carriedStale {
		lost[k] += n
	}
	for k, n := range staleEdgeLosses(ts) {
		lost[k] += n
	}
	keys := make([]staleBadgeKey, 0, len(lost))
	for k := range lost {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].objectID != keys[j].objectID {
			return keys[i].objectID < keys[j].objectID
		}
		return keys[i].layer < keys[j].layer
	})
	for _, k := range keys {
		if err := tx.InsertObjectBadge(store.ObjectBadge{
			ObjectID: k.objectID, Badge: BadgeAttributionStale, Layer: k.layer, Count: lost[k],
		}); err != nil {
			return fmt.Errorf("бейдж %s объекта %d: %w", BadgeAttributionStale, k.objectID, err)
		}
		ts.counts.objectBadge++
	}
	return nil
}

// staleEdgeLosses: чистый срез ts.staleEdges по трём исключениям
// (вернулось / владелец переопубликован / вся цепочка переопубликована),
// БЕЗ ts.carriedStale прошлых прогонов. Вынесена из publishStaleAttributionBadges
// в отдельную функцию, потому что у неё теперь два потребителя: сама
// publishStaleAttributionBadges (складывает с унаследованным) и
// publishCodeObjectEdges (по объектам-владельцам результата решает, чей
// прежний бейдж attribution-stale нужно прочитать ДО DeleteObjectBadges —
// даже когда владелец не входит в scope has-dynamic/attribution-truncated).
func staleEdgeLosses(ts *txState) map[staleBadgeKey]int64 {
	lost := make(map[staleBadgeKey]int64)
	for _, e := range ts.staleEdges {
		if _, back := ts.publishedEdges[e.ObjectDataEdgeKey]; back {
			// Связь вернулась этим же прогоном — сообщать не о чем.
			//
			// До ADR-037 исход этой ветки всегда совпадал с исходом
			// следующей (владелец переопубликован): правка модуля-цели
			// обрывала входящие в него вызовы, и ребро без переопубликации
			// владельца не возвращалось. Теперь указатели
			// нетронутых файлов переживают правку цели, и ребро владельца,
			// чей модуль не менялся, возвращается именно здесь (закреплено
			// первой половиной TestStaleAttributionBadgeMarksLostEdge).
			continue
		}
		if _, fresh := ts.republishedOwners[e.FromObjectID]; fresh {
			// Владелец переопубликован: его цепочки посчитаны заново по
			// свежему тексту, и отсутствие ребра — результат разбора, а не
			// оборванной выборки. Обычный рефакторинг (документ перестал
			// звать общий модуль) живёт именно здесь, и без этой проверки
			// признак горел бы на нём всегда — а сигнал, горящий и на потере,
			// и на намеренном удалении, перестаёт что-либо значить.
			continue
		}
		if allFilesRepublished(e.FileIDs, ts.staleFiles) {
			// Второй, более узкий случай того же: у ребра нет модуля-владельца
			// в цепочке вовсе. Так устроено декларированное ребро — вся его
			// зависимость это XML документа, а XML не модуль и в
			// republishedOwners не попадает.
			continue
		}
		lost[staleBadgeKey{objectID: e.FromObjectID, layer: e.Layer}]++
	}
	return lost
}

// staleBadgeKey — объект и слой, на которых живёт признак устаревшей
// атрибуции.
type staleBadgeKey struct {
	objectID int64
	layer    string
}

// carryStaleBadges перепроверяет признаки устаревшей атрибуции, поставленные
// ПРОШЛЫМИ прогонами, перед тем как уборка бейджей их сотрёт.
//
// Уборка идёт по объекту (бейдж не имеет файловой зависимости), поэтому любая
// правка, задевшая владельца, стирала бы и его признак — а причина при этом
// никуда не девалась: ребро по-прежнему отсутствует. Признак обязан жить,
// пока живёт причина.
//
// Перепроверка, а не слепой перенос: признак снимается у владельцев, чьи
// СОБСТВЕННЫЕ модули переопубликованы этой транзакцией — их цепочки посчитаны
// заново от начала, и то, что вышло, и есть правда. У остальных прежний
// счётчик переставляется и складывается с потерями этого прогона: одно и то же
// ребро дважды не потеряется, его уже нет в таблице.
//
// упрощение: ребро, вернувшееся без переопубликации владельца (запись
// возвращена в общий модуль, ADR-037), признак прошлого прогона не снимает:
// бейдж не знает, какие именно связи он считает. Потолок: ложное «могла
// устареть» до правки модуля владельца или полной пересборки; путь выше:
// файловая или рёберная зависимость у бейджа (смена контракта object_badge).
//
// objectIDs — НЕ обязательно совпадает со scope has-dynamic/attribution-
// truncated (вызывающий, publishCodeObjectEdges, передаёт объединение scope с
// владельцами, потерявшими ребро в этом прогоне по staleEdgeLosses). Владелец
// вне scope сюда попадает ровно потому, что DeleteObjectBadges его не
// коснётся и его старую строку attribution-stale больше НИКТО не прочитает —
// без переноса она бы просто молча замещалась публикацией
// publishStaleAttributionBadges (ON CONFLICT DO UPDATE), теряя долю, которую
// уже потерял прошлый прогон.
func carryStaleBadges(tx *store.WriteTx, ts *txState, objectIDs []int64) error {
	for _, objectID := range objectIDs {
		if _, fresh := ts.republishedOwners[objectID]; fresh {
			continue
		}
		badges, err := tx.ObjectBadges(objectID)
		if err != nil {
			return fmt.Errorf("бейджи объекта %d: %w", objectID, err)
		}
		for _, b := range badges {
			if b.Badge != BadgeAttributionStale || b.Count <= 0 {
				continue
			}
			if ts.carriedStale == nil {
				ts.carriedStale = make(map[staleBadgeKey]int64)
			}
			ts.carriedStale[staleBadgeKey{objectID: objectID, layer: b.Layer}] += b.Count
		}
	}
	return nil
}

// allFilesRepublished — вся ли цепочка ребра переопубликована этой
// транзакцией.
func allFilesRepublished(fileIDs []int64, stale map[int64]struct{}) bool {
	for _, id := range fileIDs {
		if _, ok := stale[id]; !ok {
			return false
		}
	}
	return true
}

// ownerClosureRounds — сколько раз сид расширяется модулями найденных
// владельцев, прежде чем публикация остановится. Раунд добавляет модули
// владельцев, найденных предыдущим раундом, поэтому сходится он быстро;
// потолок стоит затем, чтобы патологический граф не крутил цикл бесконечно.
const ownerClosureRounds = 4

// publishCodeObjectEdges строит кодовые рёбра (§5) по строкам register_access
// и публикует их вместе с бейджами узлов.
//
// Инкрементальность. Сид: символы модулей, republish-нутых этой
// транзакцией, ПЛЮС всё, до чего от них можно дойти ВНИЗ по графу вызовов:
// правка документа меняет атрибуцию фактов, лежащих в общих модулях, которые
// он зовёт, а те не менялись и в republish не попали. Обратный ход (вверх, до
// объекта-владельца) делает уже сам дериватор.
//
// Дальше сид расширяется модулями найденных владельцев: счётчик object_badge
// ЗАМЕЩАЕТСЯ, а не складывается, поэтому владельцу нельзя подать часть его
// строк — частичный счётчик выглядел бы уменьшившейся дырой, а не
// недосчитанной.
func publishCodeObjectEdges(tx *store.WriteTx, in publishInput, ts *txState, modState map[string]modulePublishState) error {
	g := newTxEdgeGraph(tx)

	// memoryFiles — файлы, чьи строки register_access уже лежат в памяти
	// (их вставила эта же транзакция). Только для них второй SELECT не нужен.
	// Не путать с seedFiles: тот отвечает на другой вопрос — «файл уже в
	// сиде», и после замыкания по владельцу содержит файлы, которых в памяти
	// нет и читать их обязательно.
	memoryFiles := make(map[int64]struct{}, len(ts.fileID))
	for _, id := range ts.fileID {
		memoryFiles[id] = struct{}{}
	}

	seedFiles := make(map[int64]struct{}, len(modState))
	seedSymbols := make(map[int64]struct{})
	for _, st := range modState {
		seedFiles[st.fileID] = struct{}{}
		for _, id := range st.methodSymbolID {
			seedSymbols[id] = struct{}{}
		}
	}
	// Владельцы republish-нутых модулей: их бейджи пересчитываются, даже если
	// после правки у них не осталось ни одного факта — иначе счётчик прошлого
	// прогона висел бы вечно.
	//
	// Этот же набор отвечает на второй вопрос — снята ли причина устаревшей
	// атрибуции. Она в том, что вызывающий не переопубликован: пока это так,
	// подниматься от факта к нему нечем. Владелец, чей собственный модуль
	// переопубликован, свои цепочки пересчитал от начала, и состояние карты
	// для него достоверно, каким бы оно ни вышло.
	scope := make(map[int64]struct{})
	republishedOwners := make(map[int64]struct{})
	// Модули ищутся по НОВЫМ id файлов (modState заполнен вставкой этой же
	// транзакции): старые id после удаления модулю уже не принадлежат.
	for _, st := range modState {
		mod, ok, err := tx.ModuleByFile(st.fileID)
		if err != nil {
			return fmt.Errorf("модуль файла %d: %w", st.fileID, err)
		}
		if ok && mod.OwnerObjectID != 0 {
			scope[mod.OwnerObjectID] = struct{}{}
			republishedOwners[mod.OwnerObjectID] = struct{}{}
		}
	}
	ts.republishedOwners = republishedOwners

	// Полная пересборка компонента: переопубликовано всё, строки всех файлов
	// уже в памяти. Спускаться по графу вызовов и замыкать сид по владельцам
	// не от чего и незачем — на реальной выгрузке это разница между одним
	// проходом и запросом на каждый символ конфигурации.
	fullPass := len(in.republish) >= len(in.corpus.files)
	rounds := ownerClosureRounds
	if fullPass {
		rounds = 1
	}

	// Замыкание сида по владельцам ДО первой атрибуции: бейджи объектов из
	// scope будут снесены, значит их строки обязаны быть в сиде ЦЕЛИКОМ.
	// У владельца модулей несколько (объекта, менеджера, форм), и правка
	// одного из них не тянет остальные в republish — без этого шага счётчик
	// пересчитался бы по части модулей и записался бы поверх полного.
	if !fullPass {
		if _, err := expandSeedByOwners(tx, sortedInt64Set(scope), seedFiles, seedSymbols); err != nil {
			return err
		}
	}

	var edges []resolve.ObjectDataEdge
	var badges []resolve.ObjectBadge
	for round := 0; round < rounds; round++ {
		rows := ts.registerAccess
		if !fullPass {
			var err error
			rows, err = g.accessRows(seedSymbols, memoryFiles, ts.registerAccess)
			if err != nil {
				return err
			}
		}
		edges, badges = resolve.DeriveObjectDataEdges(resolve.ObjectEdgeInput{
			Accesses: rows, Graph: g, Tunables: in.tunables,
		})
		// Ошибка транспорта не даёт рёбер молча: дериватор увидел бы пустой
		// граф и построил бы недостроенные цепочки, то есть отсутствие рёбер,
		// неотличимое от честного «связи нет».
		if err := g.Err(); err != nil {
			return err
		}
		// Владелец, которого нашла атрибуция, мог не владеть ни одним
		// republish-нутым модулем (документ, зовущий изменившийся общий
		// модуль): его строки добираются здесь, и следующий раунд считает
		// его счётчик по полному набору.
		touched := touchedObjects(edges, badges)
		for _, objectID := range touched {
			scope[objectID] = struct{}{}
		}
		if fullPass {
			break
		}
		grown, err := expandSeedByOwners(tx, touched, seedFiles, seedSymbols)
		if err != nil {
			return err
		}
		if !grown {
			break
		}
	}

	// Вставляются только рёбра, зависящие хотя бы от одного файла, который
	// эта транзакция переопубликовала. Остальные не были удалены (снос идёт
	// по тем же файлам, ADR-24) и лежат в таблице неизменными: их повторная
	// вставка задвоила бы ребро. Новым ребро быть не может, если ни один файл
	// его цепочки не менялся: цепочка целиком состоит из файлов, а вызов
	// живёт в файле вызывающего.
	changed := make(map[int64]struct{}, len(ts.fileID))
	for _, id := range ts.fileID {
		changed[id] = struct{}{}
	}
	for _, e := range edges {
		if !dependsOnChangedFile(e.FileIDs, changed) {
			continue
		}
		evidence, err := marshalEvidence(edgeEvidence{Chain: e.Chain})
		if err != nil {
			return fmt.Errorf("evidence ребра %d->%d: %w", e.FromObjectID, e.ToObjectID, err)
		}
		if _, err := tx.InsertObjectDataEdge(store.ObjectDataEdge{
			FromObjectID: e.FromObjectID, ToObjectID: e.ToObjectID, Kind: e.Kind,
			Layer: e.Layer, Provenance: e.Provenance, Confidence: e.Confidence,
			Mode: e.Mode, InTransaction: e.InTransaction, Evidence: evidence, FileIDs: e.FileIDs,
		}); err != nil {
			return fmt.Errorf("%s %d->%d: %w", e.Kind, e.FromObjectID, e.ToObjectID, err)
		}
		ts.rememberEdge(e.FromObjectID, e.ToObjectID, e.Kind, layerOrBaseName(e.Layer))
		ts.counts.objectEdge++
	}

	// Бейджи объекта не привязаны к файлу, поэтому пересборка чистит их по
	// объекту и пишет заново (ADR-24).
	//
	// упрощение: в scope попадают владельцы republish-нутых модулей и те, кого
	// нашла атрибуция этого прохода. Если изменившийся модуль потерял ВСЕ свои
	// строки register_access, подниматься наверх больше не от чего, и счётчик
	// has-dynamic на чужом владельце, набранный через этот модуль, останется
	// прошлым до полной пересборки (или до правки модуля самого владельца).
	// Потолок именно такой; закрывается он файловой зависимостью у бейджа, то
	// есть сменой контракта таблицы object_badge (§2), а не правкой здесь.
	// carryScope — scope has-dynamic/attribution-truncated РАСШИРЕННЫЙ
	// владельцами, которые в этом прогоне теряют СВОЁ ребро (staleEdgeLosses),
	// но модуля не переопубликовывали и атрибуцией не найдены. Их прежний
	// attribution-stale обязан быть прочитан здесь: DeleteObjectBadges ниже
	// вызывается ТОЛЬКО по scope, и для владельца вне scope это последний
	// момент, когда его старую строку вообще можно увидеть, — иначе
	// publishStaleAttributionBadges позже заместит её счётчиком одного этого
	// прогона и потеряет то, что было потеряно раньше (найдено внешним
	// ревью).
	carryScope := make(map[int64]struct{}, len(scope))
	for id := range scope {
		carryScope[id] = struct{}{}
	}
	for k := range staleEdgeLosses(ts) {
		carryScope[k.objectID] = struct{}{}
	}
	if err := carryStaleBadges(tx, ts, sortedInt64Set(carryScope)); err != nil {
		return err
	}
	if err := tx.DeleteObjectBadges(sortedInt64Set(scope)...); err != nil {
		return fmt.Errorf("очистка бейджей: %w", err)
	}
	for _, b := range badges {
		if err := tx.InsertObjectBadge(store.ObjectBadge{
			ObjectID: b.ObjectID, Badge: b.Badge, Layer: b.Layer, Count: b.Count,
		}); err != nil {
			return fmt.Errorf("бейдж %s объекта %d: %w", b.Badge, b.ObjectID, err)
		}
		ts.counts.objectBadge++
	}
	return nil
}

// expandSeedByOwners добавляет в сид символы ВСЕХ модулей перечисленных
// объектов-владельцев. Возвращает true, если сид вырос: вызывающий на этом
// останавливает раунды.
func expandSeedByOwners(tx *store.WriteTx, objectIDs []int64, seedFiles, seedSymbols map[int64]struct{}) (bool, error) {
	grown := false
	for _, objectID := range objectIDs {
		files, err := tx.ModuleFilesByOwnerObject(objectID)
		if err != nil {
			return false, fmt.Errorf("модули владельца %d: %w", objectID, err)
		}
		for _, fileID := range files {
			if _, have := seedFiles[fileID]; have {
				continue
			}
			seedFiles[fileID] = struct{}{}
			grown = true
			syms, err := tx.SymbolsByOriginFile(fileID)
			if err != nil {
				return false, fmt.Errorf("символы файла %d: %w", fileID, err)
			}
			for _, sym := range syms {
				seedSymbols[sym.ID] = struct{}{}
			}
		}
	}
	return grown, nil
}

// dependsOnChangedFile — зависит ли ребро хотя бы от одного файла, который
// эта транзакция переопубликовала.
func dependsOnChangedFile(fileIDs []int64, changed map[int64]struct{}) bool {
	for _, id := range fileIDs {
		if _, ok := changed[id]; ok {
			return true
		}
	}
	return false
}

// touchedObjects — объекты, которых коснулась атрибуция: концы-владельцы
// рёбер и носители бейджей.
func touchedObjects(edges []resolve.ObjectDataEdge, badges []resolve.ObjectBadge) []int64 {
	set := make(map[int64]struct{}, len(edges)+len(badges))
	for _, e := range edges {
		set[e.FromObjectID] = struct{}{}
	}
	for _, b := range badges {
		set[b.ObjectID] = struct{}{}
	}
	return sortedInt64Set(set)
}

func sortedInt64Set(set map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// txEdgeGraph — resolve.ObjectEdgeGraph поверх write-транзакции: граф вызовов
// и владельцы модулей, как их видит store.
//
// Методы контракта ошибок не возвращают: обход зовёт их тысячи раз, и
// протаскивать error через каждую вершину значит подменить алгоритм
// обработкой ошибок. Ошибка транспорта копится здесь и спрашивается через
// Err() ПОСЛЕ вызова дериватора (идиома bufio.Scanner, прецедент в проекте —
// syntax.NewLazy плюс (*Index).Err). Пока ошибка не снята, все ответы пусты:
// недостроенная цепочка даёт отсутствие ребра, а не выдуманное ребро.
type txEdgeGraph struct {
	tx      *store.WriteTx
	callers map[int64][]resolve.SymbolCall
	callees map[int64][]int64
	owners  map[int64]ownerLookup
	err     error
}

type ownerLookup struct {
	owner resolve.SymbolOwner
	known bool
}

func newTxEdgeGraph(tx *store.WriteTx) *txEdgeGraph {
	return &txEdgeGraph{
		tx:      tx,
		callers: make(map[int64][]resolve.SymbolCall),
		callees: make(map[int64][]int64),
		owners:  make(map[int64]ownerLookup),
	}
}

// Err — накопленная ошибка транспорта, спрашивается после обхода.
func (g *txEdgeGraph) Err() error { return g.err }

// fail запоминает первую ошибку: последующие не затирают её, чтобы наружу
// вышла причина, а не последнее следствие.
func (g *txEdgeGraph) fail(err error) {
	if g.err == nil {
		g.err = err
	}
}

// CallersOf — обратные рёбра графа вызовов. Кэш живёт на транзакцию: общий
// модуль зовут из тысяч мест, и без кэша один и тот же SELECT повторялся бы
// на каждый факт.
func (g *txEdgeGraph) CallersOf(symbolID int64) []resolve.SymbolCall {
	if g.err != nil {
		return nil
	}
	if cached, ok := g.callers[symbolID]; ok {
		return cached
	}
	rows, err := g.tx.CallEdgesTo(symbolID)
	if err != nil {
		g.fail(fmt.Errorf("вызывающие символа %d: %w", symbolID, err))
		return nil
	}
	out := make([]resolve.SymbolCall, 0, len(rows))
	for _, r := range rows {
		if r.CallerID == 0 {
			continue
		}
		out = append(out, resolve.SymbolCall{CallerID: r.CallerID, Confidence: r.Confidence})
	}
	g.callers[symbolID] = out
	return out
}

// SymbolOwner — модуль символа и объект-владелец этого модуля.
func (g *txEdgeGraph) SymbolOwner(symbolID int64) (resolve.SymbolOwner, bool) {
	if g.err != nil {
		return resolve.SymbolOwner{}, false
	}
	if cached, ok := g.owners[symbolID]; ok {
		return cached.owner, cached.known
	}
	sym, ok, err := g.tx.SymbolByID(symbolID)
	if err != nil {
		g.fail(fmt.Errorf("символ %d: %w", symbolID, err))
		return resolve.SymbolOwner{}, false
	}
	var res ownerLookup
	if ok {
		mod, found, err := g.tx.ModuleByFile(sym.OriginFileID)
		if err != nil {
			g.fail(fmt.Errorf("модуль символа %d: %w", symbolID, err))
			return resolve.SymbolOwner{}, false
		}
		if found {
			res = ownerLookup{known: true, owner: resolve.SymbolOwner{
				ModuleKind: mod.Kind, OwnerObjectID: mod.OwnerObjectID, FileID: sym.OriginFileID,
			}}
		}
	}
	g.owners[symbolID] = res
	return res.owner, res.known
}

// calleesOf — прямые рёбра графа вызовов (кого зовёт символ), для спуска вниз
// от изменившихся модулей.
func (g *txEdgeGraph) calleesOf(symbolID int64) ([]int64, error) {
	if cached, ok := g.callees[symbolID]; ok {
		return cached, nil
	}
	rows, err := g.tx.CallEdgesFrom(symbolID)
	if err != nil {
		return nil, fmt.Errorf("вызовы символа %d: %w", symbolID, err)
	}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		if r.CalleeID != 0 {
			out = append(out, r.CalleeID)
		}
	}
	g.callees[symbolID] = out
	return out, nil
}

// accessRows собирает вход дериватора: строки register_access всех символов
// сида и всего, до чего от них доходит спуск по графу вызовов.
//
// Строки символов из republish-нутых файлов берутся из памяти (inserted): их
// только что вставила эта же транзакция, и второй SELECT за ними на полной
// пересборке стоил бы запроса на каждый символ конфигурации. Для всех
// остальных символов сида строки читаются из store.
func (g *txEdgeGraph) accessRows(seed, memoryFiles map[int64]struct{}, inserted []store.RegisterAccessRow) ([]store.RegisterAccessRow, error) {
	visited := make(map[int64]struct{}, len(seed))
	queue := sortedInt64Set(seed)
	for _, id := range queue {
		visited[id] = struct{}{}
	}
	rows := make([]store.RegisterAccessRow, len(inserted))
	copy(rows, inserted)
	for len(queue) > 0 {
		symbolID := queue[0]
		queue = queue[1:]
		owner, known := g.SymbolOwner(symbolID)
		if err := g.Err(); err != nil {
			return nil, err
		}
		if known {
			if _, inMemory := memoryFiles[owner.FileID]; !inMemory {
				more, err := g.tx.RegisterAccessesBySymbol(symbolID)
				if err != nil {
					return nil, fmt.Errorf("доступы к регистрам символа %d: %w", symbolID, err)
				}
				rows = append(rows, more...)
			}
		}
		callees, err := g.calleesOf(symbolID)
		if err != nil {
			return nil, err
		}
		for _, id := range callees {
			if _, seen := visited[id]; seen {
				continue
			}
			visited[id] = struct{}{}
			queue = append(queue, id)
		}
	}
	return rows, nil
}
