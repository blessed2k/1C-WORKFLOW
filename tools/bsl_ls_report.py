#!/usr/bin/env python3
"""Компактный отчёт bsl-language-server: одна строка на замечание.

Штатный консольный репортёр печатает дамп Java-объектов, примерно 25 строк на
одну диагностику: на модуле в 1763 строки это 264 КБ, из которых полезны шесть.
Плюс в отчёте номера строк 0-based, а в Read, в сообщениях 1С и в редакторе они
1-based, и эта разница уже приводила к тому, что правку искали не в той строке.

Здесь вывод такой:

    Ext/Module.bsl:15 [Warning] UnusedLocalVariable — Удалите неиспользуемую переменную Сч
    Итого: 0 Error, 2 Warning, 3 Hint, 1 Information (показано 6 из 6)

Строки 1-based. Код возврата 1, если есть Error, иначе 0 — годится как gate.

Примеры:
    python3 bsl_ls_report.py --src ./src
    python3 bsl_ls_report.py --src ./src --severity Error,Warning
    python3 bsl_ls_report.py --src ./src --file Module.bsl --lines 840-870
    python3 bsl_ls_report.py --src ./tests --test-profile
"""
from __future__ import annotations

import argparse
import glob
import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile

SEVERITY_ORDER = ["Error", "Warning", "Information", "Hint"]

# Диагностики, которые на тестовых модулях YAxUnit являются нормой, а не дефектом:
# префиксы вида ЮТ_ рядом с латиницей и числа-ожидания в проверках.
TEST_NOISE = {"MagicNumber", "LatinAndCyrillicSymbolInWord", "MissingCommonModuleMethodDescription"}

# На модулях форм линтер не видит Form.xml и считает реквизиты формы
# неиспользуемыми переменными: на реальной обработке это 11 строк шума на прогон.
FORM_NOISE = {"UnusedLocalVariable"}


def find_jar(explicit: str | None) -> str:
    if explicit:
        return explicit
    if os.environ.get("BSL_LS_JAR"):
        return os.environ["BSL_LS_JAR"]
    patterns = [
        os.path.expanduser("~/tools/bsl-language-server-*-exec.jar"),
        r"C:\tools\bsl-language-server-*-exec.jar",
    ]
    found: list[str] = []
    for p in patterns:
        found.extend(glob.glob(p))
    if not found:
        sys.exit("не найден bsl-language-server: укажите --jar или переменную BSL_LS_JAR")
    return sorted(found)[-1]  # самая свежая версия


def parse_lines(spec: str | None) -> tuple[int, int] | None:
    if not spec:
        return None
    if "-" in spec:
        a, b = spec.split("-", 1)
        return int(a), int(b)
    n = int(spec)
    return n, n


def run_analysis(jar: str, src: str, workspace: str | None) -> dict:
    out_dir = tempfile.mkdtemp(prefix="bslreport-")
    try:
        cmd = ["java", "-jar", jar, "analyze", "-s", src, "-r", "json", "-o", out_dir, "-q"]
        if workspace:
            cmd += ["-w", workspace]
        proc = subprocess.run(cmd, capture_output=True, text=True)
        report = pathlib.Path(out_dir) / "bsl-json.json"
        if not report.exists():
            sys.stderr.write(proc.stdout[-2000:] + proc.stderr[-2000:])
            sys.exit("анализатор не создал отчёт")
        return json.loads(report.read_text(encoding="utf-8"))
    finally:
        shutil.rmtree(out_dir, ignore_errors=True)


def to_local_path(raw: str) -> str:
    """Отчёт хранит путь как file:// URI, включая варианты с ../ внутри."""
    if raw.startswith("file:"):
        from urllib.parse import unquote, urlparse

        parsed = urlparse(raw)
        raw = unquote(parsed.path)
        # На Windows путь приходит как /C:/tools/... — ведущий слэш лишний
        if len(raw) > 2 and raw[0] == "/" and raw[2] == ":":
            raw = raw[1:]
    return os.path.normpath(raw)


def relative_to_src(path: str, src_root: pathlib.Path) -> str:
    """Короткий путь от корня анализа: полные пути в каждой строке нечитаемы."""
    try:
        return str(pathlib.Path(path).resolve().relative_to(pathlib.Path(src_root).resolve()))
    except (ValueError, OSError):
        return os.path.basename(path)


def collect(data: dict, args) -> tuple[list[tuple], dict, int]:
    wanted = None
    if args.severity:
        wanted = {s.strip().lower() for s in args.severity.split(",")}
    line_range = parse_lines(args.lines)
    src_root = pathlib.Path(data.get("sourceDir") or args.src)

    rows: list[tuple] = []
    counts = {s: 0 for s in SEVERITY_ORDER}
    total = 0

    for info in data.get("fileinfos", []):
        path = to_local_path(info.get("path", ""))
        shown_path = relative_to_src(path, src_root)
        is_form_module = "Forms" in pathlib.Path(path).parts

        for d in info.get("diagnostics", []):
            severity = d.get("severity", "Hint")
            code = d.get("code", "")
            # 0-based в отчёте, 1-based везде, где на него потом смотрят
            line = d.get("range", {}).get("start", {}).get("line", 0) + 1
            total += 1
            counts[severity] = counts.get(severity, 0) + 1

            if wanted and severity.lower() not in wanted:
                continue
            if args.file and args.file.lower() not in shown_path.lower():
                continue
            if line_range and not (line_range[0] <= line <= line_range[1]):
                continue
            if args.test_profile and code in TEST_NOISE:
                continue
            if args.form_attrs and is_form_module and code in FORM_NOISE:
                continue

            rows.append((shown_path, line, severity, code, d.get("message", "").strip()))

    rows.sort(key=lambda r: (r[0], r[1]))
    return rows, counts, total


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--src", required=True, help="папка с BSL или один модуль")
    ap.add_argument("--jar", help="путь к bsl-language-server-*-exec.jar")
    ap.add_argument("--workspace", help="корень рабочей области (-w), если отличается от --src")
    ap.add_argument("--severity", help="через запятую: Error,Warning,Information,Hint")
    ap.add_argument("--file", help="подстрока пути модуля")
    ap.add_argument("--lines", help="диапазон строк, например 840-870 (1-based)")
    ap.add_argument("--test-profile", action="store_true",
                    help="приглушить замечания, которые на тестах YAxUnit являются нормой")
    ap.add_argument("--form-attrs", action="store_true",
                    help="приглушить «неиспользуемая переменная» в модулях форм: линтер не видит Form.xml")
    ap.add_argument("--json", dest="as_json", action="store_true", help="выдать отфильтрованное машинно")
    args = ap.parse_args()

    data = run_analysis(find_jar(args.jar), args.src, args.workspace)
    rows, counts, total = collect(data, args)

    if args.as_json:
        print(json.dumps([
            {"file": f, "line": l, "severity": s, "code": c, "message": m} for f, l, s, c, m in rows
        ], ensure_ascii=False, indent=1))
    else:
        for f, l, s, c, m in rows:
            print(f"{f}:{l} [{s}] {c} — {m}")
        summary = ", ".join(f"{counts.get(s, 0)} {s}" for s in SEVERITY_ORDER if counts.get(s))
        if not summary:
            summary = "замечаний нет"
        print(f"Итого: {summary} (показано {len(rows)} из {total})")

    return 1 if counts.get("Error") else 0


if __name__ == "__main__":
    sys.exit(main())
