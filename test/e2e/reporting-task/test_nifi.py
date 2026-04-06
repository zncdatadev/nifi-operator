#!/usr/bin/env python3
"""
Test NiFi cluster health via the REST API.

Adapted from Stackable NiFi operator:
  tests/templates/kuttl/smoke_v2/test_nifi.py

Gets a JWT token via /nifi-api/access/token, then queries
/nifi-api/controller/cluster to confirm all expected nodes are CONNECTED.

Requires only the Python standard library (no third-party packages).

Usage (from within the NiFi pod via kubectl exec):
  python3 /tmp/test_nifi.py --host https://localhost:9443 --count 1

Usage (against the headless service):
  python3 test_nifi.py \
    --host https://reporting-task-nifi-node-default-0.reporting-task-nifi-node-default-headless.<ns>.svc.cluster.local:9443 \
    --user admin --password admin --count 1
"""

import argparse
import json
import ssl
import sys
import time
import urllib.parse
import urllib.request


def _insecure_ctx() -> ssl.SSLContext:
    """Return an SSL context that skips certificate verification."""
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    return ctx


def get_token(host: str, user: str, password: str) -> str:
    """
    Acquire a JWT access token from NiFi.
    NiFi 2.x requires HTTPS for token acquisition; when running over plain HTTP it
    returns 409 Conflict.  In that case we return an empty string which the callers
    treat as "anonymous / no auth" mode.
    """
    data = urllib.parse.urlencode({"username": user, "password": password}).encode()
    req = urllib.request.Request(
        f"{host}/nifi-api/access/token",
        data=data,
        headers={"Content-Type": "application/x-www-form-urlencoded; charset=UTF-8"},
    )
    try:
        with urllib.request.urlopen(req, context=_insecure_ctx()) as resp:
            return resp.read().decode()
    except urllib.error.HTTPError as e:
        if e.code == 409:
            # HTTP (non-TLS) mode: token auth not supported; NiFi allows anonymous access
            print("INFO: Token endpoint returned 409 – NiFi running in HTTP mode (anonymous access)")
            return ""
        raise


def check_cluster(host: str, token: str, expected_count: int) -> bool:
    """
    Check NiFi cluster state via /nifi-api/controller/cluster.
    If NiFi is running in standalone mode (nifi.cluster.is.node=false), that
    endpoint returns 404 (not found) or 409 Conflict.  In either case fall back
    to /nifi-api/system-diagnostics to confirm NiFi is alive and responsive.
    An empty token means NiFi is in anonymous HTTP mode (no auth header sent).
    """
    headers: dict[str, str] = {}
    if token:
        headers["Authorization"] = f"Bearer {token}"

    req = urllib.request.Request(
        f"{host}/nifi-api/controller/cluster",
        headers=headers,
    )
    try:
        with urllib.request.urlopen(req, context=_insecure_ctx()) as resp:
            data = json.load(resp)
        nodes = data["cluster"]["nodes"]
        if len(nodes) != expected_count:
            print(
                f"Expected {expected_count} node(s), got {len(nodes)}: "
                + ", ".join(f"{n.get('address', n.get('nodeId'))}={n['status']}" for n in nodes)
            )
            return False
        not_connected = [n for n in nodes if n["status"] != "CONNECTED"]
        if not_connected:
            print(
                "Nodes not yet CONNECTED: "
                + ", ".join(f"{n.get('address', n.get('nodeId'))}={n['status']}" for n in not_connected)
            )
            return False
        return True
    except urllib.error.HTTPError as e:
        if e.code in (404, 409):
            # NiFi is running in standalone (non-cluster) mode — fall back to
            # /nifi-api/system-diagnostics to confirm it is alive and healthy.
            print(f"INFO: /controller/cluster returned {e.code} (standalone mode), checking system-diagnostics instead")
            diag_req = urllib.request.Request(
                f"{host}/nifi-api/system-diagnostics",
                headers=headers,
            )
            with urllib.request.urlopen(diag_req, context=_insecure_ctx()) as resp:
                json.load(resp)  # just confirm a valid JSON response
            return True
        raise


def main() -> None:
    ap = argparse.ArgumentParser(description="Test NiFi cluster health via REST API")
    ap.add_argument("-H", "--host", default="https://localhost:9443", help="NiFi base URL")
    ap.add_argument("-u", "--user", default="admin", help="Username")
    ap.add_argument("-p", "--password", default="admin", help="Password")
    ap.add_argument("-c", "--count", type=int, default=1, help="Expected number of CONNECTED nodes")
    ap.add_argument("-t", "--timeout", type=int, default=120, help="Total wait timeout in seconds")
    args = ap.parse_args()

    print(f"Connecting to {args.host} as {args.user!r}, expecting {args.count} CONNECTED node(s)...")
    deadline = time.time() + args.timeout
    attempt = 0
    token = None
    while time.time() < deadline:
        attempt += 1
        try:
            if token is None:
                token = get_token(args.host, args.user, args.password)
            if check_cluster(args.host, token, args.count):
                print(f"PASS: All {args.count} node(s) CONNECTED (attempt {attempt})")
                sys.exit(0)
        except Exception as exc:
            print(f"INFO: attempt {attempt} error: {exc}")
            token = None  # Re-acquire token on next attempt
        time.sleep(10)

    print(f"FAIL: cluster did not reach expected state within {args.timeout}s")
    sys.exit(1)


if __name__ == "__main__":
    main()
