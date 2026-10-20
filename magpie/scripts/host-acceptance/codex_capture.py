#!/usr/bin/env python3
"""Opt-in real Codex capture. Only loopback, synthetic credentials and temp HOME.

This is an acceptance harness, never a product HTTP implementation. Direct mode
proves host wire behavior only; --router-binary exercises the actual product.
"""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import subprocess
import tempfile
import threading
import time
import urllib.request


def save(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex", default=shutil.which("codex"))
    parser.add_argument("--expected-version", default="codex-cli 0.161.0")
    parser.add_argument("--router-binary", type=Path)
    parser.add_argument("--router-commit", help="Exact committed product source revision")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--model", default="deepseek-v4-pro")
    parser.add_argument("--profile", choices=["default", "minimal"], default="minimal")
    parser.add_argument("--scenario", choices=["text", "tools", "cancel", "error"], default="tools")
    args = parser.parse_args()
    if not args.codex:
        parser.error("Codex executable is missing")
    args.output = args.output.resolve()
    args.output.mkdir(parents=True, exist_ok=False)
    # Preserve runtime PATH, but no user's credentials, proxy or CLI settings.
    with tempfile.TemporaryDirectory(prefix="api-router-codex-0161-") as tmp:
        root = Path(tmp)
        for name in ("home", "codex", "project"):
            (root / name).mkdir()
        env = {"PATH": os.environ["PATH"], "HOME": str(root / "home"),
               "CODEX_HOME": str(root / "codex"), "TERM": "dumb"}
        for kind in ("CONFIG", "DATA", "CACHE", "STATE"):
            env["XDG_" + kind + "_HOME"] = str(root / kind.lower())
        version = subprocess.run([args.codex, "--version"], env=env,
                                 capture_output=True, text=True, check=True).stdout.strip()
        if version != args.expected_version:
            raise RuntimeError(f"version mismatch: {version!r}")
        records = []
        arrived = threading.Event()
        disconnected = threading.Event()
        release = threading.Event()
        failures = []

        def scrub(text):
            text = text.replace(str(root), "<TEMP>")
            text = re.sub(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}",
                          "00000000-0000-0000-0000-000000000000", text)
            return text

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.0"

            def log_message(self, *_):
                pass

            def do_POST(self):
                try:
                    body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                    auth = self.headers.get("Authorization", "")
                    expected = "Bearer " + ("synthetic-upstream-key" if args.router_binary else
                                             "api-router-local-placeholder")
                    if auth != expected:
                        raise AssertionError("unexpected upstream authorization source")
                    record = {"method": "POST", "path": self.path,
                              "authorizationSource": "bound synthetic provider" if args.router_binary else "public placeholder",
                              "body": body}
                    records.append(json.loads(scrub(json.dumps(record))))
                    index = len(records)
                    save(args.output / f"request-{index}.json", records[-1])
                    arrived.set()
                    if args.scenario == "error":
                        data = json.dumps({"error": {"type": "invalid_request_error",
                            "code": "acceptance_rejection", "message": "Synthetic acceptance rejection"}}).encode()
                        self.send_response(400)
                        self.send_header("Content-Type", "application/json")
                        self.end_headers()
                        self.wfile.write(data)
                        return
                    self.send_response(200)
                    self.send_header("Content-Type", "text/event-stream")
                    self.end_headers()

                    def emit(typ, **data):
                        data["type"] = typ
                        self.wfile.write(("event: " + typ + "\ndata: " + json.dumps(data) + "\n\n").encode())
                        self.wfile.flush()

                    response = {"id": f"resp_acceptance_{index}", "object": "response",
                                "status": "in_progress", "output": []}
                    emit("response.created", response=response)
                    if args.scenario == "cancel":
                        self.connection.settimeout(0.2)
                        while not release.is_set():
                            try:
                                if not self.connection.recv(1, socket.MSG_PEEK):
                                    disconnected.set()
                                    break
                            except socket.timeout:
                                pass
                        return
                    history = [v for v in body.get("input", []) if v.get("type") == "function_call_output"]
                    commands = [
                        "cat input.txt",
                        "python3 -c \"from pathlib import Path; a,b=map(int,Path('input.txt').read_text().split()); Path('result.txt').write_text(str(a+b)+'\\n')\"",
                        "python3 -c \"from pathlib import Path; assert Path('result.txt').read_text() == '42\\n'; print('VERIFIED_42')\"",
                    ]
                    if args.scenario == "tools" and len(history) < 3:
                        if index > 1 and not history:
                            raise AssertionError("host failed to return tool output history")
                        item = {"type": "function_call", "id": f"fc_{index}",
                                "call_id": f"call_acceptance_{len(history)+1}", "name": "exec_command",
                                "arguments": json.dumps({"cmd": commands[len(history)],
                                    "yield_time_ms": 1000, "max_output_tokens": 1000}), "status": "completed"}
                        emit("response.output_item.added", output_index=0,
                             item=dict(item, arguments="", status="in_progress"))
                        emit("response.function_call_arguments.delta", item_id=item["id"],
                             output_index=0, delta=item["arguments"])
                        emit("response.function_call_arguments.done", item_id=item["id"],
                             output_index=0, arguments=item["arguments"])
                    else:
                        if args.scenario == "tools":
                            assert "VERIFIED_42" in json.dumps(history), "verification result absent from next model request"
                        item = {"type": "message", "id": f"msg_{index}", "role": "assistant",
                                "status": "completed", "content": [{"type": "output_text",
                                "text": "ACCEPTANCE_42" if args.scenario == "tools" else "ACCEPTANCE_TEXT", "annotations": []}]}
                        emit("response.output_item.added", output_index=0, item=dict(item, status="in_progress"))
                        emit("response.output_text.delta", item_id=item["id"], output_index=0,
                             content_index=0, delta=item["content"][0]["text"])
                    emit("response.output_item.done", output_index=0, item=item)
                    emit("response.completed", response=dict(response, status="completed", output=[item],
                         usage={"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}))
                except (BrokenPipeError, ConnectionResetError):
                    pass
                except Exception as exc:
                    failures.append(str(exc))

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        router = None
        router_log = None
        router_sha = None
        cli = None
        try:
            base = f"http://127.0.0.1:{server.server_port}/app/codex/v1"
            if args.router_binary:
                snapshot = root / "api-router"
                shutil.copy2(args.router_binary.resolve(), snapshot)
                router_sha = hashlib.sha256(snapshot.read_bytes()).hexdigest()
                binary = str(snapshot)
                subprocess.run([binary, "config", "init"], env=env, capture_output=True, check=True)
                layout = json.loads(subprocess.check_output([binary, "dirs"], env=env))
                config_file = Path(layout["config"]) / "config.json"
                config = json.loads(config_file.read_text())
                router_port = port()
                config["listen"] = {"address": f"127.0.0.1:{router_port}"}
                save(config_file, config)
                router_log = (root / "router.log").open("w+")
                router = subprocess.Popen([binary, "serve"], env=env, stdout=router_log,
                                          stderr=subprocess.STDOUT)
                token_path = Path(layout["state"]) / "router.lock" / "management-token"
                deadline = time.monotonic() + 10
                while not token_path.exists():
                    if router.poll() is not None or time.monotonic() > deadline:
                        raise RuntimeError("isolated product did not start")
                    threading.Event().wait(0.05)
                token = token_path.read_text().strip()
                origin = f"http://127.0.0.1:{router_port}"

                def management(path, body):
                    req = urllib.request.Request(origin + path, json.dumps(body).encode(),
                        {"Authorization": "Bearer " + token, "Content-Type": "application/json"})
                    with urllib.request.urlopen(req, timeout=10) as resp:
                        return json.load(resp)

                provider = management("/api/cloud-providers", {"name": "Synthetic Codex acceptance",
                    "protocol": "responses", "baseURL": f"http://127.0.0.1:{server.server_port}/v1",
                    "model": args.model, "apiKey": "synthetic-upstream-key", "proxyMode": "direct"})["provider"]
                management("/api/apps", {"id": "codex", "name": "Codex", "revision": 0,
                    "cloud": {"providerId": provider["id"], "model": args.model}, "credentialMode": "stored"})
                base = origin + "/app/codex/v1"
            config_text = f'''model_provider = "api_router"
model = {json.dumps(args.model)}
web_search = "disabled"
[model_providers.api_router]
name = "API Router"
base_url = {json.dumps(base)}
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
experimental_bearer_token = "api-router-local-placeholder"
request_max_retries = 0
stream_max_retries = 0
'''
            if args.profile == "minimal":
                config_text += "[features]\nmulti_agent = false\ngoals = false\n"
            (root / "codex" / "config.toml").write_text(config_text)
            (root / "project" / "input.txt").write_text("19 23\n")
            (root / "project" / "AGENTS.md").write_text("Acceptance developer instruction: keep calculations exact.\n")
            command = [args.codex, "exec", "--strict-config", "--skip-git-repo-check", "--ephemeral",
                       "--json", "--ignore-rules", "-C", str(root / "project"), "-s", "workspace-write",
                       "Read input.txt, compute its sum into result.txt, verify with a command, then summarize."]
            cli = subprocess.Popen(command, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, text=True, start_new_session=True)
            canceled = False
            if args.scenario == "cancel":
                if arrived.wait(10):
                    os.killpg(cli.pid, signal.SIGINT)
                    canceled = True
                else:
                    failures.append("no upstream request before cancellation deadline")
            stdout, stderr = cli.communicate(timeout=35)
            if canceled:
                disconnected.wait(5)
            release.set()
            result = root / "project" / "result.txt"
            passed = cli.returncode == 0 and ("ACCEPTANCE_42" if args.scenario == "tools" else "ACCEPTANCE_TEXT") in stdout
            if args.scenario == "tools":
                passed = passed and result.exists() and result.read_text() == "42\n" and len(records) == 4
            if args.scenario == "cancel":
                passed = canceled and disconnected.is_set() and len(records) == 1 and "ACCEPTANCE_TEXT" not in stdout
            if args.scenario == "error":
                expected_error = "cloud_parameter_error" if args.router_binary else "Synthetic acceptance rejection"
                passed = cli.returncode != 0 and expected_error in stdout + stderr
                if args.router_binary:
                    passed = passed and "Synthetic acceptance rejection" not in stdout + stderr
            passed = passed and not failures
            (args.output / "events.jsonl").write_text(scrub(stdout))
            (args.output / "stderr.txt").write_text(scrub(stderr))
            (args.output / "config.toml").write_text(config_text.replace(base, "http://127.0.0.1:<PORT>/app/codex/v1"))
            save(args.output / "summary.json", {"passed": passed, "evidenceLevel":
                 "real CLI via product with simulated upstream" if args.router_binary else "real CLI direct simulated upstream",
                 "version": version, "platform": os.uname().sysname + " " + os.uname().release,
                 "executable": args.codex, "model": args.model, "protocol": "responses",
                 "routerCommit": args.router_commit, "routerBinarySHA256": router_sha,
                 "profile": args.profile, "scenario": args.scenario, "exitCode": cli.returncode,
                 "requests": len(records), "canceledAfterRequest": canceled, "failures": failures,
                 "upstreamConnectionClosedOnCancel": disconnected.is_set(),
                 "resultFile": result.read_text() if result.exists() else None,
                 "command": [scrub(v) for v in command], "configSHA256": hashlib.sha256(config_text.encode()).hexdigest()})
            print(json.dumps({"passed": passed, "requests": len(records), "output": str(args.output)}))
            return 0 if passed else 1
        finally:
            release.set()
            if cli and cli.poll() is None:
                os.killpg(cli.pid, signal.SIGKILL)
                cli.wait()
            if router:
                router.terminate()
                router.wait(timeout=10)
            if router_log:
                router_log.close()
            server.shutdown()
            server.server_close()
            thread.join()


if __name__ == "__main__":
    raise SystemExit(main())
