PYTHON ?= python3
GO ?= go
SWAG ?= swag
KEY_PATH ?= ./secrets/token_enc_key.b64

.PHONY: help key-generate key-generate-force key-validate key-scripts run docs lint fleet-keygen fleet-register shortcut

help:
	@echo "Available targets:"
	@echo "  make key-scripts         # Generate key (if missing) and validate it"
	@echo "  make key-generate        # Generate key file at $(KEY_PATH)"
	@echo "  make key-generate-force  # Regenerate key file at $(KEY_PATH)"
	@echo "  make key-validate        # Validate key file at $(KEY_PATH)"
	@echo "  make run                 # Start Go backend server"
	@echo "  make docs                # Generate Swagger docs"
	@echo "  make lint                # Run golangci-lint"
	@echo "  make fleet-keygen        # Generate EC key pair for Fleet API partner registration"
	@echo "  make fleet-register      # Register as Tesla Fleet API partner"
	@echo "  make shortcut            # Compile Apple Shortcut from Cherri source"

key-generate:
	$(PYTHON) scripts/gen_token_key.py --path $(KEY_PATH)

key-generate-force:
	$(PYTHON) scripts/gen_token_key.py --path $(KEY_PATH) --force

key-validate:
	$(PYTHON) scripts/validate_token_key.py --path $(KEY_PATH)

key-scripts: key-generate key-validate

docs:
	$(SWAG) init -g cmd/server/main.go -o docs

lint:
	$(shell $(GO) env GOPATH)/bin/golangci-lint run ./...

run:
	$(GO) run ./cmd/server

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

shortcut: ## Compile Apple Shortcut from Cherri source
	@test -f .env || { echo "Error: .env file not found. Copy .env.example and fill in values."; exit 1; }
	@. ./.env && sed \
		-e "s|__APP_BASE_URL__|$$APP_BASE_URL|g" \
		-e "s|__SHORTCUT_BEARER_TOKEN__|$$SHORTCUT_BEARER_TOKEN|g" \
		shortcuts/charging-alarm.cherri > shortcuts/.charging-alarm.generated.cherri
	cherri shortcuts/.charging-alarm.generated.cherri
	@rm -f shortcuts/.charging-alarm.generated.cherri
	@echo "Built: shortcuts/Tesla Charging Check.shortcut"
