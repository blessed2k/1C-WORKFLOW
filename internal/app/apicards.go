package app

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// APICard: карточка поиска метода: формулировки задачи словами разработчика.
// Пишется один раз на версию библиотеки или конфигурации (cmd/apicards);
// find_api ищет по ней наравне с именем и комментарием метода и так находит
// метод по словам, которых нет ни в имени, ни в комментарии.
type APICard struct {
	// Call: выражение вызова метода, как в ответе find_api.
	Call string `json:"call"`
	// Tasks: как задачу, которую решает метод, называют своими словами.
	Tasks []string `json:"tasks,omitempty"`
	// Instead: какой самописный код метод заменяет.
	Instead string `json:"instead,omitempty"`
}

// APICardsHeader: первая строка файла карточек: что это за набор.
type APICardsHeader struct {
	Pack   string `json:"pack"`
	Built  string `json:"built,omitempty"`
	Source string `json:"source,omitempty"`
	Cards  int    `json:"cards"`
}

// APICardsEnv: переменная окружения с каталогом карточек.
const APICardsEnv = "MCP_1C_API_CARDS"

// DefaultAPICardsDir: где сервер ищет файлы карточек, когда каталог не задан.
func DefaultAPICardsDir() string {
	if dir := os.Getenv(APICardsEnv); dir != "" {
		return dir
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "mcp1c", "api-cards")
}

// apiCardsDir: каталог карточек, заданный процессом. Процессная переменная по
// той же причине, что и пороги графа (ConfigureGraphTunables): флаг
// разбирается один раз при старте, а читает его сервис поиска при постройке
// индекса слов. Пусто: карточки не используются (так живут тесты: каталог
// пользователя в них не читается).
var (
	apiCardsMu  sync.RWMutex
	apiCardsDir string
)

// ConfigureAPICards задаёт каталог карточек поиска. Пустая строка отключает
// карточки.
func ConfigureAPICards(dir string) {
	apiCardsMu.Lock()
	defer apiCardsMu.Unlock()
	apiCardsDir = strings.TrimSpace(dir)
}

// APICardsDir: каталог карточек, заданный процессу; пусто: карточек нет.
func APICardsDir() string {
	apiCardsMu.RLock()
	defer apiCardsMu.RUnlock()
	return apiCardsDir
}

// WriteAPICards пишет файл карточек: заголовок и по карточке на строку. Пишет
// во временный файл рядом и переименовывает: сервер, строящий индекс слов в
// эту минуту, не прочтёт файл наполовину.
func WriteAPICards(path string, header APICardsHeader, cards []APICard) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(header); err != nil {
		return err
	}
	for _, c := range cards {
		if err := enc.Encode(c); err != nil {
			return err
		}
	}
	// Временный файл без расширения .jsonl: читатель каталога его не видит.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// apiCardFiles: файлы карточек каталога в порядке имён. Каталога нет или он
// не задан: файлов нет, это не ошибка.
func apiCardFiles(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	names, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// scanAPICardFile отдаёт непустые строки файла карточек с их номерами.
func scanAPICardFile(name string, each func(n int, line []byte) error) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 4<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := each(n, line); err != nil {
			return fmt.Errorf("%s: строка %d: %w", name, n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// APICardPack: набор карточек каталога: заголовок файла и отпечаток его
// содержимого.
type APICardPack struct {
	APICardsHeader
	// Digest: SHA-256 содержимого файла. Имя и число карточек набор не
	// опознают: пересобранный другой моделью набор носит те же.
	Digest string
}

// APICardPacks называет наборы карточек каталога в порядке имён файлов. Файл
// без заголовка назван по имени файла, число карточек у него не известно.
func APICardPacks(dir string) ([]APICardPack, error) {
	names, err := apiCardFiles(dir)
	if err != nil {
		return nil, err
	}
	var out []APICardPack
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		pack := APICardPack{Digest: hex.EncodeToString(sum[:])}
		first, _, _ := bytes.Cut(data, []byte("\n"))
		// Первая строка без поля pack: это карточка, заголовка у файла нет.
		_ = json.Unmarshal(first, &pack.APICardsHeader)
		if pack.Pack == "" {
			pack.APICardsHeader = APICardsHeader{Pack: strings.TrimSuffix(filepath.Base(name), ".jsonl")}
		}
		out = append(out, pack)
	}
	return out, nil
}

// LoadAPICards читает все файлы карточек каталога (*.jsonl) и возвращает
// карточки по выражению вызова в нижнем регистре. Каталога нет: карточек нет,
// это не ошибка. Метод описан в нескольких файлах: берётся первый по имени
// файла.
func LoadAPICards(dir string) (map[string]APICard, error) {
	out := map[string]APICard{}
	names, err := apiCardFiles(dir)
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		err := scanAPICardFile(name, func(_ int, line []byte) error {
			var c APICard
			if err := json.Unmarshal(line, &c); err != nil {
				return err
			}
			if c.Call == "" {
				return nil // заголовок файла
			}
			key := strings.ToLower(c.Call)
			if _, seen := out[key]; !seen {
				out[key] = c
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
