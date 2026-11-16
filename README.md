# streamline-player-ext

The `player` extension for [Streamline](https://github.com/DataHearth/streamline): the server behind every Streamline player app (TV, desktop, web). It is a WebAssembly module the server loads, served at `/extensions/player`. It answers the player API, drives the streaming engine over HTTP and keeps its own state (devices, tokens, playback sessions, watch state, activity, downloads) in its own database; it reads the library through the views Streamline grants it.

## Install

In Streamline, Settings → Extensions → Add: give it `player.wasm` and its SHA-256, then review what it asks for.

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
3. In Server settings (or `PATCH /admin/settings`), set `player.origins` to the origin of every web player (`https://watch.example.net`), and `player.url` to the player's public address.
4. Read the engine secret once from Server settings (`POST /admin/engine/secret`), put it in the engine's config (`secret:` or `secret_file:`) and start the engine.
5. Set the engine's address in Server settings (`engine.url`, and `engine.public_url` when players reach it by another address).

Until step 5 is done the API answers playback requests with `503 engine_unavailable`; browsing, accounts and admin pages keep working.

## Build

```sh
task build        # player.wasm
task lint
task test:unit
STREAMLINE_BIN=/path/to/streamline ENGINE_BIN=/path/to/engine task test:e2e
```

The e2e suite needs `ffmpeg` and `ffprobe` on `PATH`. The SDK is `github.com/datahearth/streamline/extensions/sdk` from a sibling `../streamline` checkout (a `replace` in `go.mod`) until Streamline publishes it with the asks this extension needs.

`go run ./e2e/cmd/stack --library <json>` boots Streamline, the extension and the engine on a seeded library and prints one JSON line with the addresses and the admin login.

## License

GPL-3.0, see [LICENSE](LICENSE).
