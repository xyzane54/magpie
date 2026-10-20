#!/usr/bin/env python3
"""Opt-in fixed Codex default feature capture; isolated HOME and bounded calls.

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
import urllib.error


def save(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    path.chmod(0o600)


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def feature_checks(scenario, records, completed_items, result_text):
    """Verify linked host calls and results, independently of model prose."""
    histories = [item for record in records for item in record["body"].get("input", [])]
    calls = {v["call_id"]: v for v in histories if v.get("type") in
             ("function_call", "custom_tool_call") and "call_id" in v}
    outputs = {v["call_id"]: v.get("output") for v in histories if v.get("type") in
               ("function_call_output", "custom_tool_call_output") and "call_id" in v}

    def parsed(value):
        if isinstance(value, dict): return value
        try:
            decoded = json.loads(value)
            return decoded if isinstance(decoded, dict) else {}
        except (TypeError, ValueError): return {}

    def linked(name, namespace=None):
        return [(call, outputs[call_id]) for call_id, call in calls.items()
                if call.get("name") == name and call.get("namespace") == namespace and call_id in outputs]

    shell = linked("exec_command")
    shell_ok = any("Process exited with code 0" in str(output) for _, output in shell)
    if scenario == "tools":
        return {"threeLinkedShellResults": len(shell) >= 3,
                "verifiedOutput": any("VERIFIED_42" in str(output) for _, output in shell),
                "result42": result_text == "42\n"}
    if scenario == "goals":
        statuses = lambda name: [(parsed(output).get("goal") or {}).get("status") for _, output in linked(name)]
        return {"createActive": "active" in statuses("create_goal"),
                "readActive": "active" in statuses("get_goal"),
                "updateComplete": "complete" in statuses("update_goal"),
                "shellResultReturned": shell_ok, "result42": result_text == "42\n"}
    if scenario == "multi":
        spawned = {parsed(output).get("agent_id") for _, output in linked("spawn_agent", "multi_agent_v1")}
        spawned.discard(None)
        waited = [parsed(output).get("status", {}) for _, output in linked("wait_agent", "multi_agent_v1")]
        completed = {agent_id for status in waited for agent_id, state in status.items()
                     if isinstance(state, dict) and isinstance(state.get("completed"), str) and state["completed"]}
        child_histories = [item for record in records if
                           "x-openai-subagent" in record["body"].get("client_metadata", {})
                           for item in record["body"].get("input", [])]
        child_shell_ids = {item.get("call_id") for item in child_histories if
                           item.get("type") == "function_call" and item.get("name") == "exec_command"}
        return {"spawnReturnedAgentID": bool(spawned),
                "waitReturnedSpawnedCompletion": bool(spawned & completed),
                "closeResultReturned": bool(linked("close_agent", "multi_agent_v1")),
                "childShellResultReturned": any(item.get("type") == "function_call_output" and
                    item.get("call_id") in child_shell_ids and "Process exited with code 0" in str(item.get("output"))
                    for item in child_histories)}
    if scenario == "custom":
        return {"customResultReturned": any(call.get("type") == "custom_tool_call" and
                call_id in outputs for call_id, call in calls.items()), "result42": result_text == "42\n"}
    if scenario == "search":
        searches = [item for item in completed_items if item.get("type") == "web_search_call" and item.get("status") == "completed"]
        source_urls = {source["url"] for item in searches for source in item.get("action", {}).get("sources", []) or []
                       if isinstance(source, dict) and isinstance(source.get("url"), str)}
        messages = [content.get("text", "") for item in completed_items if item.get("type") == "message"
                    for content in item.get("content", []) if isinstance(content, dict)]
        return {"completedServerSearch": bool(searches), "nonemptySearchSources": bool(source_urls),
                "returnedSourceCited": any(url in message for url in source_urls for message in messages),
                "shellResultReturned": shell_ok}
    return {}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex", default=shutil.which("codex"))
    parser.add_argument("--expected-version", default="codex-cli 0.161.0")
    parser.add_argument("--router-binary", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--model", default="deepseek-v4-pro")
    parser.add_argument("--profile", choices=["default", "minimal"], default="default")
    parser.add_argument("--scenario", choices=["text", "tools", "goals", "multi", "search", "custom", "cancel", "error"], default="tools")
    parser.add_argument("--web-search", choices=["disabled", "cached", "live"], default="cached")
    parser.add_argument("--source-config", type=Path, help="Explicit authorized config copy; real cloud opt-in")
    parser.add_argument("--router-commit", default="unknown")
    parser.add_argument("--timeout", type=int, default=90)
    args = parser.parse_args()
    if args.source_config and not args.router_binary:
        parser.error("--source-config requires --router-binary")
    if not args.codex:
        parser.error("Codex executable is missing")
    args.output = args.output.resolve()
    args.output.mkdir(parents=True, exist_ok=False, mode=0o700)
    # Preserve runtime PATH, but no user's credentials, proxy or CLI settings.
    with tempfile.TemporaryDirectory(prefix="api-router-codex-0161-") as tmp:
        root = Path(tmp)
        root.chmod(0o700)
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
        product_origin = None
        record_lock = threading.Lock()
        scrub_ids = {}

        def scrub(text):
            text = text.replace(str(root), "<TEMP>")
            def stable_id(match):
                value = match.group(0)
                if re.search(r'https?://[^\s"<>]*$', text[max(0, match.start()-300):match.start()]):
                    return value
                if value not in scrub_ids:
                    scrub_ids[value] = f"00000000-0000-4000-8000-{len(scrub_ids)+1:012d}"
                return scrub_ids[value]
            text = re.sub(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}",
                          stable_id, text)
            return text

        class Handler(http.server.BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.0"

            def log_message(self, *_):
                pass

            def do_POST(self):
                try:
                    body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                    auth = self.headers.get("Authorization", "")
                    expected = "Bearer " + ("synthetic-upstream-key" if args.router_binary and not args.source_config else
                                             "api-router-local-placeholder")
                    if auth != expected:
                        raise AssertionError("unexpected upstream authorization source")
                    record = {"method": "POST", "path": self.path,
                              "authorizationSource": "public placeholder at product ingress" if args.source_config else "bound synthetic provider" if args.router_binary else "public placeholder",
                              "body": body}
                    with record_lock:
                        records.append(json.loads(scrub(json.dumps(record))))
                        index = len(records)
                        save(args.output / f"request-{index}.json", records[-1])
                    arrived.set()
                    if args.source_config:
                        if index > 12: raise RuntimeError("real cloud request budget exceeded")
                        req = urllib.request.Request(product_origin + self.path, json.dumps(body).encode(),
                            {"Content-Type": "application/json", "Authorization": "Bearer api-router-local-placeholder"})
                        try:
                            upstream = urllib.request.urlopen(req, timeout=args.timeout)
                        except urllib.error.HTTPError as error:
                            upstream = error
                        self.send_response(upstream.status)
                        self.send_header("Content-Type", upstream.headers.get("Content-Type", "application/json"))
                        self.end_headers()
                        chunks = []
                        with upstream:
                            while chunk := upstream.read1(8192):
                                chunks.append(chunk)
                                self.wfile.write(chunk)
                                self.wfile.flush()
                        (args.output / f"response-{index}.txt").write_text(scrub(b"".join(chunks).decode(errors="replace")))
                        return
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
                    emitted = []

                    def emit(typ, **data):
                        data["type"] = typ
                        wire = "event: " + typ + "\ndata: " + json.dumps(data) + "\n\n"
                        emitted.append(wire)
                        self.wfile.write(wire.encode())
                        self.wfile.flush()

                    response = {"id": f"resp_acceptance_{index}", "object": "response",
                                "status": "in_progress", "output": []}
                    emit("response.created", response=response)
                    preceding = []
                    if args.scenario in ("tools", "goals", "multi", "custom", "search"):
                        reasoning = {"type": "reasoning", "id": f"rs_{index}", "summary": [], "content": None, "encrypted_content": None}
                        emit("response.output_item.added", output_index=len(preceding), item=reasoning)
                        emit("response.output_item.done", output_index=len(preceding), item=reasoning)
                        preceding.append(reasoning)
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
                    history = [v for v in body.get("input", []) if v.get("type") in ("function_call_output", "custom_tool_call_output")]
                    commands = [
                        "cat input.txt",
                        "python3 -c \"from pathlib import Path; a,b=map(int,Path('input.txt').read_text().split()); Path('result.txt').write_text(str(a+b)+'\\n')\"",
                        "python3 -c \"from pathlib import Path; assert Path('result.txt').read_text() == '42\\n'; print('VERIFIED_42')\"",
                    ]
                    scripted = None
                    if args.scenario == "goals":
                        scripted = [("create_goal", {"objective": "Create result.txt containing 42 and verify it"}),
                                    ("get_goal", {}),
                                    ("exec_command", {"cmd": "printf '42\\n' > result.txt && test \"$(cat result.txt)\" = 42"}),
                                    ("update_goal", {"status": "complete"})]
                    if args.scenario == "multi":
                        is_child = "x-openai-subagent" in body.get("client_metadata", {})
                        if not is_child and len(history) == 0:
                            scripted = [("spawn_agent", {"message": "Read input.txt and respond with AGENT_42. Do not spawn agents."})]
                        elif not is_child and len(history) == 1:
                            output = history[0]['output']
                            if isinstance(output, str): output = json.loads(output)
                            agent_id = output.get('agent_id')
                            scripted = [("spawn_agent", {}), ("wait_agent", {"targets": [agent_id], "timeout_ms": 10000})]
                    if args.scenario == "custom":
                        scripted = [("exec", 'text(await tools.apply_patch("*** Begin Patch\\n*** Add File: result.txt\\n+42\\n*** End Patch\\n"));')]
                    if args.scenario == "search":
                        scripted = [("exec_command", {"cmd": "pwd", "max_output_tokens": 1000})]
                        if not history:
                            search = {"type": "web_search_call", "id": "ws_synthetic", "status": "completed",
                                      "action": {"type": "search", "query": "synthetic pathlib documentation fixture",
                                                 "sources": [{"type": "url", "url": "https://docs.python.org/3/library/pathlib.html"}]}}
                            emit("response.output_item.added", output_index=len(preceding), item=search)
                            emit("response.output_item.done", output_index=len(preceding), item=search)
                            preceding.append(search)
                    if scripted is not None and len(history) < len(scripted):
                        name, arguments = scripted[len(history)]
                        custom = args.scenario == "custom"
                        item = {"type": "custom_tool_call" if custom else "function_call", "id": f"fc_{index}",
                                "call_id": f"call_acceptance_{len(history)+1}", "name": name,
                                "status": "completed"}
                        item["input" if custom else "arguments"] = arguments if custom else json.dumps(arguments)
                        if args.scenario == "multi": item["namespace"] = "multi_agent_v1"
                        if custom: item["namespace"] = "functions"
                        emit("response.output_item.added", output_index=len(preceding), item=dict(item, status="in_progress"))
                    elif args.scenario == "tools" and len(history) < 3:
                        if index > 1 and not history:
                            raise AssertionError("host failed to return tool output history")
                        item = {"type": "function_call", "id": f"fc_{index}",
                                "call_id": f"call_acceptance_{len(history)+1}", "name": "exec_command",
                                "arguments": json.dumps({"cmd": commands[len(history)],
                                    "yield_time_ms": 1000, "max_output_tokens": 1000}), "status": "completed"}
                        emit("response.output_item.added", output_index=len(preceding),
                             item=dict(item, arguments="", status="in_progress"))
                        emit("response.function_call_arguments.delta", item_id=item["id"],
                             output_index=len(preceding), delta=item["arguments"])
                        emit("response.function_call_arguments.done", item_id=item["id"],
                             output_index=len(preceding), arguments=item["arguments"])
                    else:
                        if args.scenario == "tools":
                            assert "VERIFIED_42" in json.dumps(history), "verification result absent from next model request"
                        item = {"type": "message", "id": f"msg_{index}", "role": "assistant",
                                "status": "completed", "content": [{"type": "output_text",
                                "text": "AGENT_42" if args.scenario == "multi" and is_child else "ACCEPTANCE_42" if args.scenario == "tools" else "ACCEPTANCE_TEXT Synthetic fixture source https://docs.python.org/3/library/pathlib.html" if args.scenario == "search" else "ACCEPTANCE_TEXT", "annotations": []}]}
                        emit("response.output_item.added", output_index=len(preceding), item=dict(item, status="in_progress"))
                        emit("response.output_text.delta", item_id=item["id"], output_index=len(preceding),
                             content_index=0, delta=item["content"][0]["text"])
                    emit("response.output_item.done", output_index=len(preceding), item=item)
                    emit("response.completed", response=dict(response, status="completed", output=preceding+[item],
                         usage={"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}))
                    (args.output / f"response-{index}.txt").write_text(scrub("".join(emitted)))
                except (BrokenPipeError, ConnectionResetError):
                    pass
                except Exception as exc:
                    failures.append(str(exc))

        server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever)
        thread.start()
        router = None
        router_log = None
        cli = None
        try:
            base = f"http://127.0.0.1:{server.server_port}/app/codex/v1"
            if args.router_binary:
                binary = str(args.router_binary.resolve())
                subprocess.run([binary, "config", "init"], env=env, capture_output=True, check=True)
                layout = json.loads(subprocess.check_output([binary, "dirs"], env=env))
                config_file = Path(layout["config"]) / "config.json"
                if args.source_config:
                    for source in args.source_config.iterdir():
                        if source.is_file() and source.name in ("config.json", "credentials.json"):
                            shutil.copyfile(source, config_file.parent / source.name)
                            (config_file.parent / source.name).chmod(0o600)
                config = json.loads(config_file.read_text())
                if args.source_config:
                    credential_file = config_file.parent / "credentials.json"
                    credentials = json.loads(credential_file.read_text())
                    provider_id = config["apps"]["codex"]["cloud"]["providerId"]
                    provider = config["providers"][provider_id]
                    credentials["providers"][provider["credentialRef"]].get("extraBody", {}).pop("enable_thinking", None)
                    save(credential_file, credentials)
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
                    req = urllib.request.Request(origin + path, json.dumps(body).encode() if body is not None else None,
                        {"Authorization": "Bearer " + token, "Content-Type": "application/json"})
                    with urllib.request.urlopen(req, timeout=10) as resp:
                        return json.load(resp)

                if args.source_config:
                    apps = management("/api/apps", None)["apps"]
                    app = next(v for v in apps if v["id"] == "codex")
                    if app["cloud"]["model"] != args.model: raise RuntimeError("bound model mismatch")
                    app = {key: value for key, value in app.items() if key in
                           ("id", "revision", "name", "cloud", "credentialMode", "redactionEnabled", "referencePrice")}
                    app["cloud"]["capabilities"] = ["namespace", "web_search"]
                    management("/api/apps", app)
                else:
                    provider = management("/api/cloud-providers", {"name": "Synthetic Codex acceptance",
                    "protocol": "responses", "baseURL": f"http://127.0.0.1:{server.server_port}/v1",
                    "model": args.model, "apiKey": "synthetic-upstream-key", "proxyMode": "direct"})["provider"]
                    management("/api/apps", {"id": "codex", "name": "Codex", "revision": 0,
                        "cloud": {"providerId": provider["id"], "model": args.model,
                                  "capabilities": ["namespace", "custom", "additional_tools", "web_search"]}, "credentialMode": "stored"})
                base = origin + "/app/codex/v1"
                if args.source_config:
                    product_origin = origin
                    base = f"http://127.0.0.1:{server.server_port}/app/codex/v1"
            config_text = f'''model_provider = "api_router"
model = {json.dumps(args.model)}
web_search = {json.dumps(args.web_search)}
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
            command = [args.codex, "exec", "--strict-config", "--skip-git-repo-check",
                       "--json", "--ignore-rules", "-C", str(root / "project"), "-s", "workspace-write",
                       "Read input.txt, compute its sum into result.txt, verify with a command, then summarize."]
            prompts = {
                "tools": "Run exactly three separate sequential exec_command calls: first cat input.txt; second create result.txt containing the sum 42; third verify result.txt and print VERIFIED_42. Then report ACCEPTANCE_42. Do not spawn agents for this task.",
                "multi": "Use exactly one subagent to run exactly one exec_command that reads input.txt and prints its integer sum using Python. Require no other commands or file edits. Wait for its completed result, close the agent, then summarize the sum with the literal marker ACCEPTANCE_TEXT. Keep the fixed model inherited.",
                "goals": "Execute this exact ordered lifecycle with separate tool calls: (1) create_goal with objective to write result.txt containing 42 and verify it; (2) explicitly call get_goal and confirm its returned status is active, do not treat create_goal output as the read step; (3) use exec_command to write result.txt with 42 plus newline and verify it; (4) update_goal with status complete; (5) report the literal marker ACCEPTANCE_TEXT. Do not omit get_goal, do not spawn agents, and do not add a token budget.",
                "search": "Perform these steps strictly in order. First use server web_search to find the official Python documentation for pathlib.Path.read_text; do not run any local tool during this search phase. Next emit an assistant message explicitly citing a real URL returned by search and describing the search result. Only after that cited message, run pwd with exec_command. Finally output the literal string ACCEPTANCE_TEXT. Do not substitute memory for search, do not use curl, and do not spawn agents.",
                "custom": "Use apply_patch to create result.txt containing 42, then report ACCEPTANCE_TEXT. Do not spawn agents."}
            command[-1] = prompts.get(args.scenario, command[-1])
            cli = subprocess.Popen(command, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                   stderr=subprocess.PIPE, text=True, start_new_session=True)
            canceled = False
            if args.scenario == "cancel":
                if arrived.wait(10):
                    os.killpg(cli.pid, signal.SIGINT)
                    canceled = True
                else:
                    failures.append("no upstream request before cancellation deadline")
            try:
                stdout, stderr = cli.communicate(timeout=args.timeout)
            except subprocess.TimeoutExpired:
                os.killpg(cli.pid, signal.SIGKILL)
                stdout, stderr = cli.communicate()
                failures.append("bounded CLI timeout")
            (args.output / "events.jsonl").write_text(scrub(stdout))
            (args.output / "stderr.txt").write_text(scrub(stderr))
            (args.output / "config.toml").write_text(config_text.replace(base, "http://127.0.0.1:<PORT>/app/codex/v1"))
            save(args.output / "cli-result.json", {"exitCode": cli.returncode, "model": args.model,
                 "routerCommit": args.router_commit, "requests": len(records), "failures": failures})
            if canceled:
                disconnected.wait(5)
            release.set()
            result = root / "project" / "result.txt"
            agent_messages = []
            for line in stdout.splitlines():
                try: event = json.loads(line)
                except ValueError: continue
                if event.get("type") == "item.completed" and event.get("item", {}).get("type") == "agent_message":
                    agent_messages.append(event["item"].get("text", ""))
            final_marker = "ACCEPTANCE_42" if args.scenario == "tools" else "ACCEPTANCE_TEXT"
            final_marker_verified = bool(agent_messages) and final_marker in agent_messages[-1]
            passed = cli.returncode == 0 and final_marker_verified
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
            if args.source_config:
                # Real cloud requests are captured at the local ingress by the companion proxy.
                passed = cli.returncode == 0 and final_marker_verified
            completed_items = []
            for response_file in args.output.glob("response-*.txt"):
                for line in response_file.read_text().splitlines():
                    if not line.startswith("data: "): continue
                    event = json.loads(line[6:])
                    if event.get("type") == "response.completed":
                        completed_items.extend(event.get("response", {}).get("output", []))
            annotations = [a for item in completed_items if item.get("type") == "message" for content in item.get("content", []) for a in content.get("annotations", [])]
            checks = feature_checks(args.scenario, records, completed_items, result.read_text() if result.exists() else None)
            feature_verified = all(checks.values())
            model_pinned = all(v["body"].get("model") == args.model for v in records)
            passed = passed and feature_verified and model_pinned and not failures
            save(args.output / "summary.json", {"passed": passed, "evidenceLevel":
                 "real CLI via product with real authorized cloud" if args.source_config else
                 "real CLI via product with simulated upstream" if args.router_binary else "real CLI direct simulated upstream",
                 "version": version, "platform": os.uname().sysname + " " + os.uname().release,
                 "executable": args.codex, "model": args.model, "protocol": "responses",
                 "profile": args.profile, "scenario": args.scenario, "exitCode": cli.returncode,
                 "routerCommit": args.router_commit, "routerBinarySHA256": hashlib.sha256(args.router_binary.read_bytes()).hexdigest() if args.router_binary else None,
                 "featureVerified": feature_verified, "featureChecks": checks, "webSearch": args.web_search,
                 "finalMarkerVerified": final_marker_verified,
                 "targetCapabilities": ["namespace", "web_search"] if args.source_config else ["namespace", "custom", "additional_tools", "web_search"],
                 "responseAnnotations": len(annotations), "allRequestsModelPinned": model_pinned,
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
