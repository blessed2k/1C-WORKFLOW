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
    env = dict(os.environ, HOME=home, USERPROFILE=home)
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
          sorted(n.endswith(".json") for n in os.listdir(state)) == [True, True])


def main():
    home = tempfile.mkdtemp(prefix="mcp1c-hook-test-")
    try:
        scenario(home)
    finally:
        shutil.rmtree(home, ignore_errors=True)
    print("HOOK_TEST_OK")


if __name__ == "__main__":
    main()
