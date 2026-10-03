# Entry points shared by humans, agents and CI. See AGENTS.md.

TF_ROOT     := deploy/aws
# Every root that is init'ed and applied on its own. The module under
# deploy/aws/modules is validated through the environments that call it.
TF_STACKS   := $(TF_ROOT)/envs/staging $(TF_ROOT)/envs/production $(TF_ROOT)/shared/github-oidc $(TF_ROOT)/shared/ecr $(TF_ROOT)/shared/gpu-budget $(TF_ROOT)/shared/ses-compute
COMPOSE_DIR := deploy/compose
BUILD       := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
IMAGE       ?= ghcr.io/felixsphere/unify-compute-api:$(BUILD)
BASE_URL    ?= http://127.0.0.1:8080

.PHONY: dev test image compose-config tf-fmt tf-validate tf-test smoke verify

dev: ## run compute-api locally on 127.0.0.1:8080
	cd compute-api && go run -ldflags "-X main.Build=$(BUILD)" ./cmd/compute-api

test:
	cd compute-api && gofmt -l . | tee /dev/stderr | test -z "$$(cat)" && go vet ./... && go test ./...

image: ## build the compute-api container
	docker build --build-arg BUILD=$(BUILD) -t $(IMAGE) compute-api

# Validates the compose files with placeholder values so a malformed file
# fails here rather than on an instance whose boot then silently does nothing.
# Every shape a host runs: production's (base file alone, which must not need
# any console or SkyPilot variable), staging's (base + console + SkyPilot), and
# production with SkyPilot once gpu_workers_enabled (base + SkyPilot). Hosts
# also add the IMDS overlay, which lives in scripts/imds-lockdown.sh.
compose-config:
	cd $(COMPOSE_DIR) && COMPOSE_FILE= COMPUTE_API_IMAGE=ci SITE_IMAGE=ci POSTGRES_PASSWORD=ci \
		docker compose -f docker-compose.yml config --quiet
	cd $(COMPOSE_DIR) && COMPOSE_FILE= COMPUTE_API_IMAGE=ci SITE_IMAGE=ci POSTGRES_PASSWORD=ci \
		CONSOLE_IMAGE=ci REDIS_PASSWORD=ci SESSION_SECRET=ci SKYPILOT_ADMIN_PASSWORD=ci \
		docker compose -f docker-compose.yml -f docker-compose.console.yml -f docker-compose.skypilot.yml config --quiet
	cd $(COMPOSE_DIR) && COMPOSE_FILE= COMPUTE_API_IMAGE=ci SITE_IMAGE=ci POSTGRES_PASSWORD=ci SKYPILOT_ADMIN_PASSWORD=ci \
		docker compose -f docker-compose.yml -f docker-compose.skypilot.yml config --quiet
	@set -e; imds=$$(mktemp); trap 'rm -f "$$imds"' EXIT; \
	sh scripts/imds-lockdown.sh print-overlay > "$$imds"; cd $(COMPOSE_DIR); \
	COMPOSE_FILE= COMPUTE_API_IMAGE=ci SITE_IMAGE=ci POSTGRES_PASSWORD=ci \
		docker compose -f docker-compose.yml -f "$$imds" config --quiet; \
	COMPOSE_FILE= COMPUTE_API_IMAGE=ci SITE_IMAGE=ci POSTGRES_PASSWORD=ci \
		CONSOLE_IMAGE=ci REDIS_PASSWORD=ci SESSION_SECRET=ci SKYPILOT_ADMIN_PASSWORD=ci \
		docker compose -f docker-compose.yml -f docker-compose.console.yml -f docker-compose.skypilot.yml -f "$$imds" config \
		| grep -q 'INSTANCE_ROLE_CREDENTIALS_FILE: /run/unify-compute/aws/credentials.json'

tf-fmt:
	terraform -chdir=$(TF_ROOT) fmt -check -diff -recursive

# No credentials, no state: type-checks each root, the module through its
# callers, and the provider schemas.
tf-validate: tf-fmt
	@set -e; for d in $(TF_STACKS); do \
		echo "== $$d"; \
		terraform -chdir=$$d init -backend=false -input=false >/dev/null; \
		terraform -chdir=$$d validate; \
	done

# Renders user-data for production's and staging's shapes with a mocked AWS
# provider (no credentials, no state) and checks the .env it writes against
# what docker-compose.yml passes to compute-api.
tf-test:
	terraform -chdir=$(TF_ROOT)/modules/compute-stack init -backend=false -input=false >/dev/null
	terraform -chdir=$(TF_ROOT)/modules/compute-stack test

smoke: ## probe a running compute-api; BASE_URL=https://staging.compute.unifyai.us for staging
	BASE_URL=$(BASE_URL) CONSOLE_URL=$(CONSOLE_URL) scripts/smoke.sh

verify: test compose-config tf-validate tf-test
