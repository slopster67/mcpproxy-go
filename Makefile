.PHONY: help build frontend-build frontend-dev backend-dev clean check dev-setup

VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION) -X github.com/slopster67/mcpproxy-go/internal/httpapi.buildVersion=$(VERSION) -s -w
GO_PACKAGES := ./cmd/... ./internal/... ./web/...

help:
	@echo "build           Build the application with its embedded frontend"
	@echo "frontend-build  Build the frontend and copy it into web/frontend/dist"
	@echo "frontend-dev    Run Vite on port 3000 (backend: 127.0.0.1:8085)"
	@echo "backend-dev     Build the backend without rebuilding the frontend"
	@echo "check           Run Go vet and frontend type checks"
	@echo "dev-setup       Install frontend dependencies"
	@echo "clean           Remove build output"

build: frontend-build
	go build -ldflags "$(LDFLAGS)" -o mcpproxy ./cmd/mcpproxy

frontend-build:
	cd frontend && npm ci && npm run build
	mkdir -p web/frontend/dist
	cp -R frontend/dist/. web/frontend/dist/
	touch web/frontend/dist/.gitkeep

frontend-dev:
	cd frontend && npm run dev

backend-dev:
	go build -ldflags "$(LDFLAGS)" -o mcpproxy-dev ./cmd/mcpproxy

clean:
	rm -f mcpproxy mcpproxy-dev
	rm -rf frontend/dist web/frontend/dist
	mkdir -p web/frontend/dist
	touch web/frontend/dist/.gitkeep

check:
	go vet $(GO_PACKAGES)
	cd frontend && npm run type-check

dev-setup:
	cd frontend && npm ci
