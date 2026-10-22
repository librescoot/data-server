# Librescoot Data Server

Part of the [Librescoot](https://librescoot.org/) open-source platform.

## Overview

`data-server` is a small HTTP server for a vehicle data directory. The deployed service serves `/data` on port 8080, providing browser-friendly listing and file transfer for provisioning and maintenance workflows.

## Capabilities

- Returns a JSON listing at `GET /`, or an embedded drag-and-drop HTML interface when the request `Accept` header contains `text/html`.
- Serves files and directories below the configured data directory for `GET` and `HEAD` requests.
- Accepts complete-file uploads with `PUT`, `POST`, or `PATCH`; creates parent directories as needed.
- Writes uploads through a temporary file, file sync, rename, and parent-directory sync.
- Deletes a path with `DELETE`.
- Adds `Server: librescoot-data-server/<version>` to every response.

## Operation and interfaces

The handler accepts the following methods:

| Request | Behaviour | Success response |
|---|---|---|
| `GET /` | HTML UI when `Accept` contains `text/html`; otherwise JSON array of direct child entries (`name`, `size`, and optional `dir`). | `200 OK` |
| `GET /path` and `HEAD /path` | Serves content from the configured base directory using Go's file server. | `200 OK` when found |
| `PUT`, `POST`, or `PATCH /path` | Replaces the target file with the request body; creates missing parent directories. | `200 OK` |
| `DELETE /path` | Removes the target path. | `204 No Content` |

Unsupported methods return `405 Method Not Allowed`. Uploading to or deleting `/` returns `400 Bad Request`; deleting a missing path returns `404 Not Found`. Write and delete paths are joined under the configured base directory and reject paths outside it.

There is no Redis/Valkey interface.

## Configuration

```text
data-server [-addr ADDRESS] [-data DIRECTORY]
```

| Flag | Default | Meaning |
|---|---|---|
| `-addr` | `0.0.0.0:8080` | HTTP listen address. |
| `-data` | `/data` | Directory to list, serve, write, and delete within. |

Examples:

```sh
# Standalone defaults (wildcard listener)
data-server

# Limit a local test instance to loopback and a temporary directory
data-server -addr 127.0.0.1:8080 -data ./testdata
```

## Build and test

Requires Go and the dependencies declared in `go.mod`.

```sh
make build       # static Linux ARMv7 binary: bin/data-server
make build-host  # host binary: bin/data-server
make test        # uses GOTOOLCHAIN=go1.25.7
make lint        # requires golangci-lint
```

`make fmt`, `make deps`, and `make clean` are also available. The ARM build uses `GOARCH=arm`, `GOARM=7`, disabled CGO, and `-trimpath`.

## Deployment and runtime dependencies

The Yocto package installs `/usr/bin/data-server` and systemd unit `librescoot-data-server.service`. The unit runs as root, serves `/data`, and selects `-addr 192.168.7.1:8080` on MDB or `-addr 192.168.7.2:8080` on DBC. It requires and starts after network configuration, which keeps that service address available over the PPP backup when USB is down. A failed bind restarts the service without falling back to a wildcard address. The standalone binary's default remains `0.0.0.0:8080`.

The only runtime requirements are a writable configured directory and a network listener. The program has no authentication provider, TLS configuration, request-size limit, or built-in access-control configuration.

## Operational and security notes

- The standalone default exposes read, write, and delete access to `/data` through a wildcard listener. The packaged service binds only the stable board service address; administrators using a routed VPN must connect to that address rather than the VPN IP. Address binding does not filter ingress interfaces: the MDB firewall still blocks WAN traffic addressed to the private service IP. Use a trusted management network and `-addr 127.0.0.1:8080` when remote access is unnecessary.
- Treat uploaded content as untrusted. The service does not validate file type, content, or size, and a client can consume available storage.
- Restrict filesystem permissions and review the configured base directory before enabling write access. Its contents may include credentials, diagnostic data, or files consumed by other services.
- Atomic replacement protects a completed upload from partial target writes, but a client must still retry failures and verify downloaded content where integrity matters.

## License

This project is licensed under the [GNU Affero General Public License v3.0](LICENSE).

Made with ❤️ by the Librescoot community
