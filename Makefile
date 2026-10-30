SHELL := /bin/bash
GO ?= $(CURDIR)/.toolchain/go/bin/go
ifeq ($(wildcard $(GO)),)
GO := go
endif
export GOCACHE := $(CURDIR)/.cache/go-build
export GOMODCACHE := $(CURDIR)/.cache/go-mod
export GOPATH := $(CURDIR)/.cache/gopath
export GOPROXY ?= https://goproxy.cn,direct

DB_URL ?= postgres://sched:sched@127.0.0.1:55432/sched?sslmode=disable
REDIS_URL ?= redis://127.0.0.1:56379/0
DATABASE_URL := $(DB_URL)
NPM ?= npm
WEB_DIR := web
NPM_CACHE := $(CURDIR)/.cache/npm

.PHONY: build test fmt vet tidy web-install web-build web-typecheck web-dev db-up db-up-local db-down db-reset redis-up migrate run-server run-scheduler run-worker integration leader-failover perf smoke

web-install:
	cd $(WEB_DIR) && NPM_CONFIG_CACHE=$(NPM_CACHE) $(NPM) ci

web-build: web-install
	cd $(WEB_DIR) && NPM_CONFIG_CACHE=$(NPM_CACHE) $(NPM) run build

web-typecheck:
	cd $(WEB_DIR) && NPM_CONFIG_CACHE=$(NPM_CACHE) $(NPM) run typecheck

web-dev:
	cd $(WEB_DIR) && NPM_CONFIG_CACHE=$(NPM_CACHE) $(NPM) run dev

build: web-build
	$(GO) build ./...

test: web-build
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

COMPOSE_LOCAL := docker compose -f docker-compose.local.yml

db-up:
	docker compose up -d db

# Fallback for a restricted machine that cannot pull postgres:15-alpine.
db-up-local:
	$(COMPOSE_LOCAL) up -d --build db

redis-up:
	docker compose up -d redis

db-down:
	docker compose down
	$(COMPOSE_LOCAL) down

db-reset:
	docker compose down -v
	docker compose up -d db

migrate:
	SCHED_DATABASE_URL='$(DB_URL)' $(GO) run ./cmd/sched migrate

run-server:
	SCHED_DATABASE_URL='$(DB_URL)' SCHED_REDIS_URL='$(REDIS_URL)' $(GO) run ./cmd/sched server

run-scheduler:
	SCHED_DATABASE_URL='$(DB_URL)' SCHED_REDIS_URL='$(REDIS_URL)' \
	SCHED_LOG_ROOT='$(CURDIR)/var/logs' $(GO) run ./cmd/sched scheduler

run-worker:
	SCHED_DATABASE_URL='$(DB_URL)' SCHED_LOG_ROOT='$(CURDIR)/var/logs' \
	SCHED_SPOOL_ROOT='$(CURDIR)/var/spool' SCHED_WORKSPACE_ROOT='$(CURDIR)/var/workspaces' \
	SCHED_GATEWAY_URL='http://127.0.0.1:8081' SCHED_WORKER_LABELS='linux,cpu=8' \
	$(GO) run ./cmd/sched worker

integration:
	./scripts/integration.sh

leader-failover:
	./scripts/leader-failover.sh

perf:
	./scripts/perf.sh

smoke: integration
