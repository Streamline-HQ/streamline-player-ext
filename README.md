# streamline-player-ext

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](LICENSE)

Player extension for [Streamline](https://github.com/DataHearth/streamline), as a WebAssembly extension: the API behind the [Streamline Player](https://github.com/Streamline-HQ/streamline-player) apps, with devices, TV pairing, playback sessions, watch state and downloads, driving the streaming engine. Streamline loads it and serves it at `/extensions/player`.

## Features

- The player API every Streamline Player app talks to (TV, desktop, web)
- Sign-in through Streamline's own page (authorization code + PKCE), and device pairing for TVs
- Devices, tokens, playback sessions, watch state, activity and downloads, kept in its own database
- Drives the streaming engine over HTTP: it picks copy or transcode from what each device can play
- Reads the library (movies, series, people) through the views Streamline grants it

## Install

There is no release yet: build `player.wasm` (see [Development](#development)). In Streamline, Settings → Extensions → Add: give it `player.wasm` and its SHA-256, then review what it asks for.

### What to grant

- Views: `users`, `user_logins`, `movies`, `series`, `seasons`, `episodes`, `video_files`, `people`, `credits`.
- Folders: `movies`, `series`, `artwork`, `cache`.
- Service: `http`, for the engine.
- Mounts: `/` (the API; the extension checks the credentials itself) and `/authorize` (Streamline signs the browser in first).

### The hosts

The `http` service reaches only the hosts the admin lists for the extension in Streamline's config file. List the engine's `host:port`, and Streamline's own `host:port` as players reach it: the sign-in card asks Streamline's `/register` page whether it is open, and without the entry it never offers registration.

```yaml
extensions:
  - name: player
    source: /path/to/player.wasm
    enabled: true
    hosts:
      - engine.lan:8097
      - streamline.example.net
```

### Bootstrap order

1. Add and approve the extension, with the `hosts` entry above.
2. Sign in to the desktop app first: its loopback sign-in needs no setting. The web player cannot sign in yet, because `redirect_uri` is refused until `player.origins` lists its origin, and only an admin can change that.
3. In Server settings (or `PATCH /admin/settings`), set `player.origins` to the origin of every web player (`https://watch.example.net`), and `player.url` to the player's public address. A web player on another origin than Streamline also needs that origin in Streamline's `server.cors_origins`, or the browser blocks every answer and the sign-in card reports Streamline unreachable.
4. Read the engine secret once from Server settings (`POST /admin/engine/secret`), put it in the engine's config (`secret:` or `secret_file:`) and start the engine.
5. Set the engine's address in Server settings (`engine.url`, and `engine.public_url` when players reach it by another address).

The engine calls Streamline back, at the address the admin who starts a library scan uses: it posts each finished analysis, and it keeps a scan going until it is done, so a scan finishes with nobody connected. The engine must be able to reach that address. When it cannot, a scan advances and its results are collected only while an admin has the Library page open.

Until step 5 is done the API answers playback requests with `503 engine_unavailable`; browsing, accounts and admin pages keep working.

## Documentation

- [Player API contract](https://github.com/Streamline-HQ/streamline-player/blob/main/docs/contract/player.openapi.yaml), owned by the player repository
- [Extensions](https://github.com/DataHearth/streamline/wiki/Extensions) on the Streamline wiki: how extensions are added and approved

## Development

```sh
task build        # player.wasm
task lint
task test:unit
STREAMLINE_BIN=/path/to/streamline ENGINE_BIN=/path/to/engine task test:e2e
```

The e2e suite needs `ffmpeg` and `ffprobe` on `PATH`. The SDK is `github.com/datahearth/streamline/extensions/sdk`, pinned in `go.mod` to a Streamline commit until it has a tagged release.

`go run ./e2e/cmd/stack --library <json>` boots Streamline, the extension and the engine on a seeded library and prints one JSON line with the addresses and the admin login.

## Contributing

Bug reports and PRs welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for setup,
the build/test loop, conventions and the policy on AI-assisted PRs. Participation
is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).

Found a vulnerability? Don't open an issue — see [SECURITY.md](SECURITY.md).

## Streamline repositories

| Repository | What it is |
| --- | --- |
| [streamline](https://github.com/DataHearth/streamline) | The server: media manager, web UI and extension host |
| [streamline-player](https://github.com/Streamline-HQ/streamline-player) | The player apps (Android TV, desktop, web) and the HLS streaming engine |
| [streamline-player-ext](https://github.com/Streamline-HQ/streamline-player-ext) | The `player` extension: the API the player apps talk to |
| [streamline-opds-ext](https://github.com/Streamline-HQ/streamline-opds-ext) | OPDS 1.2 catalog for e-reader apps |
| [streamline-subsonic-ext](https://github.com/Streamline-HQ/streamline-subsonic-ext) | Subsonic / OpenSubsonic server for music apps |

## License

[GPL-3.0](LICENSE)
