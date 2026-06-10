# many-dependencies

Testbed and example for a deep service dependency graph. All services use
`docker.io/nginxinc/nginx-unprivileged:alpine` as a stand-in image with a
`wget` healthcheck on `:8080`. Every `depends_on` uses `condition: service_healthy`.

## Usage

```bash
cp .env .env.local   # optional: override defaults
incus-compose up
```

Gateway is exposed on port `8080`.

## Configuration

Copy `.env` and adjust as needed:

| Variable                 | Default | Description                         |
| ------------------------ | ------- | ----------------------------------- |
| `NGINX_WORKER_PROCESSES` | `1`     | nginx worker count for all services |

All the other env variables are dummies.

## Dependency graph

```
gateway
  └── api, frontend
        api
          └── auth, users, products, orders, cache
                auth      --> auth-db, cache
                users     --> users-db, cache
                products  --> products-db, cache, search --> search-db
                orders    --> orders-db, users, products, queue
        notifications --> queue, users, mailer --> queue
        worker        --> queue, orders-db
```

## Networks

| Network        | Services                                                                                |
| -------------- | --------------------------------------------------------------------------------------- |
| `public`       | gateway, frontend, api                                                                  |
| `internal`     | api, auth, users, products, orders, notifications, mailer, worker, search, cache, queue |
| `auth-net`     | auth, auth-db (internal)                                                                |
| `users-net`    | users, users-db (internal)                                                              |
| `products-net` | products, products-db (internal)                                                        |
| `orders-net`   | orders, orders-db, worker (internal)                                                    |
| `search-net`   | search, search-db (internal)                                                            |
