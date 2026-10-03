#!/usr/bin/env python3
"""Хук Claude Code (PostToolUse): напоминание проверить черновик BSL на готовые методы.

Агент чаще пишет свою вспомогательную функцию, чем ищет готовую: вопрос «нет ли
в БСП» он пропускает до письма. После письма его задаёт этот хук. Когда
инструмент Write, Edit или MultiEdit записал в файл .bsl новые процедуры и
функции, хук дописывает в контекст агента их имена и просьбу прогнать код через
validate_bsl: тот называет возможные готовые методы (поле readyMethods), а
find_api ищет по описанию задачи.

Хук ничего не блокирует и в сеть не ходит: читает событие из stdin, печатает
JSON с additionalContext и помнит в файле состояния, о каких процедурах сессии
уже напоминал (об одной напоминает один раз).

Подключение (settings.json Claude Code), путь к файлу свой; на Windows вместо
python3 обычно python или py -3:

    "hooks": {"PostToolUse": [{"matcher": "Write|Edit|MultiEdit", "hooks": [
        {"type": "command", "command": "python3 /путь/tools/hooks/bsl_ready_methods.py"}]}]}

Ограничения. Write поверх существующего модуля называет новыми все его
процедуры: прежнего текста файла в событии нет. После сжатия контекста сессия
та же, и повторно напоминание не придёт. validate_bsl есть у сервера только в
offline-режиме; без него остаётся find_api.
"""

import hashlib
import json
import os
import re
import sys
import tempfile

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


def state_path(session_id):
    """Файл с уже названными процедурами сессии: в каталоге пользователя, не в общем."""
    digest = hashlib.sha256(session_id.encode("utf-8")).hexdigest()[:16]
    base = os.path.join(os.path.expanduser("~"), ".cache", "mcp1c-hooks")
    try:
        os.makedirs(base, mode=0o700, exist_ok=True)
    except OSError:
        base = tempfile.gettempdir()
    return os.path.join(base, "bsl-ready-" + digest + ".json")


def not_reported(session_id, file_path, names):
    """Оставляет имена, о которых в этой сессии ещё не напоминали, и запоминает их.

    упрощение: чтение и запись состояния без блокировки. Параллельные вызовы
    одной сессии могут потерять запись друг друга; последствие: повторное
    напоминание. Запись атомарна (временный файл и замена), усечённого файла
    читатель не увидит.
    """
    if not session_id:
        return names
    path = state_path(session_id)
    try:
        with open(path, encoding="utf-8") as f:
            known = set(json.load(f))
    except (OSError, ValueError):
        known = set()
    fresh = [n for n in names if file_path + "::" + n.lower() not in known]
    if fresh:
        known.update(file_path + "::" + n.lower() for n in fresh)
        tmp = path + ".%d.tmp" % os.getpid()
        try:
            with open(tmp, "w", encoding="utf-8") as f:
                json.dump(sorted(known), f, ensure_ascii=False)
            os.replace(tmp, path)
        except OSError:
            pass  # не запомнили: напомним ещё раз, это не ошибка
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
    names = []
    for new_text, old_text in written_parts(tool_input):
        names += [n for n in new_methods(new_text, old_text) if n not in names]
    names = not_reported(str(event.get("session_id") or ""), file_path, names)
    if not names:
        return 0
    out = {"hookSpecificOutput": {"hookEventName": "PostToolUse", "additionalContext": reminder(file_path, names)}}
    sys.stdout.buffer.write(json.dumps(out, ensure_ascii=False).encode("utf-8"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
