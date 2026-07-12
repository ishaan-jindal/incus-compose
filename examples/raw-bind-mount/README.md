# raw-bind-mount

> The files for this example are on [Github](https://github.com/lxc/incus-compose/tree/main/examples/raw-bind-mount).

Bind-mounting an arbitrary host path into an Incus container via `x-incus.raw.lxc`, for when a regular Compose `volumes:` bind mount isn't expressive enough.

## The example

A single `web` (Caddy) service serves files from `/data` inside the container. `compose.incus.yaml` injects a raw LXC config line:

```
lxc.mount.entry = ${PWD}/../../ data/ none rbind,create=dir,idmap=container 0 0
```

This rbind-mounts the repo root (two directories up from this compose file) into `/data`, with `idmap=container` so ownership maps correctly for the unprivileged container. `caddy/Caddyfile` enables `file_server browse`, so the mounted directory is browsable.

## Usage

```bash
incus-compose up
```

Open http://10.135.32.17/
