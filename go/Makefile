.PHONY: build run clean test fmt lint

# Binary name
BINARY_NAME=minilinux-server
BUILD_DIR=build

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=gofmt

# Build flags
LDFLAGS=-ldflags "-s -w"

# Default target
all: build

# Build the binary
build:
	@echo "Building..."
	@mkdir -p $(BUILD_DIR)
	$(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/server

# Build for ARM (Raspberry Pi)
build-arm:
	@echo "Building for ARM (Raspberry Pi)..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm GOARM=7 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-arm ./cmd/server

# Build for ARM64 (Raspberry Pi 4/5 64-bit)
build-arm64:
	@echo "Building for ARM64..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=arm64 $(GOBUILD) $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME)-arm64 ./cmd/server

# Run the server
run: build
	@echo "Running..."
	./$(BUILD_DIR)/$(BINARY_NAME)

# Run with environment variables
run-dev:
	@echo "Running in development mode..."
	SERVE_DIRECTORY=/home/pi/minilinux-server-go $(GOCMD) run ./cmd/server

# Clean build artifacts
clean:
	@echo "Cleaning..."
	$(GOCLEAN)
	rm -rf $(BUILD_DIR)

# Run tests
test:
	@echo "Testing..."
	$(GOTEST) -v ./...

# Run tests with coverage
test-coverage:
	@echo "Testing with coverage..."
	$(GOTEST) -v -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html

# Format code
fmt:
	@echo "Formatting..."
	$(GOFMT) -s -w .

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	$(GOMOD) download
	$(GOMOD) tidy

# Lint code (requires golangci-lint)
lint:
	@echo "Linting..."
	golangci-lint run

# Install to system
install: build
	@echo "Installing to /usr/local/bin..."
	sudo cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/

# Create systemd service
install-service:
	@echo "Installing systemd service..."
	sudo cp minilinux-server.service /etc/systemd/system/
	sudo systemctl daemon-reload
	sudo systemctl enable minilinux-server

# Uninstall
uninstall:
	@echo "Uninstalling..."
	sudo systemctl stop minilinux-server || true
	sudo systemctl disable minilinux-server || true
	sudo rm -f /etc/systemd/system/minilinux-server.service
	sudo rm -f /usr/local/bin/$(BINARY_NAME)
	sudo systemctl daemon-reload

# Show help
help:
	@echo "Available targets:"
	@echo "  build        - Build the binary"
	@echo "  build-arm    - Build for Raspberry Pi (32-bit)"
	@echo "  build-arm64  - Build for Raspberry Pi 4/5 (64-bit)"
	@echo "  run          - Build and run"
	@echo "  run-dev      - Run in development mode"
	@echo "  clean        - Remove build artifacts"
	@echo "  test         - Run tests"
	@echo "  test-coverage- Run tests with coverage"
	@echo "  fmt          - Format code"
	@echo "  deps         - Download dependencies"
	@echo "  lint         - Lint code"
	@echo "  install      - Install binary to system"
	@echo "  install-service - Install systemd service"
	@echo "  uninstall    - Remove from system"
