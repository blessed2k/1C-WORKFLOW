#!/usr/bin/env python3
"""Проверка хука bsl_ready_methods.py: запускает его на примерах событий.

Запуск: python3 tools/hooks/bsl_ready_methods_test.py. Печатает HOOK_TEST_OK,
когда все примеры прошли; иначе называет первый упавший и выходит с кодом 1.
Состояние сессий хука пишется во временный домашний каталог и убирается.
"""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import uuid

HOOK = os.path.join(os.path.dirname(os.path.abspath(__file__)), "bsl_ready_methods.py")

MODULE = (
    "&НаСервере\n"
    "Процедура ПриСозданииНаСервере(Отказ)\n"
    "КонецПроцедуры\n\n"
    "// Убирает дубли.\n"
    "Функция УдалитьДублиВМассиве(Массив) Экспорт\n"
    "КонецФункции\n\n"
    "Асинх Функция РазобратьСтроку (Стр)\n"
    "КонецФункции\n\n"
    "Процедура ОбработкаПроведения(Отказ, Режим)\n"
    "КонецПроцедуры\n\n"
    "Функция ПриведениеТипа(Значение)\n"
    "КонецФункции\n\n"
    "Процедура ТоварыПриИзменении(Элемент)\n"
    "КонецПроцедуры\n\n"
    "&Вместо(\"ПровестиДокумент\")\n"
    "Процедура Расш1_ПровестиДокумент()\n"
    "КонецПроцедуры\n\n"
    "&НаКлиенте\n"
    "Процедура Заполнить(Команда)\n"
    "КонецПроцедуры\n"
)


def run(event, home):
    """Запускает хук на событии; возвращает текст напоминания или пустую строку."""
    raw = event if isinstance(event, str) else json.dumps(event, ensure_ascii=False)
    env = dict(os.environ, HOME=home, USERPROFILE=home,
               XDG_CACHE_HOME=os.path.join(home, ".cache"), LocalAppData=os.path.join(home, "local"))
    proc = subprocess.run([sys.executable, HOOK], input=raw.encode("utf-8"), capture_output=True, check=False, env=env)
    if proc.returncode != 0:
        raise AssertionError("код выхода %d: %s" % (proc.returncode, proc.stderr.decode("utf-8", "replace")))
    out = proc.stdout.decode("utf-8").strip()
    if not out:
        return ""
    return json.loads(out)["hookSpecificOutput"]["additionalContext"]


def check(name, ok):
    if not ok:
        print("УПАЛО: " + name)
        sys.exit(1)


def put_snapshot(home, name, root, modules):
    """Кладёт снимок ходовых методов туда, где его ищет хук на любой ОС."""
    body = json.dumps({"root": root, "modules": modules}, ensure_ascii=False)
    for base in (os.path.join(home, "Library", "Caches"), os.path.join(home, ".cache"), os.path.join(home, "local")):
        folder = os.path.join(base, "mcp1c", "core")
        os.makedirs(folder, exist_ok=True)
        with open(os.path.join(folder, name), "w", encoding="utf-8") as f:
            f.write(body)


def core_scenario(home):
    """Ходовые методы: один раз за сессию, на первом обращении к .bsl, для своей базы."""
    session = "core-" + uuid.uuid4().hex
    read = {"session_id": session, "tool_name": "Read", "tool_input": {"file_path": "/базы/зуп/src/Модуль.bsl"}}
    check("снимка нет: о ходовых методах молчит", run(read, home) == "")

    put_snapshot(home, "a.json", "/базы/ут", [{"module": "ОбщегоНазначенияУТ", "methods": ["МетодУТ"]}])
    outside = {"session_id": "core-" + uuid.uuid4().hex, "tool_name": "Read", "tool_input": {"file_path": "/расширения/своё/Модуль.bsl"}}
    text = run(outside, home)
    check("файл вне корней при единственном свежем снимке: список этой базы с названным корнем",
          "МетодУТ" in text and "/базы/ут" in text)
    put_snapshot(home, "b.json", "/базы/зуп", [{"module": "ОбщегоНазначения", "methods": ["ЗначениеРеквизитаОбъекта", "СообщитьПользователю"]}])
    text = run(read, home)
    check("чтение .bsl отдаёт ходовые методы своей базы",
          "ОбщегоНазначения: ЗначениеРеквизитаОбъекта, СообщитьПользователю" in text and "МетодУТ" not in text)
    check("второе обращение в той же сессии молчит", run(read, home) == "")
    outside["session_id"] = "core-" + uuid.uuid4().hex
    check("файл вне корней при двух снимках: чужой список не подставляется", run(outside, home) == "")

    write = {"session_id": "core-" + uuid.uuid4().hex, "tool_name": "Write", "tool_input": {
        "file_path": "/базы/зуп/src/Новый.bsl", "content": "Функция СвояФункция()\nКонецФункции\n"}}
    text = run(write, home)
    check("запись новой процедуры в новой сессии: и список, и напоминание",
          "ЗначениеРеквизитаОбъекта" in text and "СвояФункция" in text and "validate_bsl" in text)

    no_session = {"tool_name": "Read", "tool_input": {"file_path": "/базы/зуп/src/Модуль.bsl"}}
    check("без идентификатора сессии список не повторяется на каждом обращении", run(no_session, home) == "")

    broken = os.path.join(home, "Library", "Caches", "mcp1c", "core", "c.json")
    for base in ("Library/Caches", ".cache", "local"):
        with open(os.path.join(home, *base.split("/"), "mcp1c", "core", "c.json"), "w", encoding="utf-8") as f:
            f.write("{не json")
    other = {"session_id": "core-" + uuid.uuid4().hex, "tool_name": "Read", "tool_input": {"file_path": "/базы/ут/Модуль.bsl"}}
    check("битый снимок рядом не мешает", "МетодУТ" in run(other, home) and os.path.exists(broken))


def scenario(home):
    session = "test-" + uuid.uuid4().hex
    write = {"session_id": session, "tool_name": "Write", "tool_input": {"file_path": "/x/Модуль.bsl", "content": MODULE}}

    text = run(write, home)
    check("новые процедуры названы", "УдалитьДублиВМассиве" in text and "РазобратьСтроку" in text and "validate_bsl" in text)
    check("функция с началом имени как у обработчика названа", "ПриведениеТипа" in text)
    check("обработчики событий не названы", "ПриСозданииНаСервере" not in text and "ОбработкаПроведения" not in text)
    check("обработчик элемента формы, перехватчик и команда не названы",
          "ТоварыПриИзменении" not in text and "Расш1_ПровестиДокумент" not in text and "Заполнить" not in text)
    check("повторная запись того же в той же сессии молчит", run(write, home) == "")

    edit = {"session_id": session, "tool_name": "Edit", "tool_input": {
        "file_path": "/x/Типовой.bsl",
        "old_string": "Функция СтараяТиповая(Массив)\n\tВозврат 1;\nКонецФункции\n",
        "new_string": "Функция СтараяТиповая(Массив)\n\tВозврат 2;\nКонецФункции\nФункция НоваяСвоя()\nКонецФункции\n"}}
    text = run(edit, home)
    check("правка называет только новую процедуру, не ту, что была в заменяемом куске",
          "НоваяСвоя" in text and "СтараяТиповая" not in text)

    multi = {"session_id": session, "tool_name": "MultiEdit", "tool_input": {
        "file_path": "/x/Другой.bsl", "edits": [{"old_string": "a", "new_string": "Процедура ЗаполнитьТаблицу()\nКонецПроцедуры"}]}}
    check("MultiEdit разобран", "ЗаполнитьТаблицу" in run(multi, home))

    other = {"session_id": session, "tool_name": "Write", "tool_input": {"file_path": "/x/main.go", "content": MODULE}}
    check("не .bsl: молчит", run(other, home) == "")
    check("нет своих процедур: молчит", run({"session_id": session, "tool_name": "Write", "tool_input": {
        "file_path": "/x/Пустой.bsl", "content": "Сообщить(1);\n"}}, home) == "")
    check("мусор во входе: молчит", run("не json", home) == "")
    check("событие без полей: молчит", run({}, home) == "")
    check("tool_input не объект: молчит", run({"tool_input": "строка"}, home) == "")

    # Чужая сессия о той же процедуре ещё не слышала.
    write["session_id"] = session + "-other"
    check("другая сессия получает напоминание", "УдалитьДублиВМассиве" in run(write, home))

    state = os.path.join(home, ".cache", "mcp1c-hooks")
    check("состояние лежит в каталоге пользователя, без временных файлов",
          not [n for n in os.listdir(state) if n.endswith(".tmp")] and len(os.listdir(state)) == 2)
    core_scenario(home)


def main():
    home = tempfile.mkdtemp(prefix="mcp1c-hook-test-")
    try:
        scenario(home)
    finally:
        shutil.rmtree(home, ignore_errors=True)
    print("HOOK_TEST_OK")


if __name__ == "__main__":
    main()
