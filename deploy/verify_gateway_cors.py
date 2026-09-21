"""Unauthenticated, non-billable HTTP checks for the LinAPI edge CORS policy."""

import argparse
import json
import urllib.error
import urllib.request


def verify(base_url):
    results = []

    def check(path, method, expected, cors, origin, requested="", request_method="POST"):
        headers = {"Origin": origin, "User-Agent": "LinAPI-CORS-Verification/1.0"}
        if method == "OPTIONS":
            headers["Access-Control-Request-Method"] = request_method
            headers["Access-Control-Request-Headers"] = requested
        req = urllib.request.Request(base_url.rstrip("/") + path, method=method, headers=headers)
        try:
            response = urllib.request.urlopen(req, timeout=20)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            actual = response.status
            allowed = response.headers.get_all("Access-Control-Allow-Origin") or []
            credentials = response.headers.get("Access-Control-Allow-Credentials")
            assert actual == expected, (path, method, actual, expected)
            assert allowed == (["*"] if cors else []), (path, allowed)
            assert credentials is None, (path, "credentials unexpectedly enabled")
            if method == "OPTIONS" and expected == 204:
                allowed_headers = {
                    item.strip().lower()
                    for item in response.headers.get("Access-Control-Allow-Headers", "").split(",")
                }
                assert set(requested.lower().split(",")) <= allowed_headers, path
                methods = response.headers.get("Access-Control-Allow-Methods", "")
                assert request_method in {item.strip() for item in methods.split(",")}, path
            results.append({"path": path, "method": method, "origin": origin,
                            "status": actual, "cors": cors})

    sdk_headers = (
        "authorization,content-type,x-api-key,anthropic-version,anthropic-beta,"
        "anthropic-dangerous-direct-browser-access,x-stainless-lang,"
        "x-stainless-package-version,x-goog-api-key,x-goog-api-client,"
        "openai-beta,x-client-request-id,idempotency-key"
    )
    for origin in ("https://example.com", "http://localhost:3000"):
        for path in ("/v1/models", "/v1/chat/completions", "/v1/responses", "/v1/messages",
                     "/v1beta/models", "/responses", "/models", "/chat/completions",
                     "/images/generations", "/backend-api/codex/responses",
                     "/antigravity/v1/messages"):
            check(path, "OPTIONS", 204, True, origin, sdk_headers)
        check("/v1/models", "GET", 401, True, origin)
        check("/api/v1/admin/accounts", "GET", 401, False, origin)
        check("/api/v1/admin/accounts", "OPTIONS", 403, False, origin, sdk_headers)
        check("/api/v1/auth/login", "OPTIONS", 403, False, origin, sdk_headers)
        check("/v1/models", "OPTIONS", 403, True, origin, sdk_headers, "TRACE")
    return results


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("base_url")
    args = parser.parse_args()
    results = verify(args.base_url)
    print(json.dumps({"base_url": args.base_url, "passed": len(results), "checks": results}, indent=2))
