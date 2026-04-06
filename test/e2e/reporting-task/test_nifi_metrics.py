#!/usr/bin/env python3
"""
Test NiFi Prometheus metrics endpoint.

Adapted from Stackable NiFi operator:
  tests/templates/kuttl/smoke_v1/test_nifi_metrics.py  (NiFi 1.x, plain HTTP port 8081)
  tests/templates/kuttl/smoke_v2/test_nifi_metrics.py  (NiFi 2.x, HTTPS /nifi-api/flow/metrics/prometheus)

NiFi 1.x:  The PrometheusReportingTask exposes metrics on HTTP port 8081.
NiFi 2.x:  Metrics are served natively at /nifi-api/flow/metrics/prometheus on the
           HTTPS port (default 9443) and require Bearer-token authentication.

Usage – NiFi 1.x (from within the NiFi pod):
  python3 /tmp/test_nifi_metrics.py --host localhost --port 8081

Usage – NiFi 2.x (from within the NiFi pod):
  python3 /tmp/test_nifi_metrics.py \
    --host localhost --port 9443 --use-https \
    --user admin --password admin
"""

import argparse
import ssl
import sys
import time
import urllib.parse
import urllib.request
from urllib.error import URLError


def _insecure_ctx() -> ssl.SSLContext:
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
    ctx.check_hostname = False
    ctx.verify_mode = ssl.CERT_NONE
    return ctx


def get_token(host_url: str, user: str, password: str) -> str:
    data = urllib.parse.urlencode({"username": user, "password": password}).encode()
    req = urllib.request.Request(
        f"{host_url}/nifi-api/access/token",
        data=data,
        headers={"Content-Type": "application/x-www-form-urlencoded; charset=UTF-8"},
    )
    with urllib.request.urlopen(req, context=_insecure_ctx()) as resp:
        return resp.read().decode()


def main() -> None:
    parser = argparse.ArgumentParser(description="Test NiFi Prometheus metrics endpoint")
    parser.add_argument("-m", "--metric", default="nifi_amount_bytes_read",
                        help="Metric name to look for (default: nifi_amount_bytes_read)")
    parser.add_argument("-H", "--host", default="localhost",
                        help="Hostname or IP (default: localhost)")
    parser.add_argument("-p", "--port", default="8081",
                        help="Metrics port (default: 8081 for NiFi 1.x, 9443 for NiFi 2.x)")
    parser.add_argument("--use-https", action="store_true",
                        help="Use HTTPS and authenticate (NiFi 2.x mode)")
    parser.add_argument("-u", "--user", default="admin",
                        help="Username for NiFi 2.x authentication (default: admin)")
    parser.add_argument("-P", "--password", default="admin",
                        help="Password for NiFi 2.x authentication (default: admin)")
    parser.add_argument("-n", "--namespace", default="default",
                        help="Kubernetes namespace (informational only)")
    parser.add_argument("-t", "--timeout", type=int, default=120,
                        help="Total timeout in seconds (default: 120)")
    args = parser.parse_args()

    if args.use_https:
        base_url = f"https://{args.host}:{args.port}"
        url = f"{base_url}/nifi-api/flow/metrics/prometheus"
        print(f"NiFi 2.x mode (HTTPS): getting token from {base_url} ...")
        token = get_token(base_url, args.user, args.password)
        headers = {"Authorization": f"Bearer {token}"}
        ctx = _insecure_ctx()
    elif int(args.port) != 8081:
        # NiFi 2.x without TLS: metrics served at /nifi-api/flow/metrics/prometheus over HTTP.
        # NiFi running in HTTP mode allows unauthenticated access to this endpoint.
        base_url = f"http://{args.host}:{args.port}"
        url = f"{base_url}/nifi-api/flow/metrics/prometheus"
        print(f"NiFi 2.x mode (HTTP): checking metrics at {url} ...")
        headers = {}
        ctx = None  # type: ignore[assignment]
    else:
        url = f"http://{args.host}:{args.port}/metrics/"
        headers = {}
        ctx = None  # type: ignore[assignment]

    print(f"Checking NiFi metrics at: {url}")
    print(f"Looking for metric: {args.metric}")

    deadline = time.time() + args.timeout
    attempt = 0
    while time.time() < deadline:
        attempt += 1
        try:
            req = urllib.request.Request(url, headers=headers)
            with urllib.request.urlopen(req, context=ctx, timeout=10) as resp:
                text = resp.read().decode("utf-8")
            if args.metric in text:
                print(f"PASS: Found metric '{args.metric}' (attempt {attempt})")
                sys.exit(0)
            print(f"INFO: Metric '{args.metric}' not yet in response (attempt {attempt}), retrying...")
        except URLError as exc:
            print(f"INFO: attempt {attempt} connection error: {exc}, retrying...")
        except Exception as exc:
            print(f"INFO: attempt {attempt} error: {exc}, retrying...")
        time.sleep(10)

    print(f"FAIL: metric '{args.metric}' not found within {args.timeout}s")
    sys.exit(1)


if __name__ == "__main__":
    main()
