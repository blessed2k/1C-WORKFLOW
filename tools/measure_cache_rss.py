#!/usr/bin/env python3
"""Замер RSS процесса mcp1c на реальной выгрузке в трёх точках.

Поднимает уже собранный бинарник поверх stdio, говорит с ним по JSON-RPC
(протокол MCP: initialize -> notifications/initialized -> tools/call) и снимает
блок памяти `server_info`:

  1) холодный старт      — кэш выгрузки пуст;
  2) после прогрева      — rights_audit + write_path наполнили кэш;
  3) после простоя > TTL — ни одного вызова между точками 2 и 3.

В каждой точке рядом с самоотчётом сервера (`memory.rssBytes`, его собственный
processRSS) снимается независимое чтение RSS по pid дочернего процесса — тем же
`ps`, но из этого скрипта. Два числа рядом делают самоотчёт проверяемым.

Пример:
  python3 tools/measure_cache_rss.py \
      --binary /tmp/mcp1c --dump <каталог выгрузки> \
      --ttl 25s --wait 95

Печатает JSON со всеми тремя блоками памяти; ничего не додумывает — что вернул
сервер, то и в отчёте.
"""

import argparse
import json
import os
import subprocess
import sys
import time

PROTOCOL_VERSION = "2025-06-18"


class Session:
    """Минимальный MCP-клиент поверх stdio: ndjson, по одному запросу за раз."""

    def __init__(self, argv):
        self.proc = subprocess.Popen(
            argv,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=None,
            text=True,
            bufsize=1,
        )
        self.next_id = 0

    def _send(self, message):
        self.proc.stdin.write(json.dumps(message, ensure_ascii=False) + "\n")
        self.proc.stdin.flush()

    def request(self, method, params=None):
        self.next_id += 1
        rid = self.next_id
        self._send({"jsonrpc": "2.0", "id": rid, "method": method,
                    "params": params if params is not None else {}})
        while True:
            line = self.proc.stdout.readline()
            if not line:
                raise RuntimeError(f"сервер закрыл stdout на методе {method}")
            msg = json.loads(line)
            if msg.get("id") != rid:
                continue  # чужое уведомление или запрос сервера — не наш ответ
            if "error" in msg:
                raise RuntimeError(f"{method}: {msg['error']}")
            return msg["result"]

    def notify(self, method, params=None):
        self._send({"jsonrpc": "2.0", "method": method,
                    "params": params if params is not None else {}})

    def initialize(self):
        result = self.request("initialize", {
            "protocolVersion": PROTOCOL_VERSION,
            "capabilities": {},
            "clientInfo": {"name": "measure-cache-rss", "version": "1"},
        })
        self.notify("notifications/initialized")
        return result

    def call_tool(self, name, arguments):
        result = self.request("tools/call", {"name": name, "arguments": arguments})
        if result.get("isError"):
            raise RuntimeError(f"{name} вернул ошибку: {result.get('content')}")
        return result.get("structuredContent", {})

    def close(self):
        try:
            self.proc.stdin.close()
        except Exception:
            pass
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            self.proc.kill()


def external_rss(pid):
    """RSS процесса, прочитанный снаружи: проверка самоотчёта, а не его повтор."""
    try:
        out = subprocess.run(["ps", "-o", "rss=", "-p", str(pid)],
                             capture_output=True, text=True, timeout=5)
    except (OSError, subprocess.TimeoutExpired):
        return None
    raw = out.stdout.strip()
    if not raw.isdigit():
        return None
    return int(raw) * 1024  # ps отдаёт кибибайты


def memory_block(session, label):
    info = session.call_tool("server_info", {})
    mem = info.get("memory")
    if mem is None:
        raise RuntimeError(f"{label}: в ответе server_info нет блока memory")
    # Снимается сразу после ответа: чем меньше разрыв, тем осмысленнее сравнение
    # с числом, которое сервер сообщил о себе сам.
    mem["rssExternalBytes"] = external_rss(session.proc.pid)
    return mem


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--binary", required=True, help="собранный бинарник mcp1c")
    ap.add_argument("--dump", required=True, help="каталог XML-выгрузки")
    ap.add_argument("--ttl", default="25s", help="значение флага --cache-ttl")
    ap.add_argument("--wait", type=float, default=95.0,
                    help="простой между точками 2 и 3, секунды (больше TTL и больше "
                         "периода фонового сброса — он не чаще раза в минуту)")
    ap.add_argument("--settle", type=float, default=0.0,
                    help="пауза между прогревом и точкой 2, секунды (меньше TTL): даёт "
                         "записям кэша ненулевой возраст последнего обращения")
    ap.add_argument("--object-type", default="Document")
    ap.add_argument("--object-name", default="ЗаказКлиента")
    args = ap.parse_args()

    dump = os.path.abspath(os.path.expanduser(args.dump))
    session = Session([args.binary, "--dump", dump, "--cache-ttl", args.ttl])
    report = {"ttl": args.ttl, "settle": args.settle, "wait": args.wait, "dump": dump}
    try:
        init = session.initialize()
        report["server"] = init.get("serverInfo", {})

        report["cold"] = memory_block(session, "холодный старт")

        session.call_tool("rights_audit", {"type": args.object_type, "name": args.object_name})
        session.call_tool("write_path", {"objectType": args.object_type, "name": args.object_name})
        if args.settle > 0:
            time.sleep(args.settle)
        report["warm"] = memory_block(session, "после прогрева")

        time.sleep(args.wait)
        report["expired"] = memory_block(session, "после простоя")
    finally:
        session.close()

    json.dump(report, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
