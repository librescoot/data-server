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
| `-addr` | `:8080` | Local HTTP address and port, restricted to trusted ingress interfaces. The default supports IPv4 and IPv6 where available. |
| `-data` | `/data` | Directory to list, serve, write, and delete within. |

Examples:

```sh
# Listen through trusted management interfaces only
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

Listener acceptance tests use isolated Linux network namespaces and veth links
named after trusted and untrusted interfaces. They require Python 3, iproute2,
IPv6 support and permission to create namespaces. They do not alter the host's
interfaces, routes or firewall:

```sh
GOTOOLCHAIN=go1.25.14 go test -race ./...
GOTOOLCHAIN=go1.25.14 go build -o /tmp/data-server-listener-test ./cmd/data-server
sudo python3 tests/test-listeners.py --binary /tmp/data-server-listener-test
```

These checks cover late interfaces and addresses, link down/up, deletion and
recreation, revocation on rename (including keep-alive connections), IPv4/IPv6,
WAN access to private service addresses, source-address impersonation, and
maintenance file operations.

## Deployment and runtime dependencies

The Yocto package installs `/usr/bin/data-server` and systemd unit `librescoot-data-server.service`. The unit runs as root, serves `/data`, and selects `-addr 192.168.7.1:8080` on MDB or `-addr 192.168.7.2:8080` on DBC. It requires and starts after network configuration, which keeps that service address available over the PPP backup when USB is down. These explicit addresses remain subject to the same ingress restrictions as the standalone default.

On Linux, each listener is bound with `SO_BINDTODEVICE` to `lo`, `usb0`, `ppp0`,
or an interface whose name starts with `wg`. This checks ingress devices rather
than client addresses or HTTP headers. A WAN packet addressed to a private
service IP is not accepted. The program never creates an unrestricted fallback
listener, and fails if it cannot enforce device binding or runs on a non-Linux
platform. The packaged root service has permission to bind devices; custom
service restrictions must retain that permission.

The interface inventory is reconciled every second. New interfaces get their own
listeners; deleted or renamed interfaces have their listeners and active
connections closed. Address changes on an existing interface do not require a
restart. If an explicit service address is not yet available, binding is retried
without widening the address or interface policy.

The program requires a writable configured directory. It has no authentication
provider, TLS configuration or request-size limit; trusted-interface access
allows all supported file operations.

## Operational and security notes

- Both standalone and packaged listeners restrict ingress to the trusted interface names above. The packaged service also limits destination addresses to the stable board service IP, so routed VPN clients use that address rather than the VPN IP. Keep the MDB firewall enabled as an additional boundary; this service restriction does not depend on it. Use `-addr 127.0.0.1:8080` when remote access is unnecessary.
- Treat uploaded content as untrusted. The service does not validate file type, content, or size, and a client can consume available storage.
- Restrict filesystem permissions and review the configured base directory before enabling write access. Its contents may include credentials, diagnostic data, or files consumed by other services.
- Atomic replacement protects a completed upload from partial target writes, but a client must still retry failures and verify downloaded content where integrity matters.

## License

Original Librescoot code in this repository is available, at your option, under
[AGPL-3.0-or-later](LICENSE) or [CC-BY-NC-SA-4.0](LICENSE.CC-BY-NC-SA-4.0).
Third-party code retains its existing license.

Made with ❤️ by the Librescoot community
