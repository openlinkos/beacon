"""Run with a local Beacon executable. All credentials, state and listeners are disposable."""

import contextlib
from datetime import datetime, timedelta, timezone
from hashlib import sha256
import hmac
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path
from queue import Empty, Queue
import re
import secrets
import socket
import subprocess
import sys
from tempfile import TemporaryDirectory
from threading import Thread
import time
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, ProxyHandler, Request, build_opener


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *_):
        return None


client = build_opener(ProxyHandler({}), NoRedirect())
fixture_labels = {"alertname": "ServiceDown", "service": "notification-smoke", "severity": "critical"}


def signature(secret, body):
    return "sha256=" + hmac.new(secret, body, sha256).hexdigest()


class Receiver(BaseHTTPRequestHandler):
    def do_POST(self):
        self.server.paths.add(self.path)
        if self.path not in ("/notifications", "/replacement"):
            self.send_error(404)
            return
        if self.headers.get_content_type() != "application/json":
            self.send_error(415)
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            length = 0
        if not 0 < length <= 65536:
            self.send_error(400)
            return
        body = self.rfile.read(length)
        supplied = self.headers.get("X-Beacon-Signature", "")
        if not supplied.isascii() or not hmac.compare_digest(signature(self.server.secret, body), supplied):
            self.server.rejected_signatures.put(401)
            self.send_error(401)
            return
        try:
            event = json.loads(body)
            if type(event["version"]) is not int or event["version"] != 1:
                raise ValueError("unexpected version")
            if event["event"] not in ("firing", "resolved") or not isinstance(event["delivery"], str) or not event["delivery"]:
                raise ValueError("unexpected delivery")
            if datetime.fromisoformat(event["at"].replace("Z", "+00:00")).utcoffset() is None:
                raise ValueError("missing event timestamp zone")
        except (AttributeError, KeyError, TypeError, ValueError):
            self.send_error(400)
            return
        delivery = event["delivery"]
        duplicate = delivery in self.server.accepted
        # Reject the first signed request. Persist nothing until returning a successful code.
        code = 503 if not self.server.rejected_first else 204
        self.server.rejected_first = True
        if code == 204 and not duplicate:
            self.server.accepted[delivery] = event
        self.send_response(code)
        self.end_headers()
        self.server.attempts.put((code, duplicate, event, body))

    def log_message(self, *_):
        pass


class PushoverReceiver(BaseHTTPRequestHandler):
    def reply(self, status, value):
        body = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        form = parse_qs(self.rfile.read(int(self.headers["Content-Length"])).decode())
        if form.get("token") != [self.server.token]:
            self.reply(401, {"status": 0})
            self.server.attempts.put(("invalid", "", False))
            return
        if self.path == "/1/messages.json":
            if form.get("user") != [self.server.user] or form.get("priority") != ["2"]:
                self.reply(400, {"status": 0})
                self.server.attempts.put(("invalid", "", False))
                return
            receipt = secrets.token_hex(15)
            self.server.receipts.add(receipt)
            self.reply(200, {"status": 1, "receipt": receipt})
            self.server.attempts.put(("sent", receipt, True))
            return
        match = re.fullmatch(r"/1/receipts/([a-f0-9]{30})/cancel\.json", self.path)
        if not match or match[1] not in self.server.receipts:
            self.reply(404, {"status": 0})
            self.server.attempts.put(("invalid", "", False))
            return
        accepted = self.server.rejected_first_cancel
        self.server.rejected_first_cancel = True
        self.reply(200, {"status": int(accepted)})
        self.server.attempts.put(("cancel", match[1], accepted))

    def do_GET(self):
        target = urlsplit(self.path)
        match = re.fullmatch(r"/1/receipts/([a-f0-9]{30})\.json", target.path)
        if match and match[1] in self.server.receipts and parse_qs(target.query).get("token") == [self.server.token]:
            self.reply(200, {"status": 1, "acknowledged": 0, "expired": 0})
            return
        self.reply(404, {"status": 0})

    def log_message(self, *_):
        pass


def request(url, *, data=None, headers=None):
    with client.open(Request(url, data=data, headers=headers or {}), timeout=4) as r:
        return r.read()


def console_overview(base, headers):
    try:
        return request(base + "/console", headers=headers)
    except HTTPError as err:
        if err.code != 302 or err.headers.get("Location") != "/workspace/":
            raise
    return request(base + "/console/api/v1/overview", headers=headers)


def wait_for(description, condition, seconds=20):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            if condition():
                return
        except (URLError, TimeoutError):
            pass
        time.sleep(0.2)
    raise RuntimeError(f"Timed out waiting for {description}")


def attempt(receiver, description, seconds):
    try:
        return receiver.attempts.get(timeout=seconds)
    except Empty as err:
        raise RuntimeError(f"Timed out waiting for {description}") from err


@contextlib.contextmanager
def beacon(binary, config, log_path, base_url):
    with log_path.open("ab") as log:
        process = subprocess.Popen([binary, "serve", "-config", config], stdout=log, stderr=log)
        try:
            def ready():
                if process.poll() is not None:
                    raise RuntimeError(f"Beacon exited with code {process.returncode}")
                request(base_url + "/healthz")
                return True

            wait_for("Beacon health", ready)
            yield
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait()


def port_reservation():
    reserved = socket.socket()
    reserved.bind(("127.0.0.1", 0))
    return reserved


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def show_fixture_log(log, values):
    if not log.exists():
        return
    output = log.read_text(errors="replace")
    for value in values:
        output = output.replace(value, "[fixture value]")
    output = re.sub(r'([?&](?:code|s)=)[^&"\s]+', r'\1[fixture link]', output)
    print(output[-6000:], file=sys.stderr)


def cookie_header(headers):
    cookies = SimpleCookie()
    for value in headers.get_all("Set-Cookie", []):
        cookies.load(value)
    return "; ".join(f"{name}={c.coded_value}" for name, c in cookies.items() if c.value)


def owner_session(binary, state, base):
    setup = subprocess.run([binary, "setup"], env={**os.environ, "BEACON_DATA_DIR": str(state)},
        capture_output=True, check=True, text=True, timeout=10).stdout.strip()
    with client.open(setup, timeout=4) as page:
        form = re.search(r'name="form" value="([^"]+)"', page.read().decode())
        setup_cookie = cookie_header(page.headers)
    require(form and setup_cookie, "Could not open the console owner setup form")
    password = secrets.token_hex(16)
    data = urlencode({"form": form[1], "code": parse_qs(urlsplit(setup).query)["code"][0],
        "username": "fixture-owner", "password": password, "again": password}).encode()
    try:
        request(base + "/console/setup", data=data, headers={
            "Cookie": setup_cookie, "Content-Type": "application/x-www-form-urlencoded"})
    except HTTPError as response:
        require(response.code == 303, "Console owner setup did not redirect")
        session = cookie_header(response.headers)
        require(session, "Console owner setup did not establish a session")
        return session
    raise RuntimeError("Console owner setup did not redirect")


def exercise(binary, work, receiver, rules_token, episode_links, *, mismatched_secret=False, verify_destination=False):
    with port_reservation() as console_socket, port_reservation() as gateway_socket:
        console = f"127.0.0.1:{console_socket.getsockname()[1]}"
        gateway = f"127.0.0.1:{gateway_socket.getsockname()[1]}"
    base, gateway_base = f"http://{console}", f"http://{gateway}"
    receiver_base = f"http://127.0.0.1:{receiver.server_port}"
    endpoint = receiver_base + "/notifications"
    state = work / "data"
    state.mkdir()
    config = work / "config.yaml"
    rules = work / "rules-token"
    url_file = work / "notification-url"
    secret_file = work / "notification-secret"
    signing_secret = receiver.secret + b"-mismatch" if mismatched_secret else receiver.secret
    for path, value in ((rules, rules_token.encode()), (url_file, endpoint.encode()), (secret_file, signing_secret)):
        path.touch(0o600)
        path.write_bytes(value + b"\n")
    config.write_text(
        'language: en\n'
        f'listen: "{console}"\n'
        f'data_dir: {json.dumps(str(state))}\n'
        f'public_url: {json.dumps(base)}\n'
        f'rules: {{token_file: {json.dumps(str(rules))}}}\n'
        f'gateway: {{listen: "{gateway}"}}\n'
        f'victoriametrics: {{url: {json.dumps(receiver_base)}}}\n'
        'routing: {group_by: [alertname, service], group_wait: 1s}\n'
        f'notification_webhook: {{url_file: {json.dumps(str(url_file))}, secret_file: {json.dumps(str(secret_file))}}}\n',
        encoding="utf-8",
    )
    checked = subprocess.run(
        [binary, "check-config", "-config", config], capture_output=True, text=True, timeout=10,
    )
    require(checked.returncode == 0, f"Invalid test configuration: {checked.stderr.strip()}")
    require("notification webhook configured" in checked.stdout, "check-config did not report the channel")
    require(receiver.rejected_signatures.empty() and receiver.attempts.empty(), "check-config sent a notification")
    start = datetime.now(timezone.utc) - timedelta(seconds=1)

    def report(ends_at):
        post = {
            "labels": fixture_labels,
            "annotations": {"summary": "Notification smoke test"},
            "startsAt": start.isoformat(),
            "endsAt": ends_at.isoformat(),
        }
        request(
            gateway_base + "/api/v2/alerts",
            data=json.dumps([post]).encode(),
            headers={"Content-Type": "application/json", "Authorization": "Bearer " + rules_token},
        )

    log = work / "beacon.log"
    with beacon(binary, config, log, base):
        read_token = subprocess.run(
            [binary, "token", "create", "notification-smoke", "-scopes", "status:read"],
            env={**os.environ, "BEACON_DATA_DIR": str(state)},
            capture_output=True, check=True, text=True, timeout=10,
        ).stdout.strip()
        report(start + timedelta(minutes=10))
        if mismatched_secret:
            try:
                rejected = receiver.rejected_signatures.get(timeout=20)
            except Empty as err:
                raise RuntimeError("The receiver did not observe the rejected signature") from err
            require(rejected == 401, "The receiver accepted the wrong signing secret")

            def rejection_recorded():
                headers = {"Authorization": "Bearer " + read_token}
                alerts = json.loads(request(base + "/api/v1/alerts", headers=headers))["alerts"]
                for alert in alerts:
                    events = json.loads(request(base + f"/api/v1/alerts/{alert['id']}/timeline", headers=headers))["events"]
                    if any(e.get("via") == "notification_webhook" and e.get("result") == "failed" for e in events):
                        return True
                return False

            wait_for("the rejected delivery on the incident timeline", rejection_recorded)
            require(receiver.attempts.empty(), "A delivery was accepted with the wrong signing secret")
            print("A mismatched signing secret passed configuration validation, then the receiver rejected delivery with HTTP 401.", flush=True)
            return
        code, duplicate, first, first_body = attempt(receiver, "the first signed firing", 30)
        refused_at = time.monotonic()
        require(code == 503 and not duplicate and first["event"] == "firing", "Expected the first firing to be refused")
        report(start)

        def resolved_in_beacon():
            body = request(
                base + "/api/v1/alerts?state=all",
                headers={"Authorization": "Bearer " + read_token},
            )
            return any(
                a["id"] == first["alert"]["id"] and a["status"] == "resolved"
                for a in json.loads(body)["alerts"]
            )

        wait_for("the resolved incident to be recorded before restart", resolved_in_beacon)
        print("Receiver refused a signed firing; Beacon recorded recovery before restart.", flush=True)

    with beacon(binary, config, log, base):
        code, duplicate, retry, raw = attempt(receiver, "the durable firing retry", 100)
        require(code == 204 and not duplicate and retry["event"] == "firing", "Firing must be accepted before recovery")
        require(retry["delivery"] == first["delivery"], "Retry changed the firing delivery ID")
        require(raw == first_body, "Retry changed the signed firing payload")
        elapsed = time.monotonic() - refused_at
        require(elapsed >= 55, "Failed delivery retried before the one-minute retry interval")
        print(f"Firing retry accepted after {elapsed:.1f}s with the same delivery ID and payload.", flush=True)
        code, duplicate, resolved, _ = attempt(receiver, "the recovery notification", 35)
        require(code == 204 and not duplicate and resolved["event"] == "resolved", "Expected a separate accepted recovery")
        require(resolved["delivery"] != first["delivery"], "Recovery reused the firing delivery ID")
        group = first["alert"]["id"]
        episode = first["alert"]["episode"]
        require(group and episode, "Missing incident or episode ID")
        incident_path = f"/console/alerts/{group}"
        if episode_links:
            incident_path += f"/episodes/{episode}"
        for event in (first, retry, resolved):
            incident = event["alert"]
            require(incident["id"] == group and incident["episode"] == episode, "Delivery changed incident or episode")
            require(incident["severity"] == "critical" and incident["title"] == "Notification smoke test", "Wrong incident details")
            target = urlsplit(incident["url"])
            require(
                (target.scheme, target.netloc, target.path) == ("http", console, "/console/sign-in")
                and parse_qs(target.query).get("next") == [incident_path],
                "Notification must link to the incident through sign-in",
            )
        headers = {"Content-Type": "application/json", "X-Beacon-Signature": signature(receiver.secret, raw)}
        request(endpoint, data=raw, headers=headers)
        code, duplicate, _, _ = attempt(receiver, "local duplicate replay", 5)
        require(code == 204 and duplicate and len(receiver.accepted) == 2, "Receiver processed an already accepted delivery again")
        try:
            request(endpoint, data=raw + b" ", headers=headers)
        except HTTPError as err:
            require(err.code == 401, "Receiver returned the wrong status for a modified signed body")
        else:
            raise RuntimeError("Receiver accepted a modified signed body")
        if verify_destination:
            overview_headers = {"Cookie": owner_session(binary, state, base)}

            def both_results_recorded():
                events = json.loads(request(base + f"/api/v1/alerts/{group}/timeline",
                    headers={"Authorization": "Bearer " + read_token}))["events"]
                return sum(e.get("kind") == "notification" and e.get("via") == "notification_webhook" and
                    e.get("result") == "ok" for e in events) == 2

            wait_for("both acceptance records", both_results_recorded)
    if verify_destination:
        with beacon(binary, config, log, base):
            overview = console_overview(base, overview_headers)
            require(b"Provider accepted" in overview, "Restart lost the result for the unchanged destination")
        url_file.write_text(receiver_base + "/replacement\n", encoding="utf-8")
        with beacon(binary, config, log, base):
            overview = console_overview(base, overview_headers)
            require(b"Previous configuration" in overview and b"Provider accepted" not in overview,
                "Historical acceptance verified the replacement destination")
            require("/replacement" not in receiver.paths, "Beacon contacted the new receiver without an alert")
        print("Overview retained acceptance across restart and marked it historical after the destination changed.")
    print("Signed firing and recovery passed across restart with matching incident links and idempotent acceptance.")


def main(binary):
    version = subprocess.check_output([binary, "version"], text=True, timeout=10).split()
    episode_links = version[:2] != ["beacon", "0.3.0-rc.34"]
    verify_destination = version[:2] not in (["beacon", "0.3.0-rc.34"], ["beacon", "0.3.0-rc.35"])
    with TemporaryDirectory(prefix="beacon-notifications-") as tmp, HTTPServer(("127.0.0.1", 0), Receiver) as receiver:
        work = Path(tmp)
        receiver.secret = secrets.token_hex(32).encode()
        receiver.attempts = Queue()
        receiver.rejected_signatures = Queue()
        receiver.accepted = {}
        receiver.paths = set()
        receiver.rejected_first = False
        rules_token = secrets.token_hex(32)
        thread = Thread(target=receiver.serve_forever, daemon=True)
        thread.start()
        try:
            mismatch = work / "mismatch"
            mismatch.mkdir()
            exercise(binary, mismatch, receiver, rules_token, episode_links, mismatched_secret=True)
            exercise(binary, work, receiver, rules_token, episode_links, verify_destination=verify_destination)
        except Exception:
            for log in (work / "beacon.log", work / "mismatch/beacon.log"):
                show_fixture_log(log, (rules_token, receiver.secret.decode()))
            raise
        finally:
            receiver.shutdown()
            thread.join()


def acknowledge_as_owner(binary, state, base, incident):
    cookie = owner_session(binary, state, base)
    group, episode = incident["id"], incident["episode"]
    page = request(base + f"/console/alerts/{group}/episodes/{episode}", headers={"Cookie": cookie}).decode()
    form = re.search(r'<form\b[^>]*\bid="alert-ack"[^>]*>(.*?)</form>', page, re.S)
    require(form, "The owner could not find the acknowledgement form")
    fields = dict(re.findall(r'name="(csrf|expected_ack|expected_episode|return_episode)" value="([^"]+)"', form[1]))
    require(fields.get("expected_episode") == episode and fields.get("csrf"), "The form did not name the incident")
    headers = {"Cookie": cookie, "Content-Type": "application/x-www-form-urlencoded"}
    try:
        request(base + f"/console/alerts/{group}/ack", data=urlencode(fields).encode(), headers=headers)
    except HTTPError as response:
        require(response.code == 303 and response.headers["Location"].endswith("?done=ack"),
                "Beacon did not confirm the human acknowledgement")
        return
    raise RuntimeError("The acknowledgement form did not redirect")


def exercise_pushover(binary, work, receiver, rules_token):
    with port_reservation() as console_socket, port_reservation() as gateway_socket:
        console = f"127.0.0.1:{console_socket.getsockname()[1]}"
        gateway = f"127.0.0.1:{gateway_socket.getsockname()[1]}"
    base, gateway_base = f"http://{console}", f"http://{gateway}"
    receiver_base = f"http://127.0.0.1:{receiver.server_port}"
    state = work / "data"
    state.mkdir()
    config, rules, token_file, user_file = (work / name for name in ("config.yaml", "rules", "token", "user"))
    for path, value in ((rules, rules_token), (token_file, receiver.token), (user_file, receiver.user)):
        path.touch(0o600)
        path.write_text(value + "\n", encoding="utf-8")
    config.write_text(
        'language: en\n'
        f'listen: "{console}"\n'
        f'data_dir: {json.dumps(str(state))}\n'
        f'public_url: "{base}"\n'
        f'rules: {{token_file: {json.dumps(str(rules))}}}\n'
        f'gateway: {{listen: "{gateway}"}}\n'
        'victoriametrics: {url: "http://127.0.0.1:1"}\n'
        'routing: {group_by: [alertname, service], group_wait: 1s, group_interval: 1s}\n'
        'pushover:\n'
        f'  api: "{receiver_base}"\n'
        f'  token_file: {json.dumps(str(token_file))}\n'
        f'  user_key_file: {json.dumps(str(user_file))}\n'
        '  retry: 30s\n'
        '  expire: 10m\n',
        encoding="utf-8",
    )

    def report(start, firing):
        post = {
            "labels": fixture_labels,
            "annotations": {"summary": "Phone cancellation smoke test"},
            "startsAt": start.isoformat(),
            "endsAt": (start + timedelta(minutes=10) if firing else start).isoformat(),
        }
        request(gateway_base + "/api/v2/alerts", data=json.dumps([post]).encode(),
                headers={"Content-Type": "application/json", "Authorization": "Bearer " + rules_token})

    def current():
        data = json.loads(request(base + "/api/v1/alerts?state=all", headers=headers))
        return next((a for a in data["alerts"] if a.get("service") == fixture_labels["service"]), {})

    log = work / "beacon.log"
    with beacon(binary, config, log, base):
        token = subprocess.run(
            [binary, "token", "create", "phone-smoke", "-scopes", "status:read"],
            env={**os.environ, "BEACON_DATA_DIR": str(state)},
            capture_output=True, check=True, text=True, timeout=10,
        ).stdout.strip()
        headers = {"Authorization": "Bearer " + token}
        start = datetime.now(timezone.utc) - timedelta(seconds=1)
        report(start, True)
        kind, first_receipt, accepted = attempt(receiver, "the original emergency", 25)
        require(kind == "sent" and accepted, "The receiver did not accept the original emergency")
        wait_for("the original emergency in the console", lambda: current().get("pushover_at") is not None)
        first = current()
        acknowledge_as_owner(binary, state, base, first)
        require(current().get("ack", {}).get("by") == "fixture-owner", "The named owner did not take ownership")
        require(attempt(receiver, "the refused cancellation", 25) == ("cancel", first_receipt, False),
                "The first cancellation did not address the exact original receipt")
        refused_at = time.monotonic()
        pending = current()
        require(pending.get("pushover_cancellation") == "pending" and first_receipt not in json.dumps(pending),
                "The pending cancellation was missing or exposed the receipt")
        print("Pushover refused the receipt cancellation after a named owner acknowledged.", flush=True)
        report(start, False)
        wait_for("the first episode to resolve", lambda: current().get("status") == "resolved")

    with beacon(binary, config, log, base):
        new_start = datetime.now(timezone.utc) - timedelta(seconds=1)
        report(new_start, True)
        kind, new_receipt, accepted = attempt(receiver, "the replacement emergency", 25)
        require(kind == "sent" and accepted and new_receipt != first_receipt, "Expected a separate replacement emergency")

        def replacement_saved():
            incident = current()
            return (incident.get("id") == first["id"] and incident.get("episode") != first["episode"] and
                    incident.get("status") == "firing" and incident.get("pushover_at") is not None)

        # The receiver can accept a page before Beacon finishes saving the replacement incident.
        wait_for("the replacement incident and its emergency to be saved", replacement_saved)
        next_incident = current()
        require(next_incident["id"] == first["id"] and next_incident["episode"] != first["episode"] and
                next_incident["status"] == "firing" and not next_incident.get("ack"),
                "The replacement emergency did not belong to a fresh episode of the same alert group")
        require(attempt(receiver, "the retained receipt cancellation after restart", 105) == ("cancel", first_receipt, True),
                "The retained cancellation did not address only the original receipt")
        require(time.monotonic() - refused_at >= 55, "Pushover cancellation retried before the backoff")
        replacement = current()
        require(replacement["episode"] == next_incident["episode"] and replacement["status"] == "firing" and
                not replacement.get("pushover_cancellation"),
                "Cancelling the old emergency changed the new incident")
        print("Pushover accepted the original receipt cancellation after restart while the replacement stayed firing.", flush=True)
        report(new_start, False)
        require(attempt(receiver, "the replacement receipt to be cancelled on recovery", 25) == ("cancel", new_receipt, True),
                "Resolution did not cancel the replacement incident's own receipt")
        wait_for("confirmed cancellation on the resolved incident", lambda:
                 current().get("status") == "resolved" and current().get("pushover_cancellation") == "confirmed")
        print("Rule recovery cancelled the replacement incident's separate receipt.", flush=True)


def pushover_main(binary):
    with TemporaryDirectory(prefix="beacon-phone-cancellation-") as tmp, HTTPServer(("127.0.0.1", 0), PushoverReceiver) as receiver:
        work = Path(tmp)
        receiver.token, receiver.user, rules = (secrets.token_hex(15) for _ in range(3))
        receiver.attempts = Queue()
        receiver.receipts = set()
        receiver.rejected_first_cancel = False
        thread = Thread(target=receiver.serve_forever, daemon=True)
        thread.start()
        try:
            exercise_pushover(binary, work, receiver, rules)
        except Exception:
            show_fixture_log(work / "beacon.log", (receiver.token, receiver.user, rules, *receiver.receipts))
            raise
        finally:
            receiver.shutdown()
            thread.join()


if __name__ == "__main__":
    if len(sys.argv) not in (2, 3) or (len(sys.argv) == 3 and sys.argv[2] != "--pushover-cancellation"):
        sys.exit("usage: python3 deploy/smoke-notification-webhook.py /path/to/beacon [--pushover-cancellation]")
    binary = Path(sys.argv[1]).resolve(strict=True)
    if len(sys.argv) == 3:
        pushover_main(binary)
    else:
        main(binary)
