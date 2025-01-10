# Rancher Upgrade Tool  

[![CI/CD](https://github.com/SupportTools/rancher-upgrade-tool/actions/workflows/pipeline.yml/badge.svg)](https://github.com/SupportTools/rancher-upgrade-tool/actions/workflows/pipeline.yml)  [![Go Report Card](https://goreportcard.com/badge/github.com/SupportTools/rancher-upgrade-tool)](https://goreportcard.com/report/github.com/SupportTools/rancher-upgrade-tool)  [![Codecov](https://codecov.io/gh/SupportTools/rancher-upgrade-tool/graph/badge.svg?token=ztpyFvZtOa)](https://codecov.io/gh/SupportTools/rancher-upgrade-tool)  [![GoDoc](https://pkg.go.dev/badge/github.com/SupportTools/rancher-upgrade-tool)](https://pkg.go.dev/github.com/SupportTools/rancher-upgrade-tool)  [![GitHub stars](https://img.shields.io/github/stars/SupportTools/rancher-upgrade-tool?style=social)](https://github.com/SupportTools/rancher-upgrade-tool/stargazers)  [![GitHub forks](https://img.shields.io/github/forks/SupportTools/rancher-upgrade-tool?style=social)](https://github.com/SupportTools/rancher-upgrade-tool/network/members)  [![License](https://img.shields.io/github/license/SupportTools/rancher-upgrade-tool)](LICENSE)  [![Docker Pulls](https://img.shields.io/docker/pulls/supporttools/rancher-upgrade-tool)](https://hub.docker.com/r/supporttools/rancher-upgrade-tool)  [![Docker Image Size](https://img.shields.io/docker/image-size/supporttools/rancher-upgrade-tool/latest)](https://hub.docker.com/r/supporttools/rancher-upgrade-tool)  

The Rancher Upgrade Tool simplifies generating an upgrade plan for Rancher and Kubernetes versions. It uses compatibility rules stored in a JSON file to ensure smooth and efficient upgrades. Visit [Rancher Tips](https://rancher.tips/) for more insights.

## Dependencies
- [Go Fiber](https://github.com/gofiber/fiber/v2): Web framework for building APIs in Go
- [Fiber Prometheus](https://github.com/ansrivas/fiberprometheus/v2): Middleware for Prometheus metrics in Fiber
- [HashiCorp Go Version](https://github.com/hashicorp/go-version): Library for version parsing and comparison

## File Structure
- `main.go`: Contains the main logic for the upgrade planner
- `data/upgrade-paths.json`: JSON file containing the upgrade paths and compatibility rules

## API Endpoints
- `/api/plan-upgrade/:platform/:rancher/:k8s`: Generates the upgrade plan for the provided Rancher and Kubernetes versions on a specific platform
- `/healthz`: Health check endpoint
- `/metrics`: Prometheus metrics endpoint

## Setup
1. Clone the repository:
   ```bash
   git clone https://github.com/SupportTools/rancher-upgrade-tool.git
   cd rancher-upgrade-tool
   ```
2. Add the `upgrade-paths.json` file in the `data/` directory with the upgrade paths and rules.
3. Install dependencies using Go modules:
   ```bash
   go mod tidy
   ```
4. Run the application:
   ```bash
   go run main.go
   ```

## Usage
- Make a GET request to `/api/plan-upgrade/:platform/:rancher/:k8s` to get the upgrade plan for the specified platform, Rancher version, and Kubernetes version.
- Access Prometheus metrics data at `/metrics`.

## Metrics
The application exposes custom metrics for monitoring and analysis:
- `requests_in_last_60_seconds`: Counts incoming requests in the last 60 seconds
- `versions_submitted_total`: Tracks the total number of Rancher and Kubernetes versions submitted
- `request_duration_seconds`: Measures the duration of each request
- `active_requests`: Tracks the number of active requests being processed

## License
This project is licensed under the Apache License 2.0. See the [LICENSE](LICENSE) file for more details.
