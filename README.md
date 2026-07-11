# incus-compose

[![CI](https://github.com/lxc/incus-compose/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/lxc/incus-compose/actions?query=event%3Apush+branch%3Amain)
[![Go Reference](https://pkg.go.dev/badge/github.com/lxc/incus-compose.svg)](https://pkg.go.dev/github.com/lxc/incus-compose)
[![Coverage 60%](https://img.shields.io/badge/coverage-60%25-orange)](https://github.com/lxc/incus-compose/actions/workflows/test-release.yml)

Bring the familiar Docker Compose workflow to Incus containers. `incus-compose` implements the Compose specification for the Incus ecosystem, allowing you to define and run multi-container applications using the same `docker-compose.yml` files you already know.

[![asciicast](https://asciinema.org/a/1259458.svg)](https://asciinema.org/a/1259458)

## Why incus-compose?

[Incus](https://linuxcontainers.org/incus/) provides powerful system containers and virtual machines with superior security and isolation, but lacks the declarative multi-container orchestration that Docker Compose offers. This tool bridges that gap:

- Use existing `docker-compose.yml` files with Incus containers
- Leverage Incus's native OCI registry support for image pulling
- Run Docker/OCI images directly from registries
- Manage complex multi-container applications with familiar commands
- Benefit from Incus's resource efficiency and security model

## Features

Status: **Stable**.

- Familiar commands: `up`, `down`, `start`, `stop`, `restart`, `list` (and `ps`), `logs`, `exec`, `config`, plus `build`, `healthd`, `incus` (pass-through), and `self-update`
- Windows and macOS clients: No Docker Desktop, no WSL, no local Linux VM. `incus-compose` and the `incus` client are portable Go binaries — from a Windows or macOS desktop you drive a remote Incus host over HTTPS and manage OCI containers, LXC system containers, and VMs directly. See [Installing on Windows](https://docs.incus-compose.org/getting-started/windows).
- Compose project parsing via compose-go, with automatic `compose.incus.yaml` overrides and `x-incus` / `x-incus-compose` extensions for raw Incus options
- OCI image pulling from docker.io, ghcr.io, and other registries
- Two-stage image cache in a dedicated Incus project (survives `down`/`up`, avoids registry rate limits)
- Local image building via Podman/Docker [doc](https://docs.incus-compose.org/builds)
- Bridge networks with automatic name sanitization
- Static IPv4/IPv6 addresses with automatic DHCP ranges [doc](https://docs.incus-compose.org/compose-compatibility#automatic-dhcp-ranges)
- Port forwarding via proxy devices or kernel NAT mode [doc](https://docs.incus-compose.org/compose-compatibility#port-publishing)
- Storage volumes with UID/GID shifting; bind mounts (pass-through by default, optional seeding) [doc](https://docs.incus-compose.org/compose-compatibility#volume-permissions)
- Health checks, restart policies, and `depends_on: service_healthy` ordering via the `ic-healthd` sidecar [doc](https://docs.incus-compose.org/healthd)
- Service scaling with `up --scale` and orphan pruning
- Incus project isolation
- Resource limits and other advanced compose features (`shm_size`, `container_name`, etc.)
- Configuration via `INCUS_COMPOSE_*` environment variables for every flag, with a configurable parallel worker count [doc](https://docs.incus-compose.org/environment-variables)

## Quick Start

Requires `podman` or `docker` for image building and an Incus https remote (needed for healthchecking) with OCI registries added.
See [Getting Started](https://docs.incus-compose.org/getting-started) for the full setup walkthrough.

Install the latest release:

Arch users: an [incus-compose-bin](https://aur.archlinux.org/packages/incus-compose-bin) and [incus-compose-git](https://aur.archlinux.org/packages/incus-compose-git) (maintained by @neitsab and @jochumdev) are available.

```bash
curl -sSfL https://raw.githubusercontent.com/lxc/incus-compose/main/install.sh | sh -s -- -b ~/.local/bin
```

Or grab a prebuilt archive from the [Releases Page](https://github.com/lxc/incus-compose/releases).

Then point it at your existing `compose.yaml`:

```bash
# Start services
incus-compose up

# View logs
incus-compose logs -f

# List running services
incus-compose list

# Stop and remove
incus-compose down
```

## Quick Links

All docs: [docs.incus-compose.org](https://docs.incus-compose.org)

- **[Getting Started](https://docs.incus-compose.org/getting-started)** - Install and run your first compose project
- **[CLI Reference](https://docs.incus-compose.org/cli-reference)** - Commands and options
- **[Compose Compatibility](https://docs.incus-compose.org/compose-compatibility)** - What works and what doesn't
- **[Architecture](https://docs.incus-compose.org/architecture)** - How it works under the hood
- **[Why Incus?](https://docs.incus-compose.org/why-incus)** - Benefits over Docker
- **[Contributing](CONTRIBUTING.md)** - Contributing to incus-compose
- **[Changelog](CHANGELOG.md)** - Changelog since 0.0.1-beta1

### Examples

Descriptions are in our [docs](https://docs.incus-compose.org/examples) while the files are in [examples](examples/).

## Support and community

The following channels are available for you to interact with the Incus community.

### Bug reports

You can file bug reports and feature requests at: [`https://github.com/lxc/incus-compose/issues/new`](https://github.com/lxc/incus-compose/issues/new)

### Community support

Community support is handled at: [`https://discuss.linuxcontainers.org`](https://discuss.linuxcontainers.org)

## Contributing

Fixes and new features are greatly appreciated. Make sure to read our [contributing guidelines](CONTRIBUTING.md) first!

## Credits

This project is inspired by [@bketelsen](https://github.com/bketelsen/incus-compose).
Some components are adapted from [docker compose](https://github.com/docker/compose).
The `install.sh` script is adapted from [golangci-lint](https://github.com/golangci/golangci-lint).

This project uses AI tools as development aids (drafting, iteration, reviews, tests, and documentation).
Architecture, constraints, and final code decisions are owned by the human committers.

Earlier development was on [Gitlab](https://gitlab.com/r3j0/incus-compose/).

## License

[Apache 2.0](LICENSE)
