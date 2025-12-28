# Architecture

This document explains the high-level architecture of incus-compose and how the major components fit together.

## Package Structure

```
incus-compose/
├── cmd/incus-compose/  # CLI entry point
├── client/             # Incus API wrapper with transactions
└── project/            # Compose-spec to Incus translation
```

### Package Responsibilities

**cmd/incus-compose/**
- Command-line interface and flag parsing
- Wires together client and project packages
- Handles commands: up, down, ps, etc.

**client/**
- High-level wrapper around Incus API
- Resource management (Profile, Image, Network, PoolVolume, Instance)
- Transaction support with automatic rollback
- Name sanitization for Incus compatibility

**project/**
- Loads Docker Compose files using compose-go
- Translates compose services to Incus instances
- Handles environment variables and service dependencies

## Resource Hierarchy

```
Client
  └── ClientProject (project-scoped operations)
        ├── Profile
        ├── Image
        ├── Network
        ├── PoolVolume
        └── Instance
```

All resources are created within a project context. Projects provide isolation between different compose applications.

## Two-Phase Resource Pattern

Resources follow a two-phase lifecycle:

1. **Go object created** - Resource exists in memory, tracked by client
2. **Incus resource created** - Resource exists on Incus server

This separation allows resources to be configured before creation and enables transaction tracking.

## Transaction and Rollback

Every operation tracks created resources and supports automatic rollback on errors.

### Resource Priorities

Resources are created and deleted in priority order:

```go
Project  → Profile → Image → Network → Volume → Instance → Snapshot
(256)      (512)     (512)   (512)     (1024)   (2048)     (4096)
```

Deletion happens in reverse order (instances deleted before networks, etc.) to respect dependencies.

### Error Handling

When errors occur during resource creation:

1. All errors are accumulated
2. In production mode, rollback is triggered automatically
3. Resources are deleted in reverse priority order
4. Debug mode skips rollback for manual inspection

## Name Sanitization

Incus has strict naming requirements that differ from Docker Compose:

### Projects
- No underscores or special characters
- Example: `My_Project!` → `my-project`

### Instances
- Valid DNS names (lowercase, hyphens only)
- Maximum 63 characters
- Long names are hashed for uniqueness
- Example: `web_server` → `web-server`

### Networks
- Must fit Linux interface name limits (13 characters)
- Short names use `{project}-{network}` format
- Long names use `{prefix}{hash}` format (deterministic)
- Example: `backend` → `app-backend` or `ic-a1b2c3d4e5`

## Environment Variables

incus-compose handles environment variables differently from Docker Compose for security and reproducibility:

- OS environment variables are NOT included by default
- `.env` files can use OS variables for interpolation (e.g., `HOME=${HOME}`)
- Only variables explicitly defined in `.env` files are added to the project
- Use `--os-env` flag for full Docker Compose compatibility

This prevents accidental leakage of sensitive environment variables into containers.

## Profile Handling

When Incus creates a new project, it generates an empty default profile with no devices. This causes instance launches to fail.

incus-compose automatically handles this by:

1. Checking if the profile has devices
2. If empty, copying devices from a source profile (typically the global default)
3. Updating the profile with the copied devices

Existing profiles with devices are not modified - we assume they're correctly configured.

## Volume UID/GID Shifting

Storage volumes are automatically configured with the correct UID/GID from the instance's OCI config:

1. Instance reads `oci.uid` and `oci.gid` from its image
2. Volume is configured with these values before creation
3. Files in the volume are owned by the correct user inside the container

This happens transparently when attaching volumes to instances.

## Service to Instance Translation

The project package translates Docker Compose services to Incus instances:

```go
// Load compose file
composeProject, err := project.Load(ctx, project.LoadModel(model))

// Convert service to instance
instance, err := project.ServiceToInstance(clientProject, service, image)

// Get dependency order
order, err := project.ServiceGraph(services, false) // false = start order
```

Key translations:

- `services` → instances
- `networks` → Incus bridge networks
- `volumes` → storage pool volumes or bind mounts
- `ports` → proxy devices
- `environment` → instance config

## Connection Modes

incus-compose supports two connection modes:

**1. Incus CLI config (normal usage):**
```bash
incus-compose up
```
Uses the default remote from `~/.config/incus/config.yml`

**2. Direct URL (testing/CI):**
```bash
export INCUS_COMPOSE_URL="https://192.168.1.100:8443"
export INCUS_COMPOSE_CERT="./certs/client.crt"
export INCUS_COMPOSE_KEY="./certs/client.key"
incus-compose up
```

### Remote vs Local Connections

Some features only work with local (Unix socket) connections:

- **Bind mounts**: Only supported locally (source path must be accessible to Incus server)
- **Network performance**: Local connections have lower latency

The client detects the connection type and validates operations accordingly.

## Debugging

Enable trace logging to see detailed Incus API interactions:

```bash
# Set trace level in client
c := client.New(ctx, logger,
    client.TraceLevel(slog.LevelDebug - 4),
)
```

In debug mode:
- Rollback is skipped (manual cleanup required)
- Additional fields logged for all operations
- API calls are traced with timing information

## Best Practices

1. **Always use project isolation** - Each compose application gets its own Incus project
2. **Let the client handle transactions** - Use `Ensure()` methods instead of manual Create/Get
3. **Sanitize names early** - The client handles this automatically
4. **Use `.env` files for configuration** - Avoid relying on OS environment
5. **Test with nested Incus** - Integration tests use nested Incus for isolation
6. **Enable debug mode for troubleshooting** - Prevents automatic rollback

## Related Documentation

- [Getting Started](getting-started.md) - Quick start guide
- [Compose Compatibility](compose-compatibility.md) - What's supported from Docker Compose
- [Environment Variables](environment-variables.md) - Environment variable handling details
