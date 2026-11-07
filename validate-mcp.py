#!/usr/bin/env python3
import json
import os
from pathlib import Path
import select
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parent


def send(process, message):
    process.stdin.write(json.dumps(message, separators=(",", ":")).encode() + b"\n")
    process.stdin.flush()


def receive(process, wanted_id, buffered, timeout):
    deadline = time.monotonic() + timeout
    while True:
        while b"\n" in buffered:
            line, _, remainder = buffered.partition(b"\n")
            buffered[:] = remainder
            try:
                message = json.loads(line)
            except (json.JSONDecodeError, UnicodeDecodeError):
                continue
            if message.get("id") == wanted_id:
                return message

        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        ready, _, _ = select.select([process.stdout], [], [], remaining)
        if not ready:
            break
        chunk = os.read(process.stdout.fileno(), 65536)
        if not chunk:
            break
        buffered.extend(chunk)
    raise RuntimeError(f"no response to request {wanted_id} received within {timeout:g} seconds")


def stop_server(process):
    if process.poll() is not None:
        return
    try:
        process.stdin.close()
    except (BrokenPipeError, OSError):
        pass
    try:
        process.wait(timeout=1)
    except subprocess.TimeoutExpired:
        process.terminate()
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()


def validate_schemas(response, walle):
    if "result" not in response:
        raise RuntimeError(f"tools/list failed: {response.get('error', response)}")
    checked = 0
    failures = []
    for tool in response["result"].get("tools", []):
        for key in ("inputSchema", "outputSchema"):
            if key not in tool:
                continue
            checked += 1
            result = subprocess.run(
                [walle, "-level", "strict"],
                input=json.dumps(tool[key]),
                text=True,
                capture_output=True,
            )
            if result.returncode:
                failures.append((tool.get("name", "<unnamed>"), key, result.stderr.strip()))

    for name, key, error in failures:
        print(f"{name}.{key}: {error}", file=sys.stderr)
    print(f"Checked {checked} schemas: {len(failures)} invalid")
    return 1 if failures else 0


def run(command):
    timeout = float(os.environ.get("MCP_TIMEOUT", "15"))
    with tempfile.TemporaryDirectory() as tmp:
        walle = os.environ.get("WALLE_BIN")
        if not walle:
            walle = str(Path(tmp) / "walle")
            subprocess.run(
                ["go", "build", "-o", walle, "./cmd/walle"],
                cwd=ROOT / "walle",
                check=True,
            )

        process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE)
        try:
            buffered = bytearray()
            send(process, {
                "jsonrpc": "2.0",
                "id": 1,
                "method": "initialize",
                "params": {
                    "protocolVersion": "2025-03-26",
                    "capabilities": {},
                    "clientInfo": {"name": "mcp-schema-validator", "version": "0"},
                },
            })
            receive(process, 1, buffered, timeout)
            send(process, {"jsonrpc": "2.0", "method": "notifications/initialized"})
            send(process, {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {}})
            response = receive(process, 2, buffered, timeout)
        finally:
            stop_server(process)

        return validate_schemas(response, walle)


def main():
    command = sys.argv[1:]
    if command and command[0] == "--":
        command = command[1:]
    if not command:
        print(f"Usage: {sys.argv[0]} [--] <stdio MCP command> [args...]", file=sys.stderr)
        return 2
    try:
        return run(command)
    except (OSError, RuntimeError, subprocess.CalledProcessError, ValueError) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
