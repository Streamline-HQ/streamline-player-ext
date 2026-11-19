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
- Folders: `movies`, `series`, `artwork`, `cache`, `store` (backdrops; mounted only when the entry sets `store_bytes`).
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

### Settings in Streamline's config file

The settings and the engine secret can be written in Streamline's config file, on the extension's entry. That needs no sign-in, so a declarative deployment comes up in one go:

```yaml
extensions:
  - name: player
    # source, sha256, grants and hosts as above
    settings:
      engine.url: http://engine.lan:8097
      engine.public_url: https://engine.example.net
      player.url: https://watch.example.net
      player.origins: [https://watch.example.net]
    secret_files:
      engine: /etc/streamline/secrets/player-engine-secret
```

- The keys are the ones `PATCH /admin/settings` takes for the player (`engine.url`, `engine.public_url`, `player.url`, `player.origins`, `player.app_links`, `network.home`). The engine's own settings stay in the engine's config.
- `secret_files.engine` is the secret shared with the engine. Give the engine the same file (`secret_file:`), so it is generated once and never copied.
- The file then holds all of the player's settings: one it leaves out is unset, Server settings shows them locked, and a change or a secret rotation answers `409 read_only`.
- A value the player cannot use, an unknown key, or an `engine.url` whose `host:port` is not in `hosts` keeps the extension from starting, with the reason on Streamline's Extensions page.

The order below is for a deployment that sets them from the app instead.

### Backdrops

Streamline keeps a title's poster but no backdrop, so the extension fetches them itself, from TMDB, and from TVDB for a series TMDB does not know. They are off until the entry gives it a TMDB key, room to keep the files and the providers' hosts:

```yaml
extensions:
  - name: player
    store_bytes: 1073741824
    secret_files:
      tmdb: /etc/streamline/secrets/tmdb-api-key # the API read access token, as Streamline's own metadata uses
      tvdb: /etc/streamline/secrets/tvdb-api-key # optional
    hosts:
      - api.themoviedb.org
      - image.tmdb.org
      - api4.thetvdb.com
      - artworks.thetvdb.com
```

- A library scan fetches the backdrop of every title that has none, and Streamline's `library` hook brings one title's up to date when it is added, refreshed or deleted. Nothing is fetched when an app asks.
- A file is around 250 KB (the `w1280` rendition), kept in the `store` folder under `backdrops/`. Once the folder holds `store_bytes`, fetching stops with an error in Streamline's log.
- A title no provider has a backdrop for, or that failed three times in a row, is tried again by a scan a week later, or when Streamline refreshes its metadata.
- With a `tmdb` secret, a TMDB host missing from `hosts` keeps the extension from starting, with the reason on the Extensions page. Upgrading from a version without backdrops asks for the grants to be approved again, as the manifest now lists `store`.

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

## Importing watch state from Plex or Jellyfin

`cmd/watch-import` copies what every user has watched (watched, resume position, last viewed) into the player, through `POST /admin/watchstate/import`. Give it `-plex`, `-jellyfin`, or both.

```sh
PLEX_TOKEN=… go run ./cmd/watch-import -plex http://plex.lan:32400 -dry-run
JELLYFIN_TOKEN=… go run ./cmd/watch-import -jellyfin http://jellyfin.lan:8096 \
  -user ana=ana@example.net -user ben=ben@example.net -dry-run
PLEX_TOKEN=… PLAYER_TOKEN=… go run ./cmd/watch-import \
  -plex http://plex.lan:32400 -player https://streamline.example.net/extensions/player
```

- `-dry-run` reads the servers and prints what each account has seen, and writes nothing.
- `PLAYER_TOKEN` is an admin's access token (the `Authorization: Bearer` a signed-in web player sends).
- Plex: `PLEX_TOKEN` is the server owner's token, which also gives each shared user's. An account lands on the Streamline user with the same email; a managed Plex user has no email and is skipped.
- Jellyfin: `JELLYFIN_TOKEN` is an API key (Dashboard, API Keys). Jellyfin keeps no email, so name each user's Streamline email with `-user name=email`; a user whose name is already an email needs none, and any other is skipped.
- A movie is found by its TMDB id, an episode by its series' TVDB id with season and episode numbers, so a library on other files than Streamline's still matches. The state lands on every file of the title.
- It can be run again: a state older than what the player already holds is left alone.

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
