# AGENTS.md

Go SFTP file server with an HTTP admin API. Single Go module, no submodules.

## Commands (Taskfile, `task <name>`)

- `task build` — `go build -o /dev/null`
- `task test` — `go test -v ./...`; the `api/` tests skip without a test database (see below)
- `task test-api` — starts the db container, creates `file_server_test` if missing, and runs `go test -v ./api/` against it
- `task lint` — `golangci-lint run`; `task sec` — `gosec ./...`
- `task run` — starts Postgres via `docker compose up db -d`, then `go run .` with the `FILESERVER_*` env vars preset (the SSH idle timeout and connection cap are left at their defaults)
- `task swagger` — `swag init` (regenerates `docs/`); `task swagger-fmt`-equivalent is `task swagger-format`
- `task image` — container build via `ko` (not a Dockerfile)

Single test/package: `go test ./api/ -run TestName`.

## Tests

- `config_test.go`, `limit_test.go` — configuration validation and the SSH connection cap.
- `handler/filesystem_test.go`, `handler/filesystem_sftp_test.go` — SFTP handler against a temp directory; the SFTP tests drive a real `sftp.Client` over a loopback TCP connection to the request server. No database needed.
- `api/handlers_test.go` — runs the real router against Postgres. It skips unless `FILESERVER_TEST_DATABASE_CONNECTION_STRING` is set, and refuses a connection string without `test` in it, because it **drops and re-migrates** `users` and `user_credentials`. Use `task test-api` rather than pointing it at the dev database.

## Non-obvious facts

- `main` package is split across `main.go`, `config.go` and `limit.go`. Use `go run .`; `go run main.go` (or any subset of the files) fails.
- `docs/` is **generated** by `swag init`. Do not hand-edit; change the swagger comments in `api/handlers.go` and regenerate.
- Swagger UI is mounted at `/docs/*any` (`api/router.go`), not `/swagger/...`. The `swagger-open` task's URL is stale.
- Config is Viper via `cli.ConfigureViper("", "file-server", false, "fileserver")` → env prefix `FILESERVER_`. Viper keys in code are unprefixed (`host_key`, `ssh_port`, `api_port`, `path_users_directory`, `path_database_connection_string`, `administrative_users`, `ssh_idle_timeout`, `ssh_max_connections`). Validation is in `getConfiguration()` (`config.go`).
- `ssh_idle_timeout` defaults to `5m`; a malformed value fails startup rather than silently disabling the timeout. `ssh_max_connections` defaults to `100`; `0` disables the cap.
- `FILESERVER_PATH_DATABASE_CONNECTION_STRING` and `FILESERVER_HOST_KEY` are **file paths**, not values. Both live under `keys/`, which is gitignored — a fresh clone cannot run until those files exist (`task gen-key` creates the ECDSA host key via `step`).
- `docker-compose.yml` also needs `keys/database_password`, `keys/database_connection_string_docker`, and `database/extension.sql` — none are in the repo.
- Schema is GORM `AutoMigrate` in `db/helper.go` (`db.Migrate`), run at startup. There are no migration files; change `db/models.go` instead. AutoMigrate does not create the `user_credentials.username` → `users` foreign key, so `db.Migrate` creates `fk_user_credentials_user` explicitly. It is `ON DELETE RESTRICT`: deleting a user who still has credentials returns 409.
- One `*gorm.DB` pool is opened in `main.go` (20 open/idle connections) and shared across API requests through the `withDatabaseConnection` middleware (`api/middleware.go`). Do not open connections per request.
- SSH auth is public-key only, checked against the `user_credentials` table (`getPublicKeyHandler` in `main.go`). No shell: `handler.HandleNormalSession` rejects normal sessions.
- The SSH listener is wrapped by `newLimitedListener` (`limit.go`): connections over `ssh_max_connections` are accepted and closed immediately. `IdleTimeout` is set on the server; `MaxTimeout` is deliberately unset so long transfers are not cut.
- SFTP uses the request-based server (`handler/filesystem.go`, `GetFileSystemRequestHandler`) and that is the only SFTP path. Do not switch to `sftp.NewServer`: it serves from the root of the filesystem, and `WithServerWorkingDirectory` only rebases *relative* paths, so absolute paths from the client escape the user directory. It exposes no filesystem or path hook, so it cannot be confined from the outside. An earlier handler using it was removed for this reason.
- SFTP confinement is enforced with `os.Root`: the users directory is opened once with `os.OpenRoot`, each session opens `<username>` beneath it (created on first session, after `validateUsername`), and client paths are converted by `toRootRelative`. All file operations in the handler must go through the session's `*os.Root`; never call `os.*` with a joined path.
- `api/middleware.go` `isAdmin()` is a stub that returns `true` — the API is effectively unauthenticated. Treat as known TODO, don't assume auth exists. `administrative_users` is validated at startup but not yet used.
- Two listeners: SSH on `FILESERVER_SSH_PORT`, HTTP API on `FILESERVER_API_PORT`, both shut down on SIGINT/SIGTERM with a 10s timeout.

## Layout

- `main.go`, `config.go` — wiring, configuration, host key, public-key auth
- `limit.go` — listener capping concurrent SSH connections
- `handler/` — SSH/SFTP session handlers
- `api/` — Gin router, handlers, middleware, request/response models
- `db/` — GORM models + migration
- `docs/` — generated swagger
- `data/`, `keys/`, `benchmarks/` — gitignored local artifacts
