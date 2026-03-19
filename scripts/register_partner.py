#!/usr/bin/env python3
"""Register as a Tesla Fleet API partner.

1. Obtain a partner token via client_credentials grant.
2. POST /api/1/partner_accounts with the hosting domain.

Requires: TESLA_CLIENT_ID, TESLA_CLIENT_SECRET env vars (or .env file).
Optional: TESLA_BASE_URL (default https://fleet-api.prd.na.vn.cloud.tesla.com).
"""

import argparse
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request


def _load_dotenv(path: str = ".env") -> None:
    """Load key=value pairs from .env into os.environ (no overwrite)."""
    try:
        with open(path) as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                key, _, value = line.partition("=")
                key = key.strip()
                value = value.strip().strip("\"'")
                os.environ.setdefault(key, value)
    except FileNotFoundError:
        pass


def _require_env(name: str) -> str:
    val = os.environ.get(name)
    if not val:
        print(f"error: {name} is not set", file=sys.stderr)
        sys.exit(1)
    return val


def _post_json(url: str, data: dict, headers: dict | None = None) -> dict:
    body = urllib.parse.urlencode(data).encode() if not any(
        "json" in (headers or {}).get(k, "") for k in (headers or {})
    ) else json.dumps(data).encode()
    req = urllib.request.Request(url, data=body, headers=headers or {}, method="POST")
    if "Content-Type" not in (headers or {}):
        req.add_header("Content-Type", "application/x-www-form-urlencoded")
    try:
        with urllib.request.urlopen(req) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        resp_body = e.read().decode(errors="replace")
        print(f"HTTP {e.code}: {resp_body}", file=sys.stderr)
        sys.exit(1)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--domain", default="fleet.yashjani.com",
                        help="Domain hosting the public key (default: fleet.yashjani.com)")
    parser.add_argument("--dry-run", action="store_true",
                        help="Print requests without executing them")
    args = parser.parse_args()

    _load_dotenv()

    client_id = _require_env("TESLA_CLIENT_ID")
    client_secret = _require_env("TESLA_CLIENT_SECRET")
    base_url = os.environ.get(
        "TESLA_BASE_URL", "https://fleet-api.prd.na.vn.cloud.tesla.com"
    )

    # Step 1: obtain partner token
    token_url = "https://auth.tesla.com/oauth2/v3/token"
    token_data = {
        "grant_type": "client_credentials",
        "client_id": client_id,
        "client_secret": client_secret,
        "scope": "openid vehicle_device_data vehicle_cmds",
        "audience": base_url,
    }

    if args.dry_run:
        print("[dry-run] POST", token_url)
        print("[dry-run] body:", json.dumps(token_data, indent=2))
        print()
        print("[dry-run] POST", f"{base_url}/api/1/partner_accounts")
        print("[dry-run] body:", json.dumps({"domain": args.domain}, indent=2))
        return

    print(f"-> requesting partner token from {token_url} ...")
    token_resp = _post_json(token_url, token_data)
    access_token = token_resp.get("access_token")
    if not access_token:
        print(f"error: no access_token in response: {json.dumps(token_resp)}", file=sys.stderr)
        sys.exit(1)
    print("   ok, got access_token")

    # Step 2: register partner account
    register_url = f"{base_url}/api/1/partner_accounts"
    register_data = {"domain": args.domain}
    print(f"-> registering domain '{args.domain}' at {register_url} ...")
    register_resp = _post_json(register_url, register_data, headers={
        "Authorization": f"Bearer {access_token}",
        "Content-Type": "application/json",
    })
    print("   response:", json.dumps(register_resp, indent=2))


if __name__ == "__main__":
    main()
