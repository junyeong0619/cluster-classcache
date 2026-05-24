#!/usr/bin/env python3
"""
Primer daemon — per-node coordinator for archive distribution.

Lifecycle:
  1) Compute archive key from sha256(app_jar || agent_jar || jvm_version || arch).
  2) Check local cache.
  3) If miss: query Valkey directory for peer holding this key, HTTP GET archive.
  4) If still miss: build locally (run agent + Spring Boot + ArchiveClassesAtExit).
  5) Register self in Valkey directory.
  6) Run peer HTTP server forever so others can pull from us.
"""

import hashlib
import http.server
import json
import os
import socket
import socketserver
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from pathlib import Path

import valkey

ARCHIVE_DIR = Path(os.environ.get("ARCHIVE_DIR", "/var/lib/classcache"))
APP_JAR     = Path(os.environ["APP_JAR"])
AGENT_JAR   = Path(os.environ["AGENT_JAR"])
EXTRACT_DIR = Path(os.environ.get("EXTRACT_DIR", "/work/extracted"))
VALKEY_HOST = os.environ.get("VALKEY_HOST", "redis")
VALKEY_PORT = int(os.environ.get("VALKEY_PORT", "6379"))
PEER_PORT   = int(os.environ.get("PEER_PORT", "8088"))
NODE_NAME   = os.environ.get("NODE_NAME", socket.gethostname())
# Peer endpoint advertised in directory; in k8s this should be PodIP (passed via downward API)
# Default falls back to NODE_NAME for docker-compose compatibility.
PEER_HOST   = os.environ.get("PEER_HOST", NODE_NAME)
# Extra JVM opts for build (e.g. agent-specific config like Scouter's -Dscouter.config=...)
EXTRA_JVM_OPTS = os.environ.get("EXTRA_JVM_OPTS", "").split()

ARCHIVE_DIR.mkdir(parents=True, exist_ok=True)
V = valkey.Valkey(host=VALKEY_HOST, port=VALKEY_PORT, decode_responses=True)


def log(msg):
    print(f"[primer/{NODE_NAME}] {msg}", flush=True)


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def jvm_version():
    out = subprocess.run(
        ["java", "-version"], capture_output=True, text=True
    ).stderr.strip().split("\n")[0]
    return out


def compute_key():
    parts = [
        sha256_file(APP_JAR),
        sha256_file(AGENT_JAR),
        jvm_version(),
        os.uname().machine,
    ]
    full = hashlib.sha256("|".join(parts).encode()).hexdigest()
    return full[:16]


def archive_path(key):
    return ARCHIVE_DIR / f"{key}.jsa"


def local_exists(key):
    p = archive_path(key)
    return p.exists() and p.stat().st_size > 0


def register_self(key):
    size = archive_path(key).stat().st_size
    pipe = V.pipeline()
    pipe.hset(f"archive:{key}", mapping={
        "size": size,
        "registered_at": int(time.time()),
        "jvm": jvm_version(),
        "arch": os.uname().machine,
    })
    pipe.sadd(f"archive:{key}:peers", f"{PEER_HOST}:{PEER_PORT}")
    pipe.execute()
    log(f"registered: peer={NODE_NAME}:{PEER_PORT} key={key}")


def list_peers(key):
    peers = V.smembers(f"archive:{key}:peers")
    self_ep = f"{PEER_HOST}:{PEER_PORT}"
    return [p for p in peers if p != self_ep]


def try_acquire_build_lock(key, ttl=600):
    """SETNX lock. Returns False if another node is already building."""
    holder = f"{PEER_HOST}:{PEER_PORT}"
    ok = V.set(f"archive:{key}:build_lock", holder, nx=True, ex=ttl)
    return bool(ok)


def wait_for_valkey():
    for _ in range(30):
        try:
            V.ping(); return
        except Exception:
            time.sleep(1)
    raise RuntimeError("valkey unreachable")


def wait_for_peer(key, timeout_sec=600):
    """Poll while the lock-holder builds; pull as soon as a new peer appears."""
    log("  another node is building — polling for peer...")
    deadline = time.time() + timeout_sec
    while time.time() < deadline:
        time.sleep(2)
        peers = list_peers(key)
        for peer in peers:
            if pull_from_peer(key, peer):
                return f"pulled-after-wait:{peer}"
    raise RuntimeError(f"timeout waiting for peer to publish key={key}")


def pull_from_peer(key, peer):
    url = f"http://{peer}/archive/{key}"
    log(f"  trying pull: {url}")
    t0 = time.time()
    try:
        urllib.request.urlretrieve(url, str(archive_path(key)))
        dt_ms = int((time.time() - t0) * 1000)
        sz = archive_path(key).stat().st_size
        log(f"  ✅ pulled {sz} bytes in {dt_ms} ms from {peer}")
        return True
    except (urllib.error.URLError, OSError) as e:
        log(f"  ❌ pull failed: {e}")
        try:
            archive_path(key).unlink(missing_ok=True)
        except Exception:
            pass
        return False


def ensure_extracted():
    """Extract the Spring Boot fat jar (one-shot)."""
    marker = EXTRACT_DIR / "app.jar"
    if marker.exists():
        return
    EXTRACT_DIR.mkdir(parents=True, exist_ok=True)
    log(f"  extracting Spring Boot jar → {EXTRACT_DIR}")
    subprocess.run(
        ["java", "-Djarmode=tools", "-jar", str(APP_JAR),
         "extract", "--destination", str(EXTRACT_DIR)],
        check=True, cwd=str(EXTRACT_DIR), stdout=subprocess.DEVNULL,
    )


def build_locally(key):
    log("  no peer has it — building locally")
    t0 = time.time()
    ensure_extracted()

    # The build classpath must match the runtime classpath, otherwise CDS rejects
    # the shared class paths. We use -Xbootclasspath/a:agent.jar at runtime, so we
    # have to set it at build time too.
    sb = subprocess.Popen([
        "java",
        f"-Xbootclasspath/a:{AGENT_JAR}",
        "-XX:+UnlockDiagnosticVMOptions",
        "-XX:+AllowArchivingWithJavaAgent",
        f"-XX:ArchiveClassesAtExit={archive_path(key)}",
        f"-javaagent:{AGENT_JAR}",
        *EXTRA_JVM_OPTS,
        "-jar", str(EXTRACT_DIR / "app.jar"),
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    for _ in range(60):
        try:
            urllib.request.urlopen("http://localhost:8080/hello", timeout=1).read()
            break
        except Exception:
            time.sleep(1)
    else:
        sb.kill()
        raise RuntimeError("Spring Boot didn't start in 60s")

    urllib.request.urlopen("http://localhost:8080/hello").read()
    urllib.request.urlopen("http://localhost:8080/work/100").read()

    sb.terminate()
    try:
        sb.wait(timeout=30)
    except subprocess.TimeoutExpired:
        sb.kill()

    dt_ms = int((time.time() - t0) * 1000)
    sz = archive_path(key).stat().st_size
    log(f"  ✅ built {sz} bytes in {dt_ms} ms")


class PeerHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if not self.path.startswith("/archive/"):
            self.send_error(404); return
        key = self.path[len("/archive/"):]
        p = archive_path(key)
        if not p.exists():
            self.send_error(404, f"archive {key} not found"); return
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(p.stat().st_size))
        self.end_headers()
        with open(p, "rb") as f:
            while True:
                chunk = f.read(65536)
                if not chunk: break
                self.wfile.write(chunk)

    def log_message(self, fmt, *args):
        log(f"peer-srv: {fmt % args}")


class ThreadedTCPServer(socketserver.ThreadingMixIn, socketserver.TCPServer):
    allow_reuse_address = True


def start_peer_server():
    srv = ThreadedTCPServer(("0.0.0.0", PEER_PORT), PeerHandler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    log(f"peer-srv listening on 0.0.0.0:{PEER_PORT}")


def main():
    log(f"starting (NODE_NAME={NODE_NAME})")
    wait_for_valkey()
    log("valkey ok")

    key = compute_key()
    log(f"archive key = {key}")

    t_total = time.time()
    method = None

    if local_exists(key):
        method = "local-hit"
        log(f"local hit: {archive_path(key)}")
    else:
        peers = list_peers(key)
        log(f"directory has {len(peers)} peer(s): {peers}")
        for peer in peers:
            if pull_from_peer(key, peer):
                method = f"pulled-from:{peer}"
                break
        if method is None:
            if try_acquire_build_lock(key):
                log("  acquired build lock — building locally")
                build_locally(key)
                method = "built-locally"
            else:
                method = wait_for_peer(key)

    register_self(key)

    elapsed_ms = int((time.time() - t_total) * 1000)
    archive_size = archive_path(key).stat().st_size
    log(f"READY method={method} elapsed_ms={elapsed_ms} archive_size={archive_size}")

    V.publish("primer-events", json.dumps({
        "node": NODE_NAME, "key": key, "method": method,
        "elapsed_ms": elapsed_ms, "archive_size": archive_size,
    }))

    start_peer_server()
    while True:
        time.sleep(60)


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        log(f"FATAL: {e}")
        sys.exit(1)
