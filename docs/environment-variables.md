# Environment Variables

incus-compose handles environment variables differently than docker-compose for security and reproducibility reasons.

## How It Works

### Default Behavior

By default, incus-compose loads environment variables from:

1. `.env` file in the compose file's directory
2. Files specified with `--env-file`

These `.env` files **can reference OS environment variables** for interpolation:

```env
# .env
DB_PASSWORD=secret123
HOME_DIR=${HOME}
CURRENT_USER=${USER}
```

Only variables explicitly defined in `.env` files are passed to your compose project. Your shell's environment (like `PATH`, `EDITOR`, etc.) is **not** automatically included.

### Why This Matters

- **Security**: Sensitive environment variables from your shell don't accidentally leak into containers
- **Reproducibility**: The same compose file behaves the same way on different machines
- **Explicitness**: You always know exactly which variables are available

## The `--os-env` / `-E` Flag

If you need full docker-compose compatibility, use the `--os-env` flag:

```bash
incus-compose --os-env up
incus-compose -E up
```

This includes all OS environment variables directly, matching docker-compose behavior.

## Examples

### Using .env files (recommended)

```env
# .env
DATABASE_URL=postgres://localhost/mydb
API_KEY=your-api-key
USER=${USER}
```

```yaml
# compose.yaml
services:
  app:
    environment:
      DATABASE_URL: ${DATABASE_URL}
      API_KEY: ${API_KEY}
      DEPLOYED_BY: ${USER}
```

```bash
incus-compose up
```

### Using --os-env for compatibility

```bash
export DATABASE_URL=postgres://localhost/mydb
incus-compose --os-env up
```

## Quick Reference

| Method     | Variables Available                         | Use Case                                    |
| ---------- | ------------------------------------------- | ------------------------------------------- |
| Default    | `.env` files only (can interpolate OS vars) | Production, CI/CD                           |
| `--os-env` | All OS environment variables                | Quick testing, docker-compose compatibility |

## Incus Connection

These environment variables configure how incus-compose connects to an Incus server. For normal use, incus-compose uses your existing Incus CLI configuration via the `--remote` flag or defaults to `local`.

### Connection Priority

incus-compose determines the connection in this order:

1. **`--remote` flag or `INCUS_REMOTE`** - Uses Incus CLI config to resolve the remote
2. **`INCUS_COMPOSE_URL`** - Direct URL connection (only when remote is `local`)

When `INCUS_REMOTE` is set to anything other than `local`, the `INCUS_COMPOSE_URL` variables are ignored and the Incus CLI configuration is used instead.

### Variables

| Variable                    | Description                                                    |
| --------------------------- | -------------------------------------------------------------- |
| `INCUS_REMOTE`              | Incus remote name from CLI config (e.g., `local`, `myserver`)  |
| `INCUS_COMPOSE_URL`         | Direct URL to Incus server (only used when remote is `local`)  |
| `INCUS_COMPOSE_CERT`        | Path to TLS client certificate (used with `INCUS_COMPOSE_URL`) |
| `INCUS_COMPOSE_KEY`         | Path to TLS client key (used with `INCUS_COMPOSE_URL`)         |
| `INCUS_COMPOSE_IMAGE_CACHE` | Incus project for image cache (default: `default`)             |

### Examples

**Using Incus CLI remotes (recommended):**

```bash
# Use a configured remote
incus-compose --remote myserver up

# Or via environment variable
export INCUS_REMOTE=myserver
incus-compose up
```

**Using direct URL (for testing/nested Incus):**

```bash
export INCUS_COMPOSE_URL="https://192.168.1.100:8443"
export INCUS_COMPOSE_CERT="./certs/client.crt"
export INCUS_COMPOSE_KEY="./certs/client.key"
incus-compose up
```

**Note:** The `INCUS_COMPOSE_URL` method is mainly intended for development, testing, or nested Incus scenarios where you need to bypass the CLI configuration.

See [CLI Reference](cli.md) for command options and flags.
