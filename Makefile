TEST_PG_PORT ?= 55432
TEST_DATABASE_URL ?= postgres://watchtower:watchtower@127.0.0.1:$(TEST_PG_PORT)/postgres?sslmode=disable

.PHONY: build ui test test-integration lint test-db test-db-stop

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# One binary with the web UI embedded.
build: ui
	go build -ldflags "-X main.version=$(VERSION)" -o bin/watchtower ./cmd/watchtower

ui:
	cd web && npm ci && npm run build

test:
	go test ./...

# Runs the Postgres-backed store tests as well; start a database with `make test-db`.
test-integration:
	WATCHTOWER_TEST_DATABASE_URL='$(TEST_DATABASE_URL)' go test -count=1 ./...

# The same checks CI runs. Install golangci-lint v2: https://golangci-lint.run/welcome/install/
lint:
	golangci-lint run ./...
	cd web && npx tsc -b && npm run lint

test-db:
	docker run -d --rm --name watchtower-test-pg -e POSTGRES_USER=watchtower -e POSTGRES_PASSWORD=watchtower \
		-p 127.0.0.1:$(TEST_PG_PORT):5432 postgres:18-alpine
	@until docker exec watchtower-test-pg pg_isready -U watchtower -q; do sleep 1; done

test-db-stop:
	docker stop watchtower-test-pg
