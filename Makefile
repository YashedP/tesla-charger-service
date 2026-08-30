PYTHON ?= python3
GO ?= go
GO_IMAGE := golang:1.24-alpine
LINT_IMAGE := golangci/golangci-lint:v2.4.0
CONTAINER_ARGS := --rm -v "$(CURDIR):/src" -v tesla_go_modules:/go/pkg/mod -v tesla_go_build:/root/.cache/go-build -w /src
KEY_PATH ?= ./secrets/token_enc_key.b64
SERVICE_NAME := tesla-charger.service
SYSTEMD_UNIT := deploy/systemd/$(SERVICE_NAME)
INSTALLED_UNIT := /etc/systemd/system/$(SERVICE_NAME)

.PHONY: help setup install run run-native start stop restart status logs uninstall key-generate key-generate-force key-validate key-scripts docs lint fleet-keygen fleet-register
.PHONY: test verify

help:
	@echo "Available targets:"
	@echo "  make setup               # Validate configuration and build the container"
	@echo "  make install             # Install and enable the systemd service"
	@echo "  make run                 # Run the Compose service in the foreground"
	@echo "  make run-native          # Run the Go backend directly"
	@echo "  make start|stop|restart  # Control the installed systemd service"
	@echo "  make status              # Show systemd and Compose status"
	@echo "  make logs                # Follow service logs"
	@echo "  make uninstall           # Remove the systemd service without deleting data"
	@echo "  make key-scripts         # Generate key (if missing) and validate it"
	@echo "  make key-generate        # Generate key file at $(KEY_PATH)"
	@echo "  make key-generate-force  # Regenerate key file at $(KEY_PATH)"
	@echo "  make key-validate        # Validate key file at $(KEY_PATH)"
	@echo "  make docs                # Generate Swagger docs"
	@echo "  make lint                # Run golangci-lint"
	@echo "  make test                # Run tests in Docker"
	@echo "  make verify              # Run complete containerized verification"
	@echo "  make fleet-keygen        # Generate EC key pair for Fleet API partner registration"
	@echo "  make fleet-register      # Register as Tesla Fleet API partner"

setup:
	@command -v docker >/dev/null || { echo "docker is required"; exit 1; }
	@test -f .env || { echo "Missing .env"; exit 1; }
	@mkdir -p data secrets
	docker compose build

install: setup
	@tmp="$$(mktemp)"; \
	trap 'rm -f "$$tmp"' EXIT; \
	sed 's|@PROJECT_DIR@|$(CURDIR)|g' "$(SYSTEMD_UNIT)" > "$$tmp"; \
	sudo install -Dm644 "$$tmp" "$(INSTALLED_UNIT)"
	sudo systemctl daemon-reload
	sudo systemctl enable docker.service
	sudo systemctl enable "$(SERVICE_NAME)"
	sudo systemctl restart "$(SERVICE_NAME)"

run:
	docker compose up --build --remove-orphans

run-native:
	$(GO) run ./cmd/server

start:
	sudo systemctl start "$(SERVICE_NAME)"

stop:
	sudo systemctl stop "$(SERVICE_NAME)"

restart:
	sudo systemctl restart "$(SERVICE_NAME)"

status:
	@sudo systemctl status "$(SERVICE_NAME)" --no-pager
	@docker compose ps

logs:
	sudo journalctl -u "$(SERVICE_NAME)" -f

uninstall:
	-@sudo systemctl disable --now "$(SERVICE_NAME)"
	@sudo rm -f "$(INSTALLED_UNIT)"
	@sudo systemctl daemon-reload

key-generate:
	$(PYTHON) scripts/gen_token_key.py --path $(KEY_PATH)

key-generate-force:
	$(PYTHON) scripts/gen_token_key.py --path $(KEY_PATH) --force

key-validate:
	$(PYTHON) scripts/validate_token_key.py --path $(KEY_PATH)

key-scripts: key-generate key-validate

docs:
	docker run $(CONTAINER_ARGS) $(GO_IMAGE) go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g main.go -d cmd/server,httpapi -o docs

lint:
	docker run $(CONTAINER_ARGS) $(LINT_IMAGE) golangci-lint run ./...

test:
	docker run $(CONTAINER_ARGS) $(GO_IMAGE) go test -timeout 2m ./...

verify: test lint
	docker run $(CONTAINER_ARGS) $(GO_IMAGE) sh -c 'test -z "$$(gofmt -l cmd httpapi internal)" && go vet ./...'
	docker run $(CONTAINER_ARGS) $(LINT_IMAGE) go test -race -timeout 2m ./...
	docker run $(CONTAINER_ARGS) $(GO_IMAGE) sh -c 'sha256sum docs/docs.go docs/swagger.json docs/swagger.yaml > /tmp/swagger.sha256 && go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g main.go -d cmd/server,httpapi -o docs && sha256sum -c /tmp/swagger.sha256'
	docker compose --env-file .env.example config --no-env-resolution --quiet
	docker build -t tesla-charger-service .

fleet-keygen:
	@mkdir -p ./secrets
	openssl ecparam -name prime256v1 -genkey -noout -out ./secrets/fleet_ec_private.pem
	openssl ec -in ./secrets/fleet_ec_private.pem -pubout -out ./secrets/fleet_ec_public.pem
	chmod 600 ./secrets/fleet_ec_private.pem
	chmod 644 ./secrets/fleet_ec_public.pem

fleet-register:
ifndef DOMAIN
	$(error DOMAIN is required. Usage: make fleet-register DOMAIN=your-domain.com)
endif
	$(PYTHON) scripts/register_partner.py --domain $(DOMAIN)
