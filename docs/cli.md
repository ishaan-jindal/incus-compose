# CLI Reference

## Global Options

| Option                 | Description                                            |
| ---------------------- | ------------------------------------------------------ |
| `-f`, `--file`         | Compose files (repeatable)                             |
| `-p`, `--project-name` | Project name                                           |
| `--project-directory`  | Working directory                                      |
| `--profile`            | Compose profiles (repeatable)                          |
| `--env-file`           | Environment files (repeatable)                         |
| `-E`, `--os-env`       | Include OS env vars                                    |
| `--remote`             | Incus remote (`INCUS_REMOTE`)                          |
| `--ansi`               | Color output: never/always/auto (`INCUS_COMPOSE_ANSI`) |
| `--debug`              | Debug logging                                          |

Supports [no-color.org](https://no-color.org/) via `NO_COLOR` env var.

## up

Create and start containers.

```
incus-compose up [SERVICE...]
```

| Option       | Description                              |
| ------------ | ---------------------------------------- |
| `--recreate` | Recreate existing containers             |
| `--no-start` | Create without starting                  |
| `--timeout`  | Stop/start timeout seconds (default: 10) |
| `--scale`    | Scale service: `web=3` (repeatable)      |

## down

Stop and remove containers.

```
incus-compose down [SERVICE...]
```

| Option      | Description                        |
| ----------- | ---------------------------------- |
| `--project` | Remove entire Incus project        |
| `--timeout` | Stop timeout seconds (default: 10) |

## logs

View container output.

```
incus-compose logs [SERVICE...]
```

| Option           | Description   |
| ---------------- | ------------- |
| `-f`, `--follow` | Follow output |

## config

Validate and render compose file.

```
incus-compose config [SERVICE...]
```

| Option           | Description            |
| ---------------- | ---------------------- |
| `--format`       | yaml (default) or json |
| `-q`, `--quiet`  | Validate only          |
| `--services`     | List services          |
| `--volumes`      | List volumes           |
| `--networks`     | List networks          |
| `--images`       | List images            |
| `-o`, `--output` | Save to file           |

## list

List project resources.

```
incus-compose list [SERVICE...]
```

| Option     | Description                 |
| ---------- | --------------------------- |
| `--format` | table (default), yaml, json |
