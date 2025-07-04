# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is the **Rancher Upgrade Tool** - a Go-based web service that helps users plan safe upgrade paths for Rancher and Kubernetes across multiple platforms (RKE1, RKE2, K3s, AKS, EKS, GKE).

## Build and Development Commands

### Core Development Commands
```bash
# Build Docker image
make build

# Run linting and static analysis
make lint

# Run security scanning
make security

# Run tests
make test

# Format code
make fmt

# Run all checks, build, and push
make all

# Build and push multi-platform images
make buildx
```

### Running Locally
```bash
# Install dependencies
go mod tidy

# Run the application
go run main.go

# The service will start on port 8080 (main) and 9000 (metrics)
```

## Architecture Overview

The application is structured as a single-file Go service (`main.go`) with the following key components:

1. **Web Framework**: Uses Fiber v2 for high-performance HTTP handling
2. **Version Management**: Uses HashiCorp's go-version library for semantic version parsing and comparison
3. **Metrics**: Integrated Prometheus metrics on port 9000
4. **Frontend**: Static HTML/CSS/JS served from the `/static` directory

### Core Logic Flow

1. **Upgrade Path Calculation** (`/api/plan-upgrade/:platform/:rancher/:k8s`):
   - Validates input versions
   - Loads compatibility matrix from `data/upgrade-paths.json`
   - Calculates safe upgrade paths based on platform-specific rules
   - Returns JSON response with upgrade steps

2. **Platform-Specific Rules**:
   - All platforms (RKE1/RKE2/K3s/AKS/EKS/GKE): Must upgrade Kubernetes incrementally (cannot skip minor versions)
   - Example: v1.24 → v1.25 → v1.26 (cannot skip from v1.24 to v1.26)

### Key Data Structure

The `data/upgrade-paths.json` file contains the compatibility matrix:
```json
{
  "rancher_version": {
    "platform": ["supported", "kubernetes", "versions"]
  }
}
```

## Deployment

The application is deployed using:
- **Docker**: Multi-stage build with Alpine Linux
- **Helm**: Charts in `/charts/rancher-upgrade-tool/`
- **ArgoCD**: GitOps configurations in `/argocd/` for multiple environments

## Important Considerations

1. **Version Parsing**: Always use semantic versioning (e.g., "v2.9.4" or "2.9.4")
2. **Error Handling**: The application returns appropriate HTTP status codes and error messages
3. **Metrics**: Custom Prometheus metrics track request patterns and performance
4. **Health Checks**: Available at `/healthz` endpoint