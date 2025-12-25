# Upcoming Work

> This roadmap represents current intentions, not commitments.
> Priorities and scope may change based on feedback and implementation findings.

## Current Capabilities

up/down/ps works without all the specials other compose solutions provide.

## Planned Improvements

### 1. Better Error Handling

- **Status:** Planned
- **Goal:** Implement custom error types and better error catching patterns
- **Details:**
  - Replace `errors.New()` with custom error types
  - Add error wrapping with context
  - Implement `errors.Is()` and `errors.As()` patterns
  - Better error recovery strategies
- **Example:**

  ```go
  type DisconnectedError struct {
      URL string
      Cause error
  }

  func (e *DisconnectedError) Error() string { ... }
  func (e *DisconnectedError) Unwrap() error { return e.Cause }
  ```

### 2. Move icclient Package

- **Status:** Planned
- **Goal:** Restructure icclient package location/organization

### 3. Remote Handling with Custom Config

- **Status:** Planned
- **Goal:** Add own remote/server configuration management
- **Config Format:**
  - TOML (preferred)
  - YAML (fallback)
- **Features:**
  - Multiple remote servers
  - Connection profiles
  - Cert management
  - Default remote selection

### 4. Worker Pool for Images and Tasks

- **Status:** Planned
- **Goal:** Implement concurrent worker pool for resource-intensive operations
- **Use Cases:**
  - Parallel image downloads/copies
  - Concurrent instance creation
  - Batch operations
- **Benefits:**
  - Faster multi-service deployments
  - Better resource utilization
  - Rate limiting/throttling control

### 5. Progress Reporting to CLI

- **Status:** Planned (depends on #5)
- **Goal:** Add real-time progress feedback for long-running operations
- **Features:**
  - Progress bars for image downloads
  - Parallel operation status
  - ETA calculations
  - Detailed operation logs

### 6. Various Output Formats

- **Status:** Planned
- **Goal:** Support multiple output formats for CLI commands
- **Formats:**
  - JSON
  - YAML
  - Table (current)
  - Custom templates?
- **Commands:** ps, config, inspect, etc.

### 7. Complete Compose Feature Parity

- **Status:** Planned
- **Goal:** Reach 50%+ feature completeness compared to Docker Compose
- **Current Focus Areas:**
  - Service lifecycle (up, down, restart)
  - Networks and volumes
  - Dependencies
- **Missing Features to Consider:**
  - Health checks
  - Resource limits (CPU, memory)
  - Build support (if applicable)
  - Secrets management
  - More volume types
  - Port publishing
  - Environment file handling
  - Service scaling
  - And more...
