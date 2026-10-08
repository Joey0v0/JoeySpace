#!/usr/bin/env python3
"""Check Push recovery and team-leave revocation on a full cloud deployment.

Creates disposable accounts and data. Briefly stops the shared im-push service;
run only when that interruption is acceptable. Never prints credentials.
"""

import argparse
import json
from pathlib import Path
import runpy
import secrets
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.request


smoke = runpy.run_path(str(Path(__file__).with_name("verify-cloud.py")))
CheckFailed = smoke["CheckFailed"]
WS = smoke["WS"]
http = smoke["http"]
require = smoke["require"]
full_preflight = smoke["full_preflight"]


def compose(*args):
    directory = Path(__file__).resolve().parent
    command = ["docker", "compose", "--env-file", ".env", "-f",
               "docker-compose.yaml", *args]
    try:
        result = subprocess.run(command, cwd=directory, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=90, check=False)
    except (OSError, subprocess.TimeoutExpired) as error:
        raise CheckFailed("docker compose %s could not finish" % " ".join(args)) from error
    require(result.returncode == 0, "docker compose %s failed" % " ".join(args))


def expect_forbidden(base, method, path, token, body=None):
    data = json.dumps(body).encode("utf-8") if body is not None else None
    headers = {"Authorization": "Bearer " + token}
    if data is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=12) as response:
            status, raw = response.status, response.read(1_001)
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read(1_001)
    except urllib.error.URLError as error:
        raise CheckFailed("HTTP connection failed during revocation check") from error
    require(status == 403, "%s %s should be forbidden after leave; got HTTP %s" %
            (method, path, status))
    try:
        result = json.loads(raw)
    except (ValueError, UnicodeDecodeError) as error:
        raise CheckFailed("invalid JSON in forbidden response") from error
    require(result.get("code") not in (None, 0), "forbidden response has no error code")


def wait_frame(ws, expected_type, msg_id=None, timeout=60):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        ws.sock.settimeout(max(0.1, deadline - time.monotonic()))
        try:
            message = ws.recv_json()
        except socket.timeout as error:
            raise CheckFailed("timed out waiting for WebSocket " + expected_type) from error
        if message.get("type") == "error" and expected_type != "error":
            raise CheckFailed("WebSocket returned an error: " + str(message.get("data")))
        if message.get("type") == expected_type and (msg_id is None or
                (message.get("data") or {}).get("msg_id") == msg_id):
            return message
    raise CheckFailed("timed out waiting for WebSocket " + expected_type)


def run(args):
    base = args.gateway.rstrip("/")
    require(base.startswith("http://127.0.0.1:") or base.startswith("http://localhost:"),
            "smoke check only accepts a loopback Gateway URL")
    require(args.allow_push_stop, "pass --allow-push-stop to acknowledge a brief im-push outage")
    full_preflight()

    suffix = secrets.token_hex(5)
    names = ["js_recovery_a_" + suffix, "js_recovery_b_" + suffix]
    password = secrets.token_urlsafe(24)
    sockets = []
    completed = []
    stage = "creating test accounts"
    team = group = None
    try:
        for name in names:
            http(base, "POST", "/api/v1/user/register",
                 body={"username": name, "password": password, "nickname": name})
        tokens = [http(base, "POST", "/api/v1/user/login",
                       body={"username": name, "password": password})["token"] for name in names]
        ids = [http(base, "GET", "/api/v1/user/info", token=token)["id"] for token in tokens]
        require(ids[0] != ids[1], "test accounts have the same user ID")
        team = http(base, "POST", "/api/v1/teams", token=tokens[0],
                    body={"name": "recovery-" + suffix})["team_id"]
        http(base, "POST", "/api/v1/teams/%s/members" % team,
             token=tokens[0], body={"user_id": ids[1]})
        group = http(base, "POST", "/api/v1/teams/%s/groups" % team,
                     token=tokens[0], key="group-" + suffix,
                     body={"name": "recovery-" + suffix})["group_id"]
        http(base, "POST", "/api/v1/teams/%s/groups/%s/join" % (team, group), token=tokens[1])
        task = http(base, "POST", "/api/v1/teams/%s/tasks" % team, token=tokens[0],
                    key="task-" + suffix,
                    body={"title": "Recovery task " + suffix, "assignee_id": ids[1]})["task_id"]
        tasks_path = "/api/v1/teams/%s/tasks?limit=20" % team
        group_path = "/api/v1/teams/%s/groups/%s/messages?limit=20" % (team, group)
        require(any(row["task_id"] == task for row in
                    http(base, "GET", tasks_path, token=tokens[1])["tasks"]),
                "member cannot read task before leaving")
        http(base, "GET", group_path, token=tokens[1])
        sender = WS(args.ws_host, args.ws_port, tokens[0])
        sockets.append(sender)
        receiver = WS(args.ws_host, args.ws_port, tokens[1])
        sockets.append(receiver)
        time.sleep(0.5)
        completed.append("test accounts, team and baseline access")

        stage = "stop Push, publish direct message, restore Push"
        msg_id = "smoke-recovery-" + suffix
        direct_path = "/api/v1/direct/%s/messages?limit=20" % ids[0]
        try:
            compose("stop", "im-push")
            sender.send_json({"type": "chat", "data": {"msg_id": msg_id,
                             "to_id": ids[1], "chat_type": 1, "content_type": 1,
                             "content": "recovery " + suffix}})
            wait_frame(sender, "ack", msg_id, timeout=20)
            before = http(base, "GET", direct_path, token=tokens[1])["messages"]
            require(not any(row.get("msg_id") == msg_id for row in before),
                    "message was persisted while im-push was stopped")
        finally:
            try:
                compose("start", "im-push")
            except CheckFailed as error:
                raise CheckFailed("CRITICAL: im-push could not be restarted; restore it manually") from error
        stage = "Push recovery and durable message delivery"
        wait_frame(receiver, "chat", msg_id, timeout=60)
        deadline = time.monotonic() + 20
        while True:
            history = http(base, "GET", direct_path, token=tokens[1])["messages"]
            if sum(row.get("msg_id") == msg_id for row in history) == 1:
                break
            require(time.monotonic() < deadline, "recovered message is not in direct history exactly once")
            time.sleep(0.5)
        completed.append("Push restart: queued message delivered online and persisted once")

        stage = "leave team and verify revocation"
        leave_path = "/api/v1/teams/%s/leave" % team
        leave_key = "leave-" + suffix
        result = http(base, "POST", leave_path, token=tokens[1],
                      body={"request_key": leave_key}, timeout=15)
        require(result.get("status") == 1 and result.get("team_id") == team,
                "team leave did not complete")
        operation = http(base, "GET", leave_path + "?request_key=" + leave_key,
                         token=tokens[1])
        require(operation.get("status") == 1 and
                operation.get("operation_id") == result.get("operation_id"),
                "completed team leave was not retained")
        expect_forbidden(base, "GET", group_path, tokens[1])
        expect_forbidden(base, "GET", tasks_path, tokens[1])
        expect_forbidden(base, "GET", "/api/v1/teams/%s/task-notifications?limit=20" % team,
                         tokens[1])
        expect_forbidden(base, "POST", "/api/v1/teams/%s/groups/%s/join" % (team, group),
                         tokens[1])
        receiver.send_json({"type": "chat", "data": {"msg_id": "smoke-revoked-" + suffix,
                            "to_id": group, "chat_type": 2, "content_type": 1,
                            "content": "revoked " + suffix}})
        denied = wait_frame(receiver, "error", timeout=12)
        require((denied.get("data") or {}).get("code") == 403,
                "revoked WebSocket group send was not denied with 403")
        http(base, "GET", group_path, token=tokens[0])
        http(base, "GET", tasks_path, token=tokens[0])
        http(base, "GET", "/api/v1/user/info", token=tokens[1])
        completed.append("team leave: history, tasks, notices, rejoin and group send denied")
        print("PASS: " + "; ".join(completed))
        print("NOT CHECKED: browser interaction; database/server-wide crash recovery")
        print("TEST DATA: users %s, %s; team %s; group %s; task %s" %
              (names[0], names[1], team, group, task))
        return 0
    except (CheckFailed, KeyError, OSError, ValueError, socket.timeout) as error:
        print("FAIL after: " + ("; ".join(completed) or "no completed checks"), file=sys.stderr)
        print("FAILED STAGE: " + stage, file=sys.stderr)
        print("REASON: " + str(error), file=sys.stderr)
        print("Test data already created is retained; a rerun creates new users.", file=sys.stderr)
        return 1
    finally:
        for connection in sockets:
            connection.close()


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gateway", default="http://127.0.0.1:8082")
    parser.add_argument("--ws-host", default="127.0.0.1")
    parser.add_argument("--ws-port", type=int, default=8081)
    parser.add_argument("--allow-push-stop", action="store_true",
                        help="allow this check to briefly stop the shared im-push service")
    try:
        sys.exit(run(parser.parse_args()))
    except CheckFailed as error:
        print("NOT READY: " + str(error), file=sys.stderr)
        sys.exit(2)
