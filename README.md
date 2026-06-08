# incus-compose

Bring the familiar Docker Compose workflow to Incus containers. `incus-compose` implements the Compose specification for the Incus ecosystem, allowing you to define and run multi-container applications using the same `docker-compose.yml` files you already know.

## Why incus-compose?

[Incus](https://linuxcontainers.org/incus/) provides powerful system containers and virtual machines with superior security and isolation, but lacks the declarative multi-container orchestration that Docker Compose offers. This tool bridges that gap:

- Use existing `docker-compose.yml` files with Incus containers
- Leverage Incus's native OCI registry support for image pulling
- Run Docker/OCI images directly from registries
- Manage complex multi-container applications with familiar commands
- Benefit from Incus's resource efficiency and security model

## Quick Links

- **[Getting Started](docs/getting-started.md)** - Install and run your first compose project
- **[CLI Reference](docs/cli.md)** - Commands and options
- **[Compose Compatibility](docs/compose-compatibility.md)** - What works and what doesn't
- **[Architecture](docs/architecture.md)** - How it works under the hood
- **[Why Incus?](docs/why-incus.md)** - Benefits over Docker
- **[Contributing](CONTRIBUTING.md)** - Contributing to incus-compose

## Features

Status: **Beta** - testing the beta release of incus-compose.

- `up`, `down`, `list` (and `ps`), `start`, `stop`, `restart`, `exec`, `config`, `logs` commands
- Compose project parsing via compose-go
- OCI image pulling from docker.io, ghcr.io, and other registries
- Bridge networks with automatic name sanitization
- Storage volumes with UID/GID shifting for proper permissions
- Bind mounts (one-way copy to container/storage volume)
- Port forwarding via proxy devices and kernel nat mode.
- Incus project isolation
- Container image building via Podman/Docker [doc](docs/build.md)
- Advanced compose features (healthchecks, resource limits, etc.)
- Automatic `compose.incus.yaml` overrides for Incus-specific settings

## Architecture

incus-compose uses a **resource-first design**, see [Architecture Documentation](docs/architecture.md) for details.

## Quick Start

### Prerequisites

Switch to a https remote (required for healthchecking).

incus-compose auto-detects the bridge healthd should use (incusbr0, then the default profile's eth0).
Use --healthd-network or x-incus-compose.healthd-network if your setup differs — see [Network Configuration](docs/healthd.md#network-configuration).

1.) Get the IP Address of your main bridge (incusbr0 or the one in the default profile).

```bash
incus network list
```

2.) Either set that IP as `$IP:8443` or listen on all interface `:8443`

```bash
incus config set core.https_address=<ip>:8443
```

3.) Generate a cert and add it to the trust store as admin cert.

```bash
# Generate and trust a certificate
incus remote generate-certificate
incus config trust add-certificate ~/.config/incus/client.crt

incus remote add local-https <ip>
# or
incus remote add local-https 127.0.0.1

# Switch to local-https as default remote
incus remote switch local-https
```

Add OCI image remotes to Incus, read [OCI Registry Cache](/oci-registry-cache) first as you wish.

```bash
incus remote add --protocol oci docker.io https://docker.io
incus remote add --protocol oci ghcr.io https://ghcr.io
incus remote add --protocol oci registry.gitlab.com https://registry.gitlab.com
```

#### For image building

You need either `podman` or `docker` configured and available in your path for image builds.

### Installation

Binary:

https://gitlab.com/r3j0/incus-compose/-/releases

Source:

```bash
# Build from source
git clone https://gitlab.com/r3j0/incus-compose
cd incus-compose
just build

# Or install directly
go install gitlab.com/r3j0/incus-compose/cmd/incus-compose@latest
```

### Usage

#### Existing compose.yaml

```yaml
services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"
    volumes:
      - web-data:/usr/share/nginx/html

volumes:
  web-data:
```

#### compose.incus.yaml override

`compose.incus.yaml` is loaded automatically when it exists next to the selected `compose.yaml`. This lets you keep an upstream or Docker-focused Compose file unchanged while adding Incus-specific settings in a separate file.

Typical uses:

- Remove Docker-only port publishing with `ports: !reset []`
- Add explicit health checks for `ic-healthd`
- Set static service IPs on Incus networks
- Pass raw Incus network or instance options via `x-incus`

Example `compose.incus.yaml`:

```yaml
services:
  web:
    ports: !reset []
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost"]
    networks:
      default:
        ipv4_address: 10.131.32.17

networks:
  default:
    x-incus:
      ipv4.nat: "true"
      ipv4.address: 10.131.32.1/24
```

The file follows normal [Compose merge rules](https://docs.docker.com/reference/compose-file/merge). For example, `!reset []` clears a list from the base file. See [Compose Compatibility](docs/compose-compatibility.md#incus-override-file) for details.

#### Run

Run the project normally; the override is applied automatically:

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

See [Getting Started](docs/getting-started.md) for detailed examples.

## Credits

This project builds on work by [@bketelsen](https://github.com/bketelsen).
Some components are adapted from [docker compose](https://github.com/docker/compose).

This project uses AI tools as development aids (drafting, iteration, reviews, tests, and documentation).
Architecture, constraints, and final code decisions are owned by the human committers.

## License

[Apache 2.0](LICENSE)
