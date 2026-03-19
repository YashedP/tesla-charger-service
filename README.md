# Tesla Charger Status Wrapper (Go)

Personal-use Go service that wraps Tesla Fleet API and exposes one Shortcut-friendly endpoint.

- `GET /v1/is-charging` returns plain text `true` or `false`
- OAuth bootstrap endpoints:
  - `GET /oauth/start`
  - `GET /oauth/callback`

## What this project does

This service lets you call one URL (for example from iPhone Shortcuts at 11:00 PM daily) and receive:

- `true` if the car is actively charging
- `false` for not charging, disconnected, unavailable, or upstream errors

## Architecture and behavior

- Router: `chi`
- OAuth flow: Tesla Authorization Code + refresh token
- Token storage: encrypted values in SQLite
- Database path (fixed): `./data/tesla.sqlite`
- Encryption key file (fixed): `./secrets/token_enc_key.b64`
- Wrapper endpoint auth: static bearer token (`SHORTCUT_BEARER_TOKEN`)

If `./data/tesla.sqlite` does not exist, it is created automatically on startup.

## Prerequisites

1. Tesla Fleet API app credentials already created in Tesla Developer portal:
- `TESLA_CLIENT_ID`
- `TESLA_CLIENT_SECRET`
- allowed callback URL derived as `<APP_BASE_URL>/oauth/callback` (must match exactly)

2. Your vehicle VIN

3. Runtime tools:
- Option A: Go 1.22+ and Python 3.9+
- Option B: Docker + Docker Compose

## Required environment variables

Set these in `.env` (or environment):

- `TESLA_CLIENT_ID`
- `TESLA_CLIENT_SECRET`
- `APP_BASE_URL` (example: `http://localhost:5000` or `https://your-domain`)
- `TESLA_VIN`
- `SHORTCUT_BEARER_TOKEN`
- `TESLA_BASE_URL`

Optional:

- `PORT` (default `5000`)
- `TESLA_SCOPES` (default `offline_access vehicle_device_data`)

## Regional Tesla base URL

Use the Fleet API base URL for your account region in `TESLA_BASE_URL`.

Common value (North America):

- `https://fleet-api.prd.na.vn.cloud.tesla.com`

## Quick start (local Go)

1. Clone and enter repo:

```bash
git clone <YOUR_FORK_OR_REPO_URL>
cd tesla-charger-status
```

2. Generate encryption key file:

```bash
python3 scripts/gen_token_key.py
python3 scripts/validate_token_key.py
```

3. Create env file:

```bash
cp .env.example .env
```

4. Edit `.env` and set all required variables.

5. Start server:

```bash
go run ./cmd/server
```

Server default address: `http://localhost:5000`
The app automatically loads `.env` for native runs.

## OAuth bootstrap (one-time per token grant)

1. Open:

- `http://localhost:5000/oauth/start`

2. Sign in and authorize Tesla consent screen.

3. On success you should see:

- `OAuth successful. You can now call /v1/is-charging.`

This stores encrypted Tesla tokens in `./data/tesla.sqlite`.

## Test endpoint manually

```bash
  curl -sS \
  -H "Authorization: Bearer <SHORTCUT_BEARER_TOKEN>" \
  http://localhost:5000/v1/is-charging
```

Expected output is exactly one of:

- `true`
- `false`

## iPhone Shortcuts setup

1. Create a personal automation scheduled daily (for example 11:00 PM).
2. Add action: `Get Contents of URL`
- Method: `GET`
- URL: your hosted endpoint, e.g. `https://your-domain/v1/is-charging`
- Headers:
  - `Authorization` = `Bearer <SHORTCUT_BEARER_TOKEN>`
3. Read response body text and branch on `true` or `false`.

## Docker usage

1. Create key + env locally first:

```bash
python3 scripts/gen_token_key.py
cp .env.example .env
# edit .env
```

2. Run:

```bash
docker compose up --build
```

3. Service runs on:

- `http://localhost:5000`

Volumes keep state:

- `./data` -> `/app/data`
- `./secrets` -> `/app/secrets`

## Security notes

- Keep `./secrets/token_enc_key.b64` private and never commit it.
- Keep `.env` private; it includes secrets.
- `SHORTCUT_BEARER_TOKEN` should be long and random.
- This is intended for personal/single-user usage.

## Troubleshooting

1. `load encryption key ... no such file or directory`
- Run `python3 scripts/gen_token_key.py`.

2. `missing required env vars`
- Verify `.env` values and shell env loading.

3. OAuth callback errors
- Confirm `<APP_BASE_URL>/oauth/callback` exactly matches Tesla app config.
- Confirm your server is reachable at that URI.

4. Endpoint always returns `false`
- Ensure OAuth completed successfully.
- Verify VIN and `TESLA_BASE_URL` are correct for your region.
- Check server logs for Tesla API errors.

## Repo layout

- `cmd/server/main.go` entrypoint
- `httpapi` HTTP handlers/routes
- `internal/tesla` Tesla API client
- `internal/store` SQLite encrypted token store
- `internal/crypto` AES-GCM helpers
- `scripts/` key generation/validation tools

## Current limitations

- Single Tesla account + single VIN
- No multi-user support
- No webhook/telemetry cache mode
- On upstream errors/unavailable vehicle status, API returns `false` by design
