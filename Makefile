# Entry points shared by humans, agents and CI. See AGENTS.md.

TF_DIR      := deploy/aws/staging
COMPOSE_DIR := deploy/compose
BUILD       := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
IMAGE       ?= ghcr.io/felixsphere/unify-compute-api:$(BUILD)
BASE_URL    ?= http://127.0.0.1:8080

.PHONY: dev test image compose-config tf-fmt tf-validate smoke verify

dev: ## run compute-api locally on 127.0.0.1:8080
	cd compute-api && go run -ldflags "-X main.Build=$(BUILD)" ./cmd/compute-api

test:
	cd compute-api && gofmt -l . | tee /dev/stderr | test -z "$$(cat)" && go vet ./... && go test ./...

image: ## build the compute-api container
	docker build --build-arg BUILD=$(BUILD) -t $(IMAGE) compute-api

# Validates the compose file with placeholder values so a malformed file fails
# here rather than on an instance whose boot then silently does nothing.
compose-config:
	cd $(COMPOSE_DIR) && CONSOLE_IMAGE=ci COMPUTE_API_IMAGE=ci POSTGRES_PASSWORD=ci REDIS_PASSWORD=ci SESSION_SECRET=ci \
		docker compose --profile skypilot config --quiet

tf-fmt:
	terraform -chdir=$(TF_DIR) fmt -check -diff -recursive

# No credentials, no state: type-checks the configuration and provider schemas.
tf-validate: tf-fmt
	terraform -chdir=$(TF_DIR) init -backend=false -input=false >/dev/null
	terraform -chdir=$(TF_DIR) validate

smoke: ## probe a running compute-api; BASE_URL=https://staging.compute.unifyai.us for staging
	BASE_URL=$(BASE_URL) CONSOLE_URL=$(CONSOLE_URL) scripts/smoke.sh

verify: test compose-config tf-validate
