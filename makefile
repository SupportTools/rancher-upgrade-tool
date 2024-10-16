# Makefile

# Variables
TAG ?= $(shell git describe --tags --always --dirty)
IMAGENAME = rancher-upgrade-tool
REPO = docker.io/supporttools
IMAGEFULLNAME = $(REPO)/$(IMAGENAME):$(TAG)
PLATFORMS = linux/amd64,linux/arm64

.PHONY: help build push buildx bump lint deps security docs test fmt tools all

help:
	@echo "Makefile commands:"
	@echo ""
	@echo "  make build      - Build the Docker image."
	@echo "  make push       - Push the Docker image to the repository."
	@echo "  make buildx     - Build and push multi-platform images using Buildx."
	@echo "  make bump       - Build and push the image."
	@echo "  make lint       - Run static analysis tools."
	@echo "  make deps       - Check and tidy dependencies."
	@echo "  make security   - Run security scans."
	@echo "  make docs       - Generate Swagger documentation."
	@echo "  make test       - Run Go tests."
	@echo "  make fmt        - Format Go code."
	@echo "  make all        - Run all checks, build, and push the image."
	@echo ""
	@echo "Variables:"
	@echo "  TAG             - Docker image tag (default: git describe)."
	@echo "  IMAGENAME       - Name of the Docker image (default: rancher-upgrade-tool)."
	@echo "  REPO            - Docker repository (default: docker.io/supporttools)."
	@echo "  PLATFORMS       - Target platforms for buildx (default: linux/amd64,linux/arm64)."

.DEFAULT_GOAL := all

tools:
	@echo "Installing required tools..."
	@go install golang.org/x/lint/golint@latest
	@go install honnef.co/go/tools/cmd/staticcheck@latest
	@go install github.com/securego/gosec/v2/cmd/gosec@latest
	@go install github.com/swaggo/swag/cmd/swag@latest

lint: tools
	@echo "Running Go static analysis..."
	golint ./...
	staticcheck ./...
	go vet ./...

deps:
	@echo "Checking dependencies..."
	go mod vendor
	go mod verify
	go mod tidy

security: tools
	@echo "Running security scanning..."
	gosec ./...

test:
	@echo "Running Go tests..."
	go test -v ./...

fmt:
	@echo "Formatting Go code..."
	go fmt ./...

build: lint deps security docs test fmt
	@echo "Building Docker image $(IMAGEFULLNAME)..."
	docker build \
		--pull \
		--build-arg GIT_COMMIT=`git rev-parse HEAD` \
		--build-arg VERSION=$(TAG) \
		--build-arg BUILD_DATE=`date -u +"%Y-%m-%dT%H:%M:%SZ"` \
		-t $(IMAGEFULLNAME) .

push:
	@echo "Pushing Docker image $(IMAGEFULLNAME)..."
	docker push $(IMAGEFULLNAME)
	@echo "Tagging image as latest..."
	docker tag $(IMAGEFULLNAME) $(REPO)/$(IMAGENAME):latest
	docker push $(REPO)/$(IMAGENAME):latest

buildx: lint deps security docs test fmt
	@echo "Building and pushing multi-platform images for $(IMAGEFULLNAME)..."
	docker buildx build \
		--platform $(PLATFORMS) \
		--pull \
		--build-arg VERSION=$(TAG) \
		--build-arg GIT_COMMIT=`git rev-parse HEAD` \
		--build-arg BUILD_DATE=`date -u '+%Y-%m-%dT%H:%M:%SZ'` \
		-t $(IMAGEFULLNAME) \
		-t $(REPO)/$(IMAGENAME):latest \
		--push \
		.

bump: build push

all: build push
