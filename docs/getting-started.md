# Getting Started

incus-compose lets you run your existing `compose.yaml` files directly on Incus without Docker.

## Prerequisites

- Incus 6.3+ installed and running
- Access to an Incus server (local or remote)

## Installation

```bash
# Build from source
just build

# The binary will be in ./incus-compose
```

## Quick Start

### 1. Create a compose.yaml

```yaml
services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"
    volumes:
      - ./html:/usr/share/nginx/html:ro

  app:
    image: docker.io/node:20-alpine
    working_dir: /app
    volumes:
      - ./app:/app
    command: node server.js
    depends_on:
      - web
```

### 2. Start your services

```bash
incus-compose up
```

This will:

- Create an Incus project named after your directory
- Pull images if needed
- Create networks and volumes
- Start containers in dependency order

### 3. Check status

```bash
incus-compose list
```

### 4. View logs

```bash
# View logs from all services
incus-compose logs

# Follow logs in real-time
incus-compose logs -f

# View logs from specific services
incus-compose logs web app
```

### 5. Stop and remove

```bash
# Stop and remove containers
incus-compose down

# Also remove volumes
incus-compose down --volumes

# Also remove networks
incus-compose down --volumes --networks
```

## Common Workflows

### Development with live code

```yaml
services:
  app:
    image: docker.io/python:3.11
    volumes:
      - ./src:/app
    working_dir: /app
    command: python -m http.server 8000
```

Edits to `./src` are immediately visible inside the container.

### Multi-service application

```yaml
services:
  db:
    image: docker.io/postgres:16-alpine
    environment:
      POSTGRES_PASSWORD: dev123
    volumes:
      - pgdata:/var/lib/postgresql/data

  api:
    image: docker.io/myapp/api:latest
    depends_on:
      - db
    environment:
      DATABASE_URL: postgres://postgres:dev123@db/myapp

  web:
    image: docker.io/myapp/frontend:latest
    depends_on:
      - api
    ports:
      - "3000:80"

volumes:
  pgdata:
```

Services start in order: db → api → web

### Using environment files

```env
# .env
DB_PASSWORD=secret123
API_PORT=3000
```

```yaml
services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"
```

Only variables defined in `.env` are available (not your shell environment).

## Next Steps

- [Compose Compatibility](compose-compatibility.md) - What features are supported
- [Environment Variables](environment-variables.md) - How env vars work
- [Why Incus?](why-incus.md) - Benefits over Docker
