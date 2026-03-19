PYTHON ?= python3
GO ?= go
SWAG ?= swag
KEY_PATH ?= ./secrets/token_enc_key.b64

.PHONY: help key-generate key-generate-force key-validate key-scripts run docs lint fleet-keygen fleet-register

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
	$(PYTHON) scripts/register_partner.py
