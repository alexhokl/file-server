# AGENTS.md

Go SFTP file server with an HTTP admin API. Single Go module, no submodules.

## Commands (Taskfile, `task <name>`)

- `task build` — `go build -o /dev/null`
- `task test` — `go test -v ./...` (there are currently **no test files** in the repo)
- `task lint` — `golangci-lint run`; `task sec` — `gosec ./...`
- `task run` — starts Postgres via `docker compose up db -d`, then runs the server with all `FILESERVER_*` env vars preset
- `task swagger` — `swag init` (regenerates `docs/`); `task swagger-fmt`-equivalent is `task swagger-format`
- `task image` — container build via `ko` (not a Dockerfile)

Single test/package: `go test ./api/ -run TestName`.

## Non-obvious facts

- `main` package is split across `main.go` + `config.go`. `go run .` works, but the Taskfile uses `go run main.go config.go` — plain `go run main.go` fails.
- `docs/` is **generated** by `swag init`. Do not hand-edit; change the swagger comments in `api/handlers.go` and regenerate.
- Swagger UI is mounted at `/docs/*any` (`api/router.go`), not `/swagger/...`. The `swagger-open` task's URL is stale.
- Config is Viper via `cli.ConfigureViper("", "file-server", false, "fileserver")` → env prefix `FILESERVER_`. Viper keys in code are unprefixed (`host_key`, `ssh_port`, `api_port`, `path_users_directory`, `path_database_connection_string`, `administrative_users`).
- `FILESERVER_PATH_DATABASE_CONNECTION_STRING` and `FILESERVER_HOST_KEY` are **file paths**, not values. Both live under `keys/`, which is gitignored — a fresh clone cannot run until those files exist (`task gen-key` creates the ECDSA host key via `step`).
- `docker-compose.yml` also needs `keys/database_password`, `keys/database_connection_string_docker`, and `database/extension.sql` — none are in the repo.
- Schema is GORM `AutoMigrate` in `db/helper.go`, run at startup. There are no migration files; change `db/models.go` instead.
- SSH auth is public-key only, checked against the `user_credentials` table (`getPublicKeyHandler` in `main.go`). No shell: `handler.HandleNormalSession` rejects normal sessions.
- SFTP uses the request-based server (`handler/filesystem.go`, `GetFileSystemRequestHandler`) and that is the only SFTP path. Do not switch to `sftp.NewServer`: it serves from the root of the filesystem, and `WithServerWorkingDirectory` only rebases *relative* paths, so absolute paths from the client escape the user directory. It exposes no filesystem or path hook, so it cannot be confined from the outside. An earlier handler using it was removed for this reason.
- Each SSH user is chrooted-by-convention to `<path_users_directory>/<username>`, auto-created on first session.
- `api/middleware.go` `isAdmin()` is a stub that returns `true` — the API is effectively unauthenticated. Treat as known TODO, don't assume auth exists.
- Two listeners: SSH on `FILESERVER_SSH_PORT`, HTTP API on `FILESERVER_API_PORT`, both shut down on SIGINT/SIGTERM with a 10s timeout.

## Layout

- `main.go`, `config.go` — wiring, host key, public-key auth
- `handler/` — SSH/SFTP session handlers
- `api/` — Gin router, handlers, middleware, request/response models
- `db/` — GORM models + AutoMigrate
- `docs/` — generated swagger
- `data/`, `keys/`, `benchmarks/` — gitignored local artifacts
