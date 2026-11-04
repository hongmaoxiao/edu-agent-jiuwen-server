#!/usr/bin/env python3
"""Verify the deployed HTTPS API. Output contains assertions, never credentials or tokens."""

import argparse
from http.cookies import SimpleCookie
import json
from pathlib import Path
import urllib.error
import urllib.request


ORIGIN = "https://agent.hrtiger.cn"
API = "https://agent-api.hrtiger.cn"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


def request(method, path, payload=None, headers=None):
    data = None if payload is None else json.dumps(payload).encode()
    request_headers = dict(headers or {})
    if payload is not None:
        request_headers["Content-Type"] = "application/json"
    req = urllib.request.Request(API + path, data=data, method=method, headers=request_headers)
    try:
        response = urllib.request.build_opener(NoRedirect).open(req, timeout=20)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.code, response.headers, response.read()


def refresh_cookie(headers):
    cookie = SimpleCookie()
    for value in headers.get_all("Set-Cookie", []):
        cookie.load(value)
    morsel = cookie.get("edu_refresh")
    if morsel is None:
        raise AssertionError("edu_refresh cookie is missing")
    if not (morsel["httponly"] and morsel["secure"] and morsel["samesite"] == "Lax"
            and morsel["path"] == "/v1/auth" and morsel["domain"] == ""):
        raise AssertionError("edu_refresh cookie attributes do not match the deployment contract")
    return "edu_refresh=" + morsel.value


def verify(auth):
    checks = {}
    status, _, body = request("GET", "/livez")
    assert status == 200 and json.loads(body) == {"status": "alive"}, "livez failed"
    checks["livez"] = '200 {"status":"alive"}; TLS chain and hostname verified'
    status, _, _ = request("GET", "/readyz")
    assert status == 200, "readyz failed"
    checks["readyz"] = 200
    preflight = {
        "Origin": ORIGIN,
        "Access-Control-Request-Method": "POST",
        "Access-Control-Request-Headers": "Authorization, X-Client-Version, Content-Type",
    }
    status, headers, _ = request("OPTIONS", "/v1/auth/login", headers=preflight)
    assert status in (200, 204), "preflight failed"
    assert headers.get("Access-Control-Allow-Origin") == ORIGIN, "origin missing"
    assert headers.get("Access-Control-Allow-Credentials") == "true", "credentials not allowed"
    allowed = {part.strip().lower() for part in headers.get("Access-Control-Allow-Headers", "").split(",")}
    assert {"authorization", "x-client-version"} <= allowed, "required request headers missing"
    checks["allowed_preflight"] = {
        "status": status,
        "origin": headers.get("Access-Control-Allow-Origin"),
        "credentials": headers.get("Access-Control-Allow-Credentials"),
        "headers": headers.get("Access-Control-Allow-Headers"),
    }
    preflight["Origin"] = "https://evil.example"
    status, headers, _ = request("OPTIONS", "/v1/auth/login", headers=preflight)
    assert headers.get("Access-Control-Allow-Origin") is None, "evil origin was allowed"
    checks["rejected_preflight"] = {"status": status, "allow_origin": None}
    status, _, body = request("GET", "/metrics")
    assert status == 404 and b"# HELP" not in body, "public metrics are exposed"
    checks["public_metrics"] = 404
    status, _, _ = request("GET", "/metrics/")
    assert status == 404, "public metrics subpath is exposed"

    if auth:
        passwords = json.loads(Path("/etc/edu-agent-server/accounts.json").read_text())
        for login in ("ops", "demo"):
            status, headers, body = request("POST", "/v1/auth/login", {
                "login": login, "password": passwords[login], "client_app": "web",
            }, {"Origin": ORIGIN})
            assert status == 200, f"{login} login failed"
            token = json.loads(body)
            assert token.get("access_token") and not token.get("refresh_token"), "web token response is invalid"
            cookie = refresh_cookie(headers)
            checks[login + "_login"] = {"status": 200, "cookie": "HttpOnly; Secure; SameSite=Lax; Path=/v1/auth; no Domain"}
            status, headers, body = request("POST", "/v1/auth/refresh", headers={"Cookie": cookie, "Origin": ORIGIN})
            assert status == 200, "cookie refresh with Origin failed"
            refreshed = json.loads(body)
            assert refreshed.get("access_token") and refreshed["access_token"] != token["access_token"], "access token was not rotated"
            new_cookie = refresh_cookie(headers)
            assert new_cookie != cookie, "refresh cookie was not rotated"
            checks[login + "_refresh"] = {"status": 200, "access_rotated": True, "cookie_rotated": True}
            status, _, _ = request("POST", "/v1/auth/refresh", headers={"Cookie": new_cookie})
            assert status == 401, "cookie refresh without Origin was accepted"
            checks[login + "_refresh_without_origin"] = 401
            status, _, _ = request("POST", "/v1/auth/logout", headers={"Cookie": new_cookie, "Origin": ORIGIN})
            assert status == 204, "verification session logout failed"
            checks[login + "_logout"] = 204
    return checks


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--public-only", action="store_true", help="Skip account checks; safe to run outside the server")
    args = parser.parse_args()
    print(json.dumps(verify(auth=not args.public_only), indent=2, ensure_ascii=False))


if __name__ == "__main__":
    main()
