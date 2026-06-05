# docker Registry

## Prerequisites

```sh
incus remote add --protocol oci direct-docker.io https://docker.io
```

## Create and use

### With caddy

```Caddyfile
docker-registry.example.com {
	log {
		output file /var/log/caddy/docker-registry.example.com-access.log
	}

	reverse_proxy 10.132.32.17:5017
}
```

```sh
incus-compose -f docker-registry-cache/compose.yaml up
incus-compose -f docker-registry-cache/compose.yaml list

incus remote remove docker.io
incus remote add --protocol oci docker.io https://docker-registry.example.com
```

## Docs

[docker-hub](https://hub.docker.com/_/registry)
[Configuration docs](https://distribution.github.io/distribution/about/configuration/)
[docker Deployment docs](https://distribution.github.io/distribution/about/deploying/)
