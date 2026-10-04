#!/usr/bin/env python3
"""Хук Claude Code (PostToolUse): готовые методы БСП перед глазами агента.

Агент чаще пишет свою вспомогательную функцию, чем ищет готовую: вопрос «нет ли
в БСП» он пропускает. Инструмент сервера ему в этом не поможет, пока он сам не
решит его вызвать. Хук от решений агента не зависит и делает две вещи.

1. Когда агент в первый раз за сессию открывает или правит файл .bsl, хук
   кладёт в его контекст ходовые методы БСП этой базы: те, которыми прикладной
   код пользуется чаще всего. Список считает сервер mcp1c и оставляет файлом в
   каталоге кэша пользователя (mcp1c/core); хук его только читает. Сервер ещё не
   запускался или список не посчитан: хук об этом молчит.
2. Когда инструмент Write, Edit или MultiEdit записал в файл .bsl новые
   процедуры и функции, хук называет их и просит прогнать код через
   validate_bsl: тот называет возможные готовые методы (поле readyMethods), а
   find_api ищет по описанию задачи.

Хук ничего не блокирует и в сеть не ходит: читает событие из stdin, печатает
JSON с additionalContext и помнит в файле состояния, что уже говорил в этой
сессии.

Подключение (settings.json Claude Code), путь к файлу свой; на Windows вместо
python3 обычно python или py -3:

    "hooks": {"PostToolUse": [{"matcher": "Read|Write|Edit|MultiEdit", "hooks": [
        {"type": "command", "command": "python3 /путь/tools/hooks/bsl_ready_methods.py"}]}]}

Ограничения. Write поверх существующего модуля называет новыми все его
процедуры: прежнего текста файла в событии нет. После сжатия контекста сессия
та же, и повторно ни список, ни напоминание не придут. validate_bsl есть у
сервера только в offline-режиме; без него остаётся find_api.
"""

import hashlib
import json
import os
import re
import sys
import tempfile
import time

# Объявление процедуры или функции: аннотации на той же строке, имя, параметры
# до закрывающей скобки. То же правило, что у сервера (internal/app/apidraft.go,
# reDraftMethodHead).
METHOD_HEAD = re.compile(
    r"^[ \t]*((?:&\S+[ \t]+)*)(?:асинх[ \t]+|async[ \t]+)?(?:процедура|функция|procedure|function)[ \t]+"
    r"([^\W\d]\w*)[ \t]*\(([^)\n]*)",
    re.IGNORECASE | re.MULTILINE,
)
ANNOTATION = re.compile(r"&\s*([^\W\d]\w*)")
# Слово идентификатора по границам CamelCase.
WORD = re.compile(r"[A-ZА-ЯЁ]+(?![a-zа-яё])|[A-ZА-ЯЁ]?[a-zа-яё]+|\d+")

# Списки ниже повторяют серверные (apiDraftEventWords,
# apiDraftInterceptAnnotations): хук и validate_bsl должны пропускать одно и то
# же, иначе хук зовёт проверить процедуру, которую проверка пропустит.
EVENT_WORDS = {"при", "перед", "после", "обработка", "on", "before", "after"}
INTERCEPT_ANNOTATIONS = {"перед", "после", "вместо", "изменениеиконтроль", "before", "after", "around", "changeandvalidate"}

# Сколько имён называть в одном напоминании.
NAMES_LIMIT = 12
# Потолок блока ходовых методов, знаков: защита от испорченного файла снимка.
CORE_LIMIT = 6000
# Снимок старше этого возраста не подставляется файлу вне известных корней.
CORE_FRESH_SECONDS = 14 * 24 * 3600


def written_parts(tool_input):
    """Пары (новый текст, прежний текст): весь файл у Write, куски у Edit и MultiEdit."""
    parts = []
    if isinstance(tool_input.get("content"), str):
        parts.append((tool_input["content"], ""))
    if isinstance(tool_input.get("new_string"), str):
        parts.append((tool_input["new_string"], tool_input.get("old_string") or ""))
    for edit in tool_input.get("edits") or []:
        if isinstance(edit, dict) and isinstance(edit.get("new_string"), str):
            parts.append((edit["new_string"], edit.get("old_string") or ""))
    return parts


def is_handler(name, params, annotations_line, lines_above):
    """Метод написан по обязанности: обработчик события, команды, оповещения, перехватчик."""
    annotations = ANNOTATION.findall(annotations_line)
    for line in lines_above:
        annotations += ANNOTATION.findall(line)
    if any(a.lower() in INTERCEPT_ANNOTATIONS for a in annotations):
        return True
    if name.lower().startswith("подключаемый_") or params.strip().lower() in ("команда", "command"):
        return True
    words = [w.lower() for w in WORD.findall(name)]
    if not words:
        return False
    return words[0] in EVENT_WORDS or words[-1] == "завершение" or "при" in words[1:]


def declared_names(text):
    """Имена всех объявленных в тексте процедур и функций, в нижнем регистре."""
    return {m.group(2).lower() for m in METHOD_HEAD.finditer(text if isinstance(text, str) else "")}


def new_methods(new_text, old_text):
    """Процедуры и функции, которых не было в прежнем тексте, без обработчиков, без повторов."""
    existed = declared_names(old_text)
    seen, out = set(), []
    for m in METHOD_HEAD.finditer(new_text):
        name = m.group(2)
        low = name.lower()
        if low in seen or low in existed:
            continue
        # Строки с аннотациями вплотную над объявлением.
        above, lines = [], new_text[: m.start()].split("\n")[:-1]
        while lines and lines[-1].strip().startswith("&"):
            above.append(lines.pop())
        if is_handler(name, m.group(3), m.group(1), above):
            continue
        seen.add(low)
        out.append(name)
    return out


def user_cache_dir():
    """Каталог кэша пользователя: тот же, что os.UserCacheDir у сервера."""
    if sys.platform == "win32":
        return os.environ.get("LocalAppData") or ""
    if sys.platform == "darwin":
        return os.path.join(os.path.expanduser("~"), "Library", "Caches")
    return os.environ.get("XDG_CACHE_HOME") or os.path.join(os.path.expanduser("~"), ".cache")


def real(path):
    """Путь без символических ссылок и без различий регистра там, где ОС их не различает."""
    return os.path.normcase(os.path.realpath(path))


def core_snapshot(file_path):
    """Снимок ходовых методов для базы, к которой относится файл.

    Берётся снимок, чей корень проекта или выгрузки содержит файл (из вложенных
    корней самый глубокий). Файл лежит вне всех известных корней (код пишется в
    соседнюю папку, например в расширение): снимок берётся, только если он один
    и свежий; из нескольких баз угадывать нельзя, чужой список хуже никакого.
    """
    base = user_cache_dir()
    if not base:
        return None
    folder = os.path.join(base, "mcp1c", "core")
    try:
        names = [n for n in os.listdir(folder) if n.endswith(".json")]
    except OSError:
        return None
    target = real(file_path)
    inside, fresh = None, []
    for name in names:
        path = os.path.join(folder, name)
        try:
            with open(path, encoding="utf-8") as f:
                snap = json.load(f)
            age = time.time() - os.path.getmtime(path)
        except (OSError, ValueError):
            continue
        if not isinstance(snap, dict) or not isinstance(snap.get("modules"), list):
            continue
        depth = 0
        for root in (snap.get("root"), snap.get("dump")):
            if isinstance(root, str) and root:
                prefix = real(root).rstrip("\\/") + os.sep
                if target.startswith(prefix):
                    depth = max(depth, len(prefix))
        if depth and (inside is None or depth > inside[0]):
            inside = (depth, snap)
        if age <= CORE_FRESH_SECONDS:
            fresh.append(snap)
    if inside:
        return inside[1]
    return fresh[0] if len(fresh) == 1 else None


def core_text(snap):
    """Ходовые методы одной строкой на модуль, целыми строками в пределах потолка."""
    lines = []
    for module in snap.get("modules") or []:
        if not isinstance(module, dict):
            continue
        methods = [m for m in module.get("methods") or [] if isinstance(m, str)]
        if isinstance(module.get("module"), str) and methods:
            lines.append(module["module"] + ": " + ", ".join(methods))
    if not lines:
        return ""
    root = snap.get("root") if isinstance(snap.get("root"), str) else ""
    text = (
        "Ходовые методы БСП (ими прикладной код пользуется чаще всего; посчитаны сервером по базе %s). "
        "Прежде чем писать свою вспомогательную функцию, проверь, нет ли её здесь; "
        "сигнатуру и описание даст get_symbol или find_api с module, остальные методы ищет find_api по описанию задачи."
    ) % (root or "этого проекта")
    for line in lines:
        if len(text) + len(line) + 1 > CORE_LIMIT:
            break
        text += "\n" + line
    return text


def once(session_id, mark):
    """Истина ровно один раз на сессию и метку: создаёт файл-метку, а не читает и пишет.

    Создание файла с запретом перезаписи атомарно: параллельные вызовы одной
    сессии (несколько Read подряд) не выдадут список дважды. Метку создать
    нельзя: возвращается ложь, потому что иначе список шёл бы на каждый вызов.
    """
    if not session_id:
        return False
    try:
        fd = os.open(state_path(session_id) + "." + mark, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
    except OSError:
        return False
    os.close(fd)
    return True


def state_path(session_id):
    """Файл с тем, что уже сказано в сессии: в каталоге пользователя, не в общем."""
    digest = hashlib.sha256(session_id.encode("utf-8")).hexdigest()[:16]
    base = os.path.join(os.path.expanduser("~"), ".cache", "mcp1c-hooks")
    try:
        os.makedirs(base, mode=0o700, exist_ok=True)
    except OSError:
        base = tempfile.gettempdir()
    return os.path.join(base, "bsl-ready-" + digest + ".json")


def not_reported(session_id, keys):
    """Оставляет ключи, которых в этой сессии ещё не было, и запоминает их.

    упрощение: чтение и запись состояния без блокировки. Параллельные вызовы
    одной сессии могут потерять запись друг друга; последствие: повторное
    напоминание. Запись атомарна (временный файл и замена), усечённого файла
    читатель не увидит. Без идентификатора сессии запоминать негде: всё
    считается новым.
    """
    if not session_id or not keys:
        return list(keys)
    path = state_path(session_id)
    try:
        with open(path, encoding="utf-8") as f:
            known = set(json.load(f))
    except (OSError, ValueError):
        known = set()
    fresh = [k for k in keys if k not in known]
    if fresh:
        known.update(fresh)
        tmp = path + ".%d.tmp" % os.getpid()
        try:
            with open(tmp, "w", encoding="utf-8") as f:
                json.dump(sorted(known), f, ensure_ascii=False)
            os.replace(tmp, path)
        except OSError:
            pass  # не запомнили: скажем ещё раз, это не ошибка
    return fresh


def reminder(file_path, names):
    shown = ", ".join(names[:NAMES_LIMIT])
    if len(names) > NAMES_LIMIT:
        shown += " и ещё %d" % (len(names) - NAMES_LIMIT)
    return (
        "В %s записаны новые процедуры и функции: %s. "
        "До применения проверь, нет ли готовых методов: вызови validate_bsl с этим кодом "
        "(поле readyMethods назовёт возможные готовые методы БСП и конфигурации; это подсказка по словам, "
        "читай назначение метода) или find_api по описанию того, что делает процедура. "
        "Готовый метод делает то же самое: замени свой код его вызовом."
    ) % (os.path.basename(file_path), shown)


def main():
    # Событие и ответ идут в UTF-8 независимо от кодировки консоли: на Windows
    # текстовые stdin и stdout берут кодировку локали и портят кириллицу.
    try:
        event = json.loads(sys.stdin.buffer.read().decode("utf-8"))
    except (ValueError, UnicodeDecodeError):
        return 0
    if not isinstance(event, dict):
        return 0
    tool_input = event.get("tool_input")
    if not isinstance(tool_input, dict):
        return 0
    file_path = tool_input.get("file_path") or tool_input.get("path") or ""
    if not isinstance(file_path, str) or not file_path.lower().endswith(".bsl"):
        return 0
    session_id = str(event.get("session_id") or "")
    parts = []

    # Ходовые методы: один раз за сессию, на первом обращении к .bsl.
    snap = core_snapshot(file_path)
    text = core_text(snap) if snap else ""
    if text and once(session_id, "core"):
        parts.append(text)

    # Новые процедуры записанного файла.
    names = []
    for new_text, old_text in written_parts(tool_input):
        names += [n for n in new_methods(new_text, old_text) if n not in names]
    keys = not_reported(session_id, [file_path + "::" + n.lower() for n in names])
    names = [n for n in names if file_path + "::" + n.lower() in keys]
    if names:
        parts.append(reminder(file_path, names))

    if not parts:
        return 0
    out = {"hookSpecificOutput": {"hookEventName": "PostToolUse", "additionalContext": "\n\n".join(parts)}}
    sys.stdout.buffer.write(json.dumps(out, ensure_ascii=False).encode("utf-8"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
