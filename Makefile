.PHONY: setup check test build migrate dev

setup:
	go mod download

# Full local verification: needs PostgreSQL 18 server binaries (run on the Mac; see docs/session-handoff.md).
check:
	go vet ./...
	CGO_ENABLED=0 sh scripts/with-test-db.sh go test ./...
	git diff --check
	@test -z "$$(gofmt -l cmd internal api migrations)"

# Without a database the integration tests skip; useful on machines without PostgreSQL.
test:
	go test ./...

build:
	CGO_ENABLED=0 go build -o bin/edu-agent-server ./cmd/server
	CGO_ENABLED=0 go build -o bin/edu-agent-migrate ./cmd/migrate
	CGO_ENABLED=0 go build -o bin/edu-agent-admin ./cmd/admin

migrate:
	go run ./cmd/migrate

dev:
	EDU_ENV=development go run ./cmd/server
