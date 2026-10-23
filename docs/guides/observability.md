# tesla-charger-service observability deployment runbook

This PR targets `main` and contains only observability changes.
Do not change the deployed branch from an existing product feature branch to `main` as part of this migration.
If Dokploy currently deploys a feature branch, first merge or integrate the observability commit into that branch, resolve conflicts, and run its release checks before deployment.
In particular, preserve product-specific workers and routing when merging changes to server startup.

## Prerequisites and merge order

1. Merge and stage the `home_infra` observability PR first, following its `observability/README.md`.
2. Confirm `home-otel-collector` is healthy and attached to `dokploy-network`.
3. Merge this application's observability PR into `main` or integrate it into the actual deployment branch.
4. Record the merged deployment commit and the previous healthy image/deployment in Dokploy.

The app still runs with JSON stdout logs when no OTLP endpoint is configured.
No database migration or new application credential is required by this change.
The Go toolchain and affected dependencies are updated to pass the vulnerability gate.

## Dokploy configuration

Set `SERVICE_VERSION` in this application's Dokploy environment to the exact commit being deployed.
Use the existing Compose path, normally `compose.yaml`, together with `compose.observability.yaml`.
In Advanced, copy the currently displayed default Docker command and insert `-f compose.observability.yaml` immediately after its existing Compose `-f` argument and path.
Preserve the current project name, environment file arguments, build flags, and other settings.
The overlay service key must match the actual base service key; if product specialization renamed it, update the overlay before deployment and confirm Preview Compose adds no duplicate application service.
The custom command begins with `compose`, because Dokploy prepends `docker`.
For example, the resulting shape is:

```sh
compose -p YOUR_EXISTING_DOKPLOY_APP_NAME -f compose.yaml -f compose.observability.yaml up -d --build --remove-orphans
```

Do not literally use the placeholder project name.
Keep the existing Dokploy domains and port settings.
The overlay attaches the app to `dokploy-network`, sets service name `tesla-charger-service`, exports OTLP/HTTP internally, and stamps the deployed version.
Nothing publishes OTLP ports to the host.
The overlay also bounds local Docker JSON logs to three 10 MB files per container.
Review Preview Compose, then rebuild/redeploy the application.
A restart alone does not apply the changed environment, networks, or injected Traefik labels.

For a server-managed Compose deployment instead of Dokploy, run from the application's existing deployment directory, preserving its existing project name:

```sh
export SERVICE_VERSION="$(git rev-parse HEAD)"
docker compose -f compose.yaml -f compose.observability.yaml config --quiet
docker compose -f compose.yaml -f compose.observability.yaml up -d --build tesla-charger-service
```

Do not run a second unmanaged Compose deployment beside a Dokploy-managed one.

## Acceptance checks

1. Confirm the deployed version and container health in Dokploy.
2. Request `/health` and confirm successful probes do not produce request logs or server spans.
3. Perform one normal, read-only application request through the real hostname.
4. In HyperDX Logs, filter `ServiceName` to `tesla-charger-service` and open the completion log.
5. Confirm the message is readable, the request ID remains available where the app emits one, and TraceId/SpanId are native fields.
6. Open its trace and confirm the server span and any exercised database, OAuth, email, or object-storage client operations appear.
7. Return to related logs and confirm the same trace ID and deployed `service.version` in the trace's resource attributes.
8. Confirm request headers, raw query strings, SQL statements/parameters, and provider exception messages are absent from trace attributes.

Not every request calls every provider.
Provider calls not wired through the instrumented clients remain outside this initial tracing scope.
Database spans omit SQL text and driver exception details; use the correlated application error log to investigate failures.
Current tracing covers this repository's `main`; feature-branch-only worker operations need explicit spans when those branches are integrated.

## Rollback and troubleshooting

To disable export while keeping ordinary JSON logs, set `OTEL_SDK_DISABLED=true` and rebuild/redeploy.
To remove the network overlay, restore the recorded original Dokploy command and redeploy.
Rolling back the image requires no observability-specific database restore.
Keep `SERVICE_VERSION` synchronized with the actual rollback image.
If logs arrive without linked traces, check the collector's health, shared network membership, deployment version, and OTLP endpoint first.
Use context-aware logging calls such as `slog.InfoContext(ctx, ...)`; plain `slog.Info(...)` cannot recover a request's active span.
Do not enable raw header/body/SQL logging to diagnose missing traces.
For backend rollback, queue monitoring, retention, backup, and UI recovery, use the `home_infra` runbook.
