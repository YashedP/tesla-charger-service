# Tesla Charger Service

Personal Go service that checks a Tesla's charging state every night and sends emergency alerts through Anchor.
Dokploy keeps one Compose service running continuously; an internal worker owns the schedule.
No cron job, Anchor automation, or iPhone Shortcut is required.

## Nightly behavior

The check starts at `CHARGING_CHECK_TIME` in `CHARGING_CHECK_TIMEZONE`, defaulting to `23:00` in `America/New_York`.
The notification follows the check and may arrive later if the car needs waking.
Restart the container after changing configuration.

- `Charging` or `Complete`: record success without alerting.
- `Stopped`, `Disconnected`, or `NoPower`: send **Tesla is not charging** as an emergency notification.
- An unavailable car, failed OAuth, missing data, or an unrecognized state: retry up to three 45-second attempts, waiting 15 then 30 seconds, then send **Could not verify Tesla charging** as an emergency notification.
- Intentionally scheduled charging after the check time is not exempt.

Each local date has one persisted run per configured vehicle.
Timezone rules follow daylight saving time: skipped clock times run at the first valid minute after the gap, and repeated clock times run once at their first occurrence.
Changing the time does not repeat a run already recorded for that local date.

After a restart, unfinished work resumes only within 30 minutes of the original scheduled time.
Older checks are marked missed; older pending alerts are marked failed rather than sent the following morning.
HTTP retries use the same persisted payload and idempotency key, preventing duplicate Anchor notifications after an ambiguous response.
Transient notification failures use exponential backoff from 5 seconds to 5 minutes and honor `Retry-After` within that same window.
Permanent rejections are recorded without retrying.

Anchor acceptance is recorded separately from phone delivery.
Anchor owns delivery and acknowledgment after acceptance; this service does not poll the car again or automatically cancel an alert when charging starts later.
The direct notification requests one emergency delivery cycle with acknowledgment required, skip/snooze disabled, and a 30-minute deadline.
Anchor currently uses the persistent sound and repeats emergency notifications every 60 seconds for up to 30 minutes per cycle, stopping on acknowledgment.
If the host or Anchor is unavailable, phone delivery cannot be guaranteed; inspect missed/failed-run logs.

## Setup

You'll need:

- Tesla Fleet API credentials (`TESLA_CLIENT_ID`, `TESLA_CLIENT_SECRET`) from the [Tesla Developer Portal](https://developer.tesla.com/)
- Your vehicle VIN
- Docker with Compose for builds, tests, and deployment
- Python 3.9+ for the existing local key scripts
- An Anchor API key with only the `notifications:create` action
- A configured Anchor Pushover integration and Pushover Critical Alerts enabled on your iPhone

```bash
# generate encryption key
make key-scripts

# configure
cp .env.example .env
# fill in your values

# run
docker compose up --build
```

The Compose service listens on container port `80` for the reverse proxy.
It does not publish a host port.
A direct native development run uses port `5000` by default.

## Environment variables

Set in `.env`:


| Variable | Required | Default / purpose |
| --- | --- | --- |
| `TESLA_CLIENT_ID` | yes | Fleet API application ID |
| `TESLA_CLIENT_SECRET` | yes | Fleet API application secret |
| `APP_BASE_URL` | yes | Public URL used for the OAuth callback |
| `TESLA_VIN` | yes | Vehicle to check |
| `TESLA_BASE_URL` | yes | North America: `https://fleet-api.prd.na.vn.cloud.tesla.com` |
| `TESLA_SCOPES` | no | `offline_access vehicle_device_data vehicle_cmds` |
| `CHARGING_CHECK_TIME` | no | `23:00`, strict 24-hour `HH:MM` |
| `CHARGING_CHECK_TIMEZONE` | no | `America/New_York`, an IANA timezone name |
| `ANCHOR_BASE_URL` | yes | Anchor base URL, without credentials, query, or fragment |
| `ANCHOR_API_KEY` | yes | API key authorized for `notifications:create` |
| `PORT` | no | Native default `5000`; Compose fixes this to `80` |

Anchor requires HTTPS with normal certificate verification.
HTTP is accepted only for literal loopback IP addresses used by local tests.
The client never follows redirects or logs credentials or response bodies.
Do not put API keys in URLs or commit them to source control.

## One-time setup

### 1. Fleet API partner registration

Tesla needs to verify your app before it'll respond to API calls.
This is a one-time handshake.

```bash
make fleet-keygen     # generate EC key pair
# deploy so Tesla can reach /.well-known/appspecific/com.tesla.3p.public-key.pem
make fleet-register DOMAIN=your-domain.com
```

### 2. OAuth

Open `<APP_BASE_URL>/oauth/start`, sign in with Tesla, and authorize.
Tokens are stored encrypted in SQLite.

## Migrating from Apple Shortcuts

1. Configure the new Anchor and schedule environment variables in Dokploy before deploying this version.
2. Keep the existing data and secrets volumes; the additive SQLite migration preserves encrypted OAuth tokens.
3. Reauthorize through `/oauth/start` if the existing Tesla grant lacks `vehicle_cmds`, which is needed to wake the vehicle.
   Changing `TESLA_SCOPES` alone does not change an already-issued grant.
4. Disable the old iPhone Shortcuts automation and remove `SHORTCUT_BEARER_TOKEN` from deployment configuration.
   `GET /v1/is-charging` has been removed and returns `404`.
5. Confirm the worker's startup and next-run logs, then arrange a deliberate phone test separately from automated checks.

Keep exactly one running instance for this personal service.
The worker serializes its own checks, and SQLite suppresses completed daily runs, but this is not a distributed scheduler.
Do not overlap old and new service instances during rollout.

Enable **Critical Alerts for Emergency Priority** in Pushover and grant the iOS permission so the alarm can sound through silent mode.
Confirm its Critical Alert volume and acknowledgment behavior during the deliberate phone test.
See [Pushover Critical Alerts](https://blog.pushover.net/posts/2020/2/ios-critical-alerts) and [emergency notification behavior](https://pushover.net/api#priority).

## Run on boot with systemd (Arch Linux)

For a standalone Arch Linux deployment, use systemd as the single owner of the Compose lifecycle.
Do not also enable this unit when Dokploy owns the same deployment.
Install and enable it with:

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

## Endpoints


| Route                                                      | Auth         | Purpose                                   |
| ---------------------------------------------------------- | ------------ | ----------------------------------------- |
| `GET /health`                                              | none         | Process health check                      |
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

The Compose file intentionally uses `expose` instead of host `ports` so Traefik can route to the container without binding a host port.
The container listens on internal HTTP port `80`; other containers can also use internal port `80` without conflict because no host port is published.

Set these values in Dokploy before deploying:

```text
TESLA_CLIENT_ID
TESLA_CLIENT_SECRET
APP_BASE_URL=https://fleet.yashjani.com
TESLA_VIN
TESLA_BASE_URL
TESLA_SCOPES=offline_access vehicle_device_data vehicle_cmds
CHARGING_CHECK_TIME=23:00
CHARGING_CHECK_TIMEZONE=America/New_York
ANCHOR_BASE_URL
ANCHOR_API_KEY
```

Do not set `PORT` in Dokploy.
Compose sets `PORT=80` for the container so the service behaves like a standard HTTP container.
The app still supports `PORT` for direct non-Compose local runs.

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

The service writes JSON logs to stdout.
The `home_infra` Vector collector reads Docker logs, parses JSON messages into structured fields, and ships them to VictoriaLogs with Docker metadata such as Compose project, service, container, and stream.

Every non-health HTTP request gets a `request_id` and completion event.
Logs omit OAuth query strings, request headers, credentials, VIN values, and provider response bodies.
Background work uses `run_id`, `scheduled_at`, `timezone`, `outcome`, `reason`, `check_attempts`, and `send_attempts`.
`charging_next_run` records the next scheduled check.
`anchor_accepted` means Anchor accepted the notification, not that a person heard it.
`/health` remains a process-liveness endpoint and does not claim Tesla or Anchor availability.
Unexpected worker termination stops the process so the container restart policy can recover it.

Useful VictoriaLogs queries:

```text
service:tesla-charger-service event:charging_next_run
service:tesla-charger-service event:charging_check_result
service:tesla-charger-service event:anchor_accepted
service:tesla-charger-service state:missed
service:tesla-charger-service state:failed
service:tesla-charger-service run_id:<run_id>
```

## Verification

```bash
make verify
```

This runs containerized formatting checks, vet, tests, race detection, lint, generated Swagger consistency, Compose validation, and the production image build.
Tests use temporary SQLite databases and local fake OAuth, Tesla, and Anchor HTTP services.
They never contact Tesla, Anchor production, or Pushover, and never send phone alerts.
Use `make docs` to regenerate Swagger and `make test` or `make lint` for focused checks.

## Project structure

```
cmd/server/       entrypoint
httpapi/          routes + handlers
internal/tesla/   Fleet API client
internal/store/   encrypted tokens and durable nightly runs
internal/charging/ checker and background worker
internal/schedule/ timezone-aware daily scheduling
internal/anchor/  direct notification client
internal/crypto/  AES-GCM helpers
scripts/          key gen, validation, partner registration
bruno/            API collection for manual testing
```

## Security

- Never commit `./secrets/` or `.env` (gitignored by default)
- Production `data` and `secrets` are stored in Docker named volumes
- The EC public key is served publicly - that's by design
- Single-user, personal use only

## Troubleshooting


| Problem                                | Fix                                                                         |
| -------------------------------------- | --------------------------------------------------------------------------- |
| `load encryption key ... no such file` | `make key-scripts`                                                          |
| `missing required env vars`            | Check `.env`                                                                |
| OAuth callback fails                   | `APP_BASE_URL/oauth/callback` must match Tesla app config exactly           |
| Could not verify charging | Check OAuth grant, wake-up scope, connectivity, VIN configuration, and Fleet region |
| Anchor alert failed | Check API-key action, configured Pushover integration, and `state:failed` logs |
| No audible alert | Check Pushover Critical Alerts permission/volume and phone connectivity |

