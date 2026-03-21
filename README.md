# Tesla Charger Service

Personal Go service that wraps the Tesla Fleet API into one simple endpoint: "is my car charging?"

Built for iPhone Shortcuts — schedule a nightly check at 11 PM, get back `true` or `false`.

## Setup

You'll need:

- Tesla Fleet API credentials (`TESLA_CLIENT_ID`, `TESLA_CLIENT_SECRET`) from the [Tesla Developer Portal](https://developer.tesla.com/)
- Your vehicle VIN
- Go 1.22+ / Python 3.9+, or Docker
- [Cherri](https://cherrilang.org/) compiler (only to rebuild the shortcut): `go install github.com/electrikmilk/cherri@main`

```bash
# generate encryption key
make key-scripts

# configure
cp .env.example .env
# fill in your values

# run
go run ./cmd/server        # or: docker compose up --build
```

Server starts on `http://localhost:5000`.

## Environment variables

Set in `.env`:


| Variable                | Required | Note                                                            |
| ----------------------- | -------- | --------------------------------------------------------------- |
| `TESLA_CLIENT_ID`       | yes      |                                                                 |
| `TESLA_CLIENT_SECRET`   | yes      |                                                                 |
| `APP_BASE_URL`          | yes      | e.g. `https://your-domain.com`                                  |
| `TESLA_VIN`             | yes      |                                                                 |
| `SHORTCUT_BEARER_TOKEN` | yes      | long random string you make up                                  |
| `TESLA_BASE_URL`        | yes      | `https://fleet-api.prd.na.vn.cloud.tesla.com` for North America |
| `PORT`                  | no       | default `5000`                                                  |


## One-time setup

### 1. Fleet API partner registration

Tesla needs to verify your app before it'll respond to API calls. This is a one-time handshake.

```bash
make fleet-keygen     # generate EC key pair
# deploy so Tesla can reach /.well-known/appspecific/com.tesla.3p.public-key.pem
make fleet-register DOMAIN=your-domain.com
```

### 2. OAuth

Open `http://<your_url>/oauth/start`, sign in with Tesla, authorize. Tokens are stored encrypted in SQLite.

## Usage

```bash
curl -H "Authorization: Bearer <SHORTCUT_BEARER_TOKEN>" http://localhost:5000/v1/is-charging
# {"is_charging": true}
```

### iPhone Shortcut

The shortcut source lives in `shortcuts/charging-alarm.cherri`. To build:

```bash
make shortcut
# Built: shortcuts/Tesla Charging Check.shortcut
```

This reads `APP_BASE_URL` and `SHORTCUT_BEARER_TOKEN` from `.env` and bakes them into the compiled shortcut.

To install, AirDrop or open `shortcuts/Tesla Charging Check.shortcut` on your iPhone. Then create a daily Shortcuts automation (e.g. 11 PM) to run it.

## Endpoints


| Route                                                      | Auth         | Purpose                                   |
| ---------------------------------------------------------- | ------------ | ----------------------------------------- |
| `GET /v1/is-charging`                                      | Bearer token | Charging status                           |
| `GET /.well-known/appspecific/com.tesla.3p.public-key.pem` | none         | Fleet API public key (Tesla fetches this) |
| `GET /oauth/start`                                         | none         | Start OAuth flow                          |
| `GET /oauth/callback`                                      | none         | OAuth callback                            |
| `GET /docs/`                                               | none         | Swagger UI                                |


## Project structure

```
cmd/server/       entrypoint
httpapi/          routes + handlers
internal/tesla/   Fleet API client
internal/store/   SQLite encrypted token store
internal/crypto/  AES-GCM helpers
scripts/          key gen, validation, partner registration
shortcuts/        Cherri source + compiled Apple Shortcut
bruno/            API collection for manual testing
```

## Security

- Never commit `./secrets/` or `.env` (gitignored by default)
- The EC public key is served publicly — that's by design
- Single-user, personal use only

## Troubleshooting


| Problem                                | Fix                                                                         |
| -------------------------------------- | --------------------------------------------------------------------------- |
| `load encryption key ... no such file` | `make key-scripts`                                                          |
| `missing required env vars`            | Check `.env`                                                                |
| OAuth callback fails                   | `APP_BASE_URL/oauth/callback` must match Tesla app config exactly           |
| Always returns `false`                 | Check OAuth completed, VIN is correct, `TESLA_BASE_URL` matches your region |


