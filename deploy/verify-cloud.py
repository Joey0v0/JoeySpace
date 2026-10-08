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
import secrets
import socket
import struct
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


def http(base, method, path, token=None, body=None, key=None):
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
        with urllib.request.urlopen(req, timeout=12) as response:
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--gateway", default="http://127.0.0.1:8082")
    parser.add_argument("--ws-host", default="127.0.0.1")
    parser.add_argument("--ws-port", type=int, default=8081)
    parser.add_argument("--group-chat", action="store_true",
                        help="also require group Push delivery; needs team-leave mTLS overlay")
    args = parser.parse_args()
    base = args.gateway.rstrip("/")
    require(base.startswith("http://127.0.0.1:") or base.startswith("http://localhost:"),
            "smoke check only accepts a loopback Gateway URL")
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
        read = http(base, "PUT", "/api/v1/teams/%s/task-notifications/%s/read" %
                    (team, matching[0]["notification_id"]), token=tokens[0])
        require(read["read_at_unix_ms"] > 0, "notification was not marked read")
        completed.append("task creation, cross-account status and persisted notification")

        stage = "receiver WebSocket handshake"
        receiver = WS(args.ws_host, args.ws_port, tokens[1])
        sockets.append(receiver)
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
        print("PASS: " + "; ".join(completed))
        if not args.group_chat:
            print("NOT CHECKED: team-group Push delivery (requires team-leave mTLS overlay)")
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
