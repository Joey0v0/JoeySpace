#!/usr/bin/env python3
"""Run one disposable, two-account smoke check against a deployed JoeySpace.

Uses only Python's standard library. Never prints passwords or login tokens.
This creates test users, a team, a group, a task, and a direct message.
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import secrets
import socket
import struct
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request


class CheckFailed(Exception):
    pass


def require(condition, message):
    if not condition:
        raise CheckFailed(message)


def http(base, method, path, token=None, body=None, key=None, timeout=12,
         allow_not_found=False):
    headers = {}
    if token:
        headers["Authorization"] = "Bearer " + token
    if key:
        headers["Idempotency-Key"] = key
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body, ensure_ascii=False).encode("utf-8")
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as response:
            status = response.status
            raw = response.read(1_000_001)
    except urllib.error.HTTPError as error:
        status = error.code
        raw = error.read(1_000_001)
    except urllib.error.URLError as error:
        raise CheckFailed("HTTP connection failed: " + str(error.reason)) from error
    require(len(raw) <= 1_000_000, "HTTP response too large")
    try:
        result = json.loads(raw)
    except (ValueError, UnicodeDecodeError) as error:
        raise CheckFailed("invalid JSON from " + path) from error
    if allow_not_found and status == 404:
        return None
    require(status == 200 and result.get("code") == 0,
            "%s %s failed: HTTP %s, code %s, msg %s" %
            (method, path, status, result.get("code"), result.get("msg")))
    return result.get("data") or {}


class WS:
    def __init__(self, host, port, token):
        self.sock = socket.create_connection((host, port), timeout=15)
        self.sock.settimeout(15)
        self.buffer = b""
        key = base64.b64encode(os.urandom(16)).decode("ascii")
        path = "/ws?" + urllib.parse.urlencode({"token": token})
        request = ("GET %s HTTP/1.1\r\nHost: %s:%d\r\nUpgrade: websocket\r\n"
                   "Connection: Upgrade\r\nSec-WebSocket-Key: %s\r\n"
                   "Sec-WebSocket-Version: 13\r\n\r\n") % (path, host, port, key)
        self.sock.sendall(request.encode("ascii"))
        while b"\r\n\r\n" not in self.buffer:
            require(len(self.buffer) < 16_384, "WebSocket handshake too large")
            part = self.sock.recv(4096)
            require(part, "WebSocket closed during handshake")
            self.buffer += part
        header, self.buffer = self.buffer.split(b"\r\n\r\n", 1)
        expected = base64.b64encode(hashlib.sha1(
            (key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").encode("ascii")
        ).digest()).decode("ascii")
        require(header.startswith(b"HTTP/1.1 101 "), "WebSocket upgrade failed")
        response_headers = {}
        for line in header.split(b"\r\n")[1:]:
            if b":" in line:
                name, value = line.split(b":", 1)
                response_headers[name.strip().lower()] = value.strip()
        require(response_headers.get(b"sec-websocket-accept") == expected.encode("ascii"),
                "invalid WebSocket handshake")

    def close(self):
        self.sock.close()

    def read_exact(self, count):
        while len(self.buffer) < count:
            part = self.sock.recv(max(4096, count - len(self.buffer)))
            require(part, "WebSocket closed before expected message")
            self.buffer += part
        result, self.buffer = self.buffer[:count], self.buffer[count:]
        return result

    def send_frame(self, opcode, payload):
        mask = os.urandom(4)
        size = len(payload)
        head = bytes([0x80 | opcode])
        if size < 126:
            head += bytes([0x80 | size])
        elif size < 65_536:
            head += bytes([0x80 | 126]) + struct.pack("!H", size)
        else:
            head += bytes([0x80 | 127]) + struct.pack("!Q", size)
        masked = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
        self.sock.sendall(head + mask + masked)

    def send_json(self, value):
        self.send_frame(1, json.dumps(value, ensure_ascii=False).encode("utf-8"))

    def recv_json(self):
        while True:
            first, second = self.read_exact(2)
            require(first & 0x80, "fragmented WebSocket frame unsupported")
            size = second & 0x7f
            if size == 126:
                size = struct.unpack("!H", self.read_exact(2))[0]
            elif size == 127:
                size = struct.unpack("!Q", self.read_exact(8))[0]
            require(size <= 1_000_000, "WebSocket frame too large")
            mask = self.read_exact(4) if second & 0x80 else None
            payload = self.read_exact(size)
            if mask:
                payload = bytes(value ^ mask[index % 4] for index, value in enumerate(payload))
            opcode = first & 0x0f
            if opcode == 9:
                self.send_frame(10, payload)
                continue
            require(opcode != 8, "WebSocket closed before expected message")
            if opcode == 1:
                return json.loads(payload)


def receive_message(ws, expected_type, msg_id):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        ws.sock.settimeout(max(0.1, deadline - time.monotonic()))
        try:
            message = ws.recv_json()
        except socket.timeout as error:
            raise CheckFailed("timed out waiting for WebSocket " + expected_type) from error
        if message.get("type") == "error":
            raise CheckFailed("WebSocket returned an error: " + str(message.get("data")))
        if message.get("type") == expected_type and message.get("data", {}).get("msg_id") == msg_id:
            return message
    raise CheckFailed("timed out waiting for " + expected_type)


def receive_hint(ws, notification_id, team_id):
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        ws.sock.settimeout(max(0.1, deadline - time.monotonic()))
        try:
            message = ws.recv_json()
        except socket.timeout as error:
            raise CheckFailed("timed out waiting for online task hint") from error
        if message.get("type") == "error":
            raise CheckFailed("WebSocket returned an error: " + str(message.get("data")))
        data = message.get("data") or {}
        if (message.get("type") == "task_notification_changed" and
                data.get("version") == 1 and data.get("notification_id") == notification_id and
                data.get("team_id") == team_id):
            return
    raise CheckFailed("timed out waiting for online task hint")


def full_preflight():
    """Reject a base-only deployment before a group send can block Push's chat reader."""
    directory = Path(__file__).resolve().parent
    values = {}
    env_file = directory / ".env"
    require(env_file.is_file(), "full checks need deploy/.env and optional overlays")
    for line in env_file.read_text(encoding="utf-8").splitlines():
        if "=" in line and not line.lstrip().startswith("#"):
            name, value = line.split("=", 1)
            values[name.strip()] = value.strip().strip("\"'")
    cert_names = ("IM_BOT_CERT_DIR", "AGENT_BOT_CERT_DIR", "USER_TRIGGER_CERT_DIR",
                  "IM_TRIGGER_CERT_DIR", "IM_TRIGGER_USER_CERT_DIR", "AGENT_TRIGGER_CERT_DIR",
                  "WS_NOTIFICATION_CERT_DIR", "PUSH_NOTIFICATION_CERT_DIR",
                  "USER_LEAVE_CERT_DIR", "IM_LEAVE_CERT_DIR", "USER_PUSH_CERT_DIR",
                  "PUSH_USER_CERT_DIR")
    for name in cert_names:
        folder = Path(values.get(name, ""))
        require(folder.is_absolute() and all((folder / part).is_file()
                for part in ("cert.pem", "key.pem", "ca.pem")),
                "full checks need private %s with cert.pem, key.pem and ca.pem" % name)
    yaml_file = directory / "docker-config.local.yaml"
    require(yaml_file.is_file(), "full checks need deploy/docker-config.local.yaml")
    switches = {}
    stack = []
    for raw in yaml_file.read_text(encoding="utf-8").splitlines():
        line = raw.split("#", 1)[0].rstrip()
        if not line or ":" not in line:
            continue
        indent = len(line) - len(line.lstrip(" "))
        key, value = line.strip().split(":", 1)
        while stack and stack[-1][0] >= indent:
            stack.pop()
        path = tuple(part for _, part in stack) + (key,)
        if value.strip():
            switches[path] = value.strip().strip("\"'").lower()
        else:
            stack.append((indent, key))
    for path in (("kafka", "agent_trigger_enabled"),
                 ("task_notifications", "push", "enabled"),
                 ("task_notifications", "ws", "enabled")):
        require(switches.get(path) == "true", "full checks need %s=true in private YAML" %
                ".".join(path))
    compose = ["docker", "compose", "--env-file", ".env", "--profile", "agent"]
    for filename in ("docker-compose.yaml", "docker-compose.bot.yaml",
                     "docker-compose.trigger.yaml", "docker-compose.notifications.yaml",
                     "docker-compose.team-leave.yaml"):
        compose += ["-f", filename]
    try:
        config = subprocess.run(compose + ["config", "--quiet"], cwd=directory,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    except OSError as error:
        raise CheckFailed("Docker Compose is required for full preflight") from error
    require(config.returncode == 0, "full Compose overlay configuration is invalid")
    markers = {"user-rpc": ("USER_PUSH_LISTEN_ON", "USER_TRIGGER_LISTEN_ON"),
               "im-rpc": ("IM_BOT_LISTEN_ON", "IM_TRIGGER_LISTEN_ON"),
               "im-push": ("PUSH_USER_RPC_ADDR",),
               "im-ws": (),
               "agent-rpc": ("AGENT_IM_BOT_ADDR", "AGENT_TRIGGER_WORKER_ENABLED"),
               "task-rpc": ("TASK_NOTIFICATION_PUBLISH_ENABLED",)}
    for service, required in markers.items():
        found = subprocess.run(compose + ["ps", "-q", service], cwd=directory,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        container_id = found.stdout.decode("utf-8", "replace").strip()
        require(found.returncode == 0 and container_id, "full checks need running %s" % service)
        result = subprocess.run(["docker", "inspect", "--format",
                                 "{{json .Config.Env}}", container_id], cwd=directory,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
        require(result.returncode == 0, "cannot inspect running %s" % service)
        try:
            env = dict(item.split("=", 1) for item in json.loads(result.stdout) if "=" in item)
        except (ValueError, TypeError) as error:
            raise CheckFailed("invalid container configuration for %s" % service) from error
        require(all(env.get(name) for name in required),
                "running %s lacks an optional overlay; restart with the full Compose set" % service)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gateway", default="http://127.0.0.1:8082")
    parser.add_argument("--ws-host", default="127.0.0.1")
    parser.add_argument("--ws-port", type=int, default=8081)
    parser.add_argument("--group-chat", action="store_true",
                        help="also require group Push delivery; needs team-leave mTLS overlay")
    parser.add_argument("--agent-ask", action="store_true",
                        help="also require one real Agent model answer; needs agent-rpc and model credentials")
    parser.add_argument("--full", action="store_true",
                        help="require configured mTLS overlays, group delivery, Agent, bot reply and online task hint")
    args = parser.parse_args()
    if args.full:
        args.agent_ask = True
        args.group_chat = True
    base = args.gateway.rstrip("/")
    require(base.startswith("http://127.0.0.1:") or base.startswith("http://localhost:"),
            "smoke check only accepts a loopback Gateway URL")
    if args.full:
        try:
            full_preflight()
        except CheckFailed as error:
            print("NOT READY: " + str(error), file=sys.stderr)
            return 2
    suffix = secrets.token_hex(5)
    usernames = ["js_smoke_a_" + suffix, "js_smoke_b_" + suffix]
    password = secrets.token_urlsafe(24)
    sockets = []
    completed = []
    stage = "creating test accounts"
    try:
        for name in usernames:
            http(base, "POST", "/api/v1/user/register",
                 body={"username": name, "password": password, "nickname": name})
        tokens = [http(base, "POST", "/api/v1/user/login",
                       body={"username": name, "password": password})["token"]
                  for name in usernames]
        ids = [http(base, "GET", "/api/v1/user/info", token=t)["id"] for t in tokens]
        require(ids[0] != ids[1], "test accounts have the same user ID")
        completed.append("two-account registration, login and profile")

        stage = "team and group setup"
        team = http(base, "POST", "/api/v1/teams", token=tokens[0],
                    body={"name": "smoke-" + suffix})["team_id"]
        http(base, "POST", "/api/v1/teams/%s/members" % team,
             token=tokens[0], body={"user_id": ids[1]})
        group = http(base, "POST", "/api/v1/teams/%s/groups" % team,
                     token=tokens[0], key="group-" + suffix,
                     body={"name": "smoke-" + suffix})["group_id"]
        http(base, "POST", "/api/v1/teams/%s/groups/%s/join" % (team, group), token=tokens[1])
        completed.append("team creation, member addition and group join")

        if args.agent_ask:
            stage = "Agent model answer"
            answer = http(base, "POST", "/api/v1/teams/%s/groups/%s/ask" % (team, group),
                          token=tokens[0],
                          body={"question": "请用一句话说明这个新建群目前是否有消息可供总结。"},
                          timeout=25).get("answer")
            require(isinstance(answer, str) and answer.strip(), "Agent returned an empty answer")
            completed.append("Agent real model answer")

        if args.full:
            stage = "online task hint receiver handshake"
            sender = WS(args.ws_host, args.ws_port, tokens[0])
            sockets.append(sender)
            time.sleep(0.5)
        stage = "task and notification checks"
        task = http(base, "POST", "/api/v1/teams/%s/tasks" % team,
                    token=tokens[0], key="task-" + suffix,
                    body={"title": "Smoke task " + suffix, "assignee_id": ids[1]})["task_id"]
        tasks = http(base, "GET", "/api/v1/teams/%s/tasks?limit=20" % team, token=tokens[1])
        require(any(row["task_id"] == task and row["assignee_id"] == ids[1]
                    for row in tasks["tasks"]), "assignee cannot see created task")
        http(base, "PUT", "/api/v1/teams/%s/tasks/%s/status" % (team, task),
             token=tokens[1], body={"status": 1})
        notices = http(base, "GET", "/api/v1/teams/%s/task-notifications?limit=20" % team,
                       token=tokens[0])["notifications"]
        matching = [row for row in notices if row["task_id"] == task
                    and row["from_status"] == 0 and row["to_status"] == 1]
        require(len(matching) == 1 and matching[0]["read_at_unix_ms"] == 0,
                "creator notification missing or already read")
        if args.full:
            stage = "online task hint"
            receive_hint(sender, matching[0]["notification_id"], team)
            completed.append("Task -> Kafka -> Push -> WebSocket online hint")
        read = http(base, "PUT", "/api/v1/teams/%s/task-notifications/%s/read" %
                    (team, matching[0]["notification_id"]), token=tokens[0])
        require(read["read_at_unix_ms"] > 0, "notification was not marked read")
        completed.append("task creation, cross-account status and persisted notification")

        stage = "receiver WebSocket handshake"
        receiver = WS(args.ws_host, args.ws_port, tokens[1])
        sockets.append(receiver)
        if not args.full:
            stage = "sender WebSocket handshake"
            sender = WS(args.ws_host, args.ws_port, tokens[0])
            sockets.append(sender)
        time.sleep(0.5)  # Allow the receiver's online route to reach Redis.
        msg_id = "smoke-direct-" + suffix
        stage = "direct-message send"
        sender.send_json({"type": "chat", "data": {"msg_id": msg_id, "to_id": ids[1],
                                                 "chat_type": 1, "content_type": 1,
                                                 "content": "smoke " + suffix}})
        stage = "direct-message Kafka acknowledgement"
        receive_message(sender, "ack", msg_id)
        stage = "direct-message online delivery"
        receive_message(receiver, "chat", msg_id)
        completed.append("WebSocket -> Kafka -> Push -> WebSocket direct message")

        if args.group_chat:
            msg_id = "smoke-group-" + suffix
            stage = "team-group send"
            sender.send_json({"type": "chat", "data": {"msg_id": msg_id, "to_id": group,
                                                     "chat_type": 2, "content_type": 1,
                                                     "content": "smoke " + suffix}})
            stage = "team-group Kafka acknowledgement"
            receive_message(sender, "ack", msg_id)
            stage = "team-group online delivery"
            receive_message(receiver, "chat", msg_id)
            completed.append("team-group delivery with current membership checks")
        if args.full:
            trigger_msg_id = "smoke-trigger-" + suffix
            stage = "background @AI trigger send"
            sender.send_json({"type": "chat", "data": {"msg_id": trigger_msg_id,
                             "to_id": group, "chat_type": 2, "content_type": 1,
                             "content": "@AI 整理任务 请创建一项标题为验收机器人回帖的测试任务，不指定负责人和截止时间。"}})
            receive_message(sender, "ack", trigger_msg_id)
            receive_message(receiver, "chat", trigger_msg_id)
            stage = "background @AI source persistence"
            deadline = time.monotonic() + 20
            source_id = None
            while source_id is None:
                history = http(base, "GET", "/api/v1/teams/%s/groups/%s/messages?limit=30" %
                               (team, group), token=tokens[0])["messages"]
                source_id = next((row["id"] for row in history
                                  if row.get("msg_id") == trigger_msg_id), None)
                require(source_id or time.monotonic() < deadline,
                        "@AI source message is absent from group history")
                if source_id is None:
                    time.sleep(0.5)
            stage = "background @AI trigger completion"
            deadline = time.monotonic() + 90
            run = None
            while run is None:
                state = http(base, "GET", "/api/v1/teams/%s/groups/%s/agent-triggers/%s" %
                             (team, group, source_id), token=tokens[0], allow_not_found=True)
                if state is not None:
                    require(state.get("status") != "exhausted", "@AI trigger exhausted its model budget")
                    if state.get("status") == "completed":
                        run = state.get("run_id")
                require(run or time.monotonic() < deadline, "@AI trigger did not complete in 90s")
                if run is None:
                    time.sleep(1)
            completed.append("group @AI -> Kafka -> Agent draft")
            stage = "Agent draft review"
            collection = http(base, "GET", "/api/v1/agent/runs/%s/drafts" % run,
                              token=tokens[0])
            items = collection.get("items") or []
            require(collection.get("item_count") == 1 and len(items) == 1,
                    "generated collection needs manual review; no task was confirmed")
            item = items[0]
            draft = item.get("draft") or {}
            require(item.get("status") == "waiting_confirmation" and draft.get("title") and
                    draft.get("revision") and draft.get("assignee_id") == "0" and
                    draft.get("due_at_unix_ms") == 0 and
                    (draft.get("deadline") or {}).get("resolution") == "none",
                    "generated draft needs manual review; no task was confirmed")
            stage = "Agent draft confirmation and bot reply"
            confirmed = http(base, "POST", "/api/v1/agent/runs/%s/drafts/0/confirm" % run,
                             token=tokens[0], body={
                                 "expected_revision": draft["revision"],
                                 "expected_title": draft["title"],
                                 "expected_description": draft["description"],
                                 "expected_assignee_id": draft["assignee_id"],
                                 "expected_due_at_unix_ms": draft["due_at_unix_ms"],
                                 "expected_deadline_resolution": "none",
                             }, timeout=25)["item"]
            reply_id = confirmed.get("reply_msg_id")
            require(confirmed.get("status") == "succeeded" and
                    confirmed.get("reply_status") == "accepted" and reply_id,
                    "task was confirmed but bot reply is not accepted; inspect this run before retrying")
            stage = "bot reply online delivery"
            receive_message(receiver, "chat", reply_id)
            stage = "bot reply persisted history"
            deadline = time.monotonic() + 20
            while True:
                history = http(base, "GET", "/api/v1/teams/%s/groups/%s/messages?limit=30" %
                               (team, group), token=tokens[1])["messages"]
                if any(row.get("msg_id") == reply_id and row.get("sender_type") == 2
                       for row in history):
                    break
                require(time.monotonic() < deadline, "accepted bot reply is absent from group history")
                time.sleep(0.5)
            completed.append("Agent draft -> confirmation -> bot reply online and persisted")
        print("PASS: " + "; ".join(completed))
        if not args.group_chat:
            print("NOT CHECKED: team-group Push delivery (requires team-leave mTLS overlay)")
        if args.full:
            print("NOT CHECKED: failure-recovery, revoked-access and browser interaction scenarios")
        elif args.agent_ask:
            print("NOT CHECKED: bot reply and real-time task hints (require optional overlays)")
        else:
            print("NOT CHECKED: Agent/model, bot reply and real-time task hints (require optional overlays)")
        print("TEST DATA: users %s, %s; team %s; group %s; task %s" %
              (usernames[0], usernames[1], team, group, task))
        return 0
    except (CheckFailed, KeyError, OSError, ValueError, socket.timeout) as error:
        print("FAIL after: " + ("; ".join(completed) or "no completed checks"), file=sys.stderr)
        print("FAILED STAGE: " + stage, file=sys.stderr)
        print("REASON: " + str(error), file=sys.stderr)
        print("Test data already created is retained; do not rerun with the same generated users.",
              file=sys.stderr)
        return 1
    finally:
        for connection in sockets:
            connection.close()


if __name__ == "__main__":
    sys.exit(main())
