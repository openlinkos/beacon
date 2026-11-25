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


def request(url, *, data=None, headers=None):
    with client.open(Request(url, data=data, headers=headers or {}), timeout=4) as r:
        return r.read()


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
            overview = request(base + "/console", headers=overview_headers)
            require(b"Provider accepted" in overview, "Restart lost the result for the unchanged destination")
        url_file.write_text(receiver_base + "/replacement\n", encoding="utf-8")
        with beacon(binary, config, log, base):
            overview = request(base + "/console", headers=overview_headers)
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
                if log.exists():
                    output = log.read_text(errors="replace").replace(rules_token, "[rules token]")
                    print(output.replace(receiver.secret.decode(), "[webhook secret]")[-6000:], file=sys.stderr)
            raise
        finally:
            receiver.shutdown()
            thread.join()


if __name__ == "__main__":
    if len(sys.argv) != 2:
        sys.exit("usage: python3 deploy/smoke-notification-webhook.py /path/to/beacon")
    main(Path(sys.argv[1]).resolve(strict=True))
