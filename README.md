# data-server

A minimal HTTP file server for the `/data` partition on Librescoot vehicles (MDB and DBC). It replaces the ad-hoc Python upload servers that the installer spawns during provisioning.

Part of the [Librescoot](https://librescoot.org/) open-source platform.

## Features

- **GET** `/` — JSON file listing, or drag-and-drop web UI (content-negotiated on `Accept: text/html`)
- **GET** `/<path>` — download a file
- **PUT / POST / PATCH** `/<path>` — upload a file (atomic write via temp file + rename)
- **DELETE** `/<path>` — delete a file
- Subdirectory creation on upload
- Path traversal protection

## Usage

```
data-server [-addr 0.0.0.0:8080] [-data /data]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-addr` | `0.0.0.0:8080` | Listen address |
| `-data` | `/data` | Base directory to serve |

## Building

```bash
# ARM binary (for MDB/DBC)
make build

# Host binary (for local testing)
make build-host

# Run tests
make test
```

Requires Go 1.24+. The ARM build cross-compiles to `GOARCH=arm GOARM=7` with CGO disabled and static linking.

## License

This project is dual-licensed. The source code is available under the
[Creative Commons Attribution-NonCommercial-ShareAlike 4.0 International License][cc-by-nc-sa].
The maintainers reserve the right to grant separate licenses for commercial distribution; please contact the maintainers to discuss commercial licensing.

[![CC BY-NC-SA 4.0][cc-by-nc-sa-image]][cc-by-nc-sa]

[cc-by-nc-sa]: http://creativecommons.org/licenses/by-nc-sa/4.0/
[cc-by-nc-sa-image]: https://licensebuttons.net/l/by-nc-sa/4.0/88x31.png
