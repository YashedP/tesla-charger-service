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
| `PORT`                  | no       | Direct local run override only; default `5000`                  |


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

## Run on boot with systemd (Arch Linux)

The project uses systemd as the single owner of the Compose lifecycle. Install
and enable it with:

```bash
make setup
make install
```

Useful commands:

```bash
make status
make logs
make restart
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
| `GET /health`                                              | none         | Process health check                      |
| `GET /v1/is-charging`                                      | Bearer token | Charging status                           |
| `GET /.well-known/appspecific/com.tesla.3p.public-key.pem` | none         | Fleet API public key (Tesla fetches this) |
| `GET /oauth/start`                                         | none         | Start OAuth flow                          |
| `GET /oauth/callback`                                      | none         | OAuth callback                            |
| `GET /docs/`                                               | none         | Swagger UI                                |


## Dokploy deployment

Dokploy runs this service from the Compose resource at `./compose.yaml`.

Production settings:

- Service: `tesla-charger-service`
- Domain: `fleet.yashjani.com`
- App port: `80`
- Env vars: configured in Dokploy's Compose environment UI and loaded through `.env`
- Runtime state:
  - `tesla_charger_service_data` mounted at `/app/data`
  - `tesla_charger_service_secrets` mounted at `/app/secrets`

The Compose file intentionally uses `expose` instead of host `ports` so Traefik can route to the container without binding a host port. The container listens on internal HTTP port `80`; other containers can also use internal port `80` without conflict because no host port is published.

Set these values in Dokploy before deploying:

```text
TESLA_CLIENT_ID
TESLA_CLIENT_SECRET
APP_BASE_URL=https://fleet.yashjani.com
TESLA_VIN
SHORTCUT_BEARER_TOKEN
TESLA_BASE_URL
```

Do not set `PORT` in Dokploy. Compose sets `PORT=80` for the container so the service behaves like a standard HTTP container. The app still supports `PORT` for direct non-Compose local runs.

### One-time volume migration

Before deploying the Dokploy Compose version, copy the old bare-metal state into the Docker volumes:

```bash
sudo systemctl stop tesla-charger.service || true
sudo systemctl disable tesla-charger.service || true
```

```bash
find /home/yash/tesla-charger-service/data /home/yash/tesla-charger-service/secrets \
  -maxdepth 1 -type f -exec ls -lh {} +
```

```bash
docker volume create tesla_charger_service_data
docker volume create tesla_charger_service_secrets
```

```bash
docker run --rm \
  -v tesla_charger_service_data:/target \
  -v /home/yash/tesla-charger-service/data:/source:ro \
  alpine:3.20 \
  sh -c 'cp -a /source/. /target/'
```

```bash
docker run --rm \
  -v tesla_charger_service_secrets:/target \
  -v /home/yash/tesla-charger-service/secrets:/source:ro \
  alpine:3.20 \
  sh -c 'cp -a /source/. /target/'
```

```bash
docker run --rm \
  -v tesla_charger_service_data:/data \
  -v tesla_charger_service_secrets:/secrets \
  alpine:3.20 \
  sh -c 'chown -R 10001:10001 /data /secrets && chmod 700 /secrets && find /secrets -type f -exec chmod 600 {} +'
```

Verify migrated file names and sizes only:

```bash
docker run --rm \
  -v tesla_charger_service_data:/target:ro \
  alpine:3.20 \
  find /target -maxdepth 1 -type f -exec ls -lh {} +
```

```bash
docker run --rm \
  -v tesla_charger_service_secrets:/target:ro \
  alpine:3.20 \
  find /target -maxdepth 1 -type f -exec ls -lh {} +
```

## Observability

The service writes JSON logs to stdout. The `home_infra` Vector collector reads
Docker logs, parses JSON messages into structured fields, and ships them to
VictoriaLogs with Docker metadata such as Compose project, service, container,
and stream.

Every non-health HTTP request gets a `request_id`, a `request_complete` event,
and route-specific milestone events. `/v1/is-charging` logs token loading,
token refresh, Tesla charge-state checks, wake attempts, and the final charging
decision. Logs intentionally avoid bearer tokens, OAuth codes, access tokens,
refresh tokens, VIN values, request headers, and raw Tesla response bodies.

Useful VictoriaLogs queries:

```text
service:tesla-charger-service event:is_charging_complete
service:tesla-charger-service request_id:<request_id>
service:tesla-charger-service event:vehicle_wake_failed
service:tesla-charger-service result:default_charging_true
```

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
- Production `data` and `secrets` are stored in Docker named volumes
- The EC public key is served publicly — that's by design
- Single-user, personal use only

## Troubleshooting


| Problem                                | Fix                                                                         |
| -------------------------------------- | --------------------------------------------------------------------------- |
| `load encryption key ... no such file` | `make key-scripts`                                                          |
| `missing required env vars`            | Check `.env`                                                                |
| OAuth callback fails                   | `APP_BASE_URL/oauth/callback` must match Tesla app config exactly           |
| Always returns `false`                 | Check OAuth completed, VIN is correct, `TESLA_BASE_URL` matches your region |

