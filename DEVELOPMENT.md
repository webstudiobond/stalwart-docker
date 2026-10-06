# Stalwart Docker Development Guide

## Prerequisites

Ensure the following dependencies are installed prior to local development:
- Git
- Go 1.27+
- GNU Make
- Docker and Docker Compose (required for building the minimal `FROM scratch` `server` and `cli` images)
- golangci-lint (required for strict Go code linting)
- govulncheck (required for Go vulnerability checking)
- hadolint (required for `Dockerfile` linting)
- trivy (required for container and filesystem security scanning)
- gitleaks (required for repository secret scanning)

## Repository Initialization

1. **Clone the repository:**
   ```sh
   git clone https://github.com/webstudiobond/stalwart-docker
   cd stalwart-docker
   ```

2. **Install Go and Security Tooling:**
   Install the required Go linter, vulnerability checker, and security scanners using any convenient method for your platform. These tools are utilized in our CI pipeline and are required for local code validation prior to submission:
   - **golangci-lint**: [https://golangci-lint.run/docs/welcome/install/](https://golangci-lint.run/docs/welcome/install/)
   - **govulncheck**: `go install golang.org/x/vuln/cmd/govulncheck@latest`
   - **hadolint**: [https://github.com/hadolint/hadolint#install](https://github.com/hadolint/hadolint#install)
   - **trivy**: [https://trivy.dev/docs/latest/getting-started/](https://trivy.dev/docs/latest/getting-started/)
   - **gitleaks**: [https://github.com/gitleaks/gitleaks#installing](https://github.com/gitleaks/gitleaks#installing)

3. **Repository Layout:**
   ```text
   ├── Dockerfile                     # Multi-stage scratch build (server & cli targets)
   ├── docker-compose.yaml            # Production deployment manifest
   ├── docker-compose.dev.yaml        # Local build and development manifest
   ├── Makefile                       # Verification, linting, and testing targets
   ├── .golangci.yml                  # Strict Go linter configuration
   ├── go.mod                         # Go module definition
   ├── init/                          # Static Go entrypoint (secret mediation, Valkey socket proxy & exec)
   │   ├── main.go
   │   └── main_test.go
   ├── probe/                         # Static Go HTTP liveness probe (/healthz/live)
   │   ├── main.go
   │   └── main_test.go
   ├── config/
   │   └── valkey/
   │       └── valkey.conf            # Hardened in-memory Valkey configuration
   └── examples/
       ├── .env.example               # Environment variable template
       └── email.mobileconfig.example # Apple Mail configuration profile template
   ```

## Code Validation & Testing

The project enforces strict code quality and security standards across the Go helper binaries (`init` and `probe`), `Dockerfile`, and Compose manifests. All validation steps are centralized in the [`Makefile`](Makefile).

1. **Go Formatting, Linting & Unit Tests:**
   To validate Go code formatting (`gofmt -s`), execute `golangci-lint`, verify compilation, and run race-enabled unit tests:
   ```sh
   make check
   make test
   ```

2. **Vulnerability & Secret Scanning:**
   To execute Go vulnerability checks, filesystem security scans, and repository secret detection:
   ```sh
   make vulncheck
   make security-trivy
   make secrets-scan
   ```

3. **Docker & Compose Validation:**
   To validate `Dockerfile` best practices via `hadolint`:
   ```sh
   make docker-lint
   ```
   If you have Docker Compose installed locally, you can also validate the syntax and interpolation of the production and development Compose manifests:
   ```sh
   cp examples/.env.example .env
   docker compose -f docker-compose.yaml config -q
   docker compose -f docker-compose.dev.yaml config -q
   ```

4. **Code Coverage:**
   To execute the Go test suite with the race detector enabled and generate a function-level coverage report for `init` and `probe`:
   ```sh
   make coverage
   ```
   To remove generated coverage artifacts afterward:
   ```sh
   make clean
   ```

5. **Project Verification:**
   Prior to submitting any pull request, you must execute the complete validation, testing, and security suite:
   ```sh
   make verify
   ```

## Build Workflow

The repository builds two zero-dependency static Go binaries (`init` and `probe` with `CGO_ENABLED=0`) and packages them alongside the upstream `stalwart` and `stalwart-cli` binaries into minimal `FROM scratch` container images (`server` and `cli` targets).

1. **Compile the Go binaries locally:**
   ```sh
   CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o init/init ./init
   CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o probe/probe ./probe
   ```

2. **Cross-compile the Go binaries for target container architectures (Optional):**
   You can verify cross-compilation for the supported Linux container architectures (`amd64` and `arm64`) by specifying `GOOS` and `GOARCH`:

   **Linux (AMD64):**
   ```sh
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o /dev/null ./init
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -trimpath -o /dev/null ./probe
   ```

   **Linux (ARM64):**
   ```sh
   CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -trimpath -o /dev/null ./init
   CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -trimpath -o /dev/null ./probe
   ```

3. **Build the Docker images locally:**
   The project uses a multi-stage [`Dockerfile`](Dockerfile) with two release targets: `server` (containing `stalwart`, `init`, `probe`, and CA certificates) and `cli` (containing `stalwart-cli` and CA certificates).

   Build both targets natively for your local architecture:
   ```sh
   docker build --target server -t stalwart:dev .
   docker build --target cli -t stalwart:dev-cli .
   ```

   To explicitly cross-compile for another architecture on a modern Docker Engine with BuildKit:
   ```sh
   # For ARM64 architecture:
   docker build --platform linux/arm64 --target server -t stalwart:dev .
   docker build --platform linux/arm64 --target cli -t stalwart:dev-cli .

   # For AMD64 architecture:
   docker build --platform linux/amd64 --target server -t stalwart:dev .
   docker build --platform linux/amd64 --target cli -t stalwart:dev-cli .
   ```

4. **Build and run using Docker Compose:**
   You can use [`docker-compose.dev.yaml`](docker-compose.dev.yaml) to build and test the stack locally with all production security constraints (`read_only: true`, `cap_drop: ALL`, `no-new-privileges: true`, isolated `tmpfs` UNIX domain sockets) applied:
   ```sh
   # Build the server image natively:
   docker compose -f docker-compose.dev.yaml build stalwart

   # Build the on-demand CLI image (under the tools profile):
   docker compose -f docker-compose.dev.yaml --profile tools build stalwart-cli

   # Start the development stack:
   docker compose -f docker-compose.dev.yaml up -d
   ```

   To explicitly cross-compile for another architecture using Docker Compose (do not run `up` locally if the target architecture differs from your host system):
   ```sh
   # For ARM64 architecture:
   DOCKER_DEFAULT_PLATFORM=linux/arm64 docker compose -f docker-compose.dev.yaml build stalwart
   DOCKER_DEFAULT_PLATFORM=linux/arm64 docker compose -f docker-compose.dev.yaml --profile tools build stalwart-cli

   # For AMD64 architecture:
   DOCKER_DEFAULT_PLATFORM=linux/amd64 docker compose -f docker-compose.dev.yaml build stalwart
   DOCKER_DEFAULT_PLATFORM=linux/amd64 docker compose -f docker-compose.dev.yaml --profile tools build stalwart-cli
   ```

## Makefile Targets Reference

| Target | Description |
| --- | --- |
| `make check` | Validates Go code formatting (`gofmt -s`), executes `golangci-lint`, and verifies compilation across all packages. |
| `make test` | Executes the Go unit test suite (`./init` and `./probe`) with race condition detection enabled (`-race`). |
| `make coverage` | Executes the Go unit test suite with race detection and outputs a function-level statement coverage report (`coverage.out`). |
| `make vulncheck` | Analyzes Go source code and standard library dependencies for known vulnerabilities using `govulncheck`. |
| `make docker-lint` | Lints the multi-stage `Dockerfile` via `hadolint`. |
| `make security-trivy` | Scans the repository filesystem for `CRITICAL` and `HIGH` severity vulnerabilities using `trivy`. |
| `make secrets-scan` | Scans the repository working tree for hardcoded secrets using `gitleaks`. |
| `make verify` | Sequentially executes `check`, `test`, `vulncheck`, `docker-lint`, `security-trivy`, and `secrets-scan`. **Must be executed prior to submitting a pull request.** |
| `make clean` | Removes generated test coverage artifacts (`coverage` and `coverage.out`). |
