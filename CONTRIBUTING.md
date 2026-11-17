# Contributing

Thanks for taking an interest. Bug reports and small focused PRs are the most
useful things you can send.

## Before you write code

- **Bug fix**: just open a PR. Link the issue if there is one.
- **New feature**: open an issue first so we can agree on the shape before you
  spend an evening on it.
- **The API contract belongs to the player.** Its source of truth is
  [`docs/contract/player.openapi.yaml`](https://github.com/Streamline-HQ/streamline-player/blob/main/docs/contract/player.openapi.yaml)
  in streamline-player: a change to what the apps send or receive starts there,
  and this extension follows it.
- **A change Streamline has to make** (a view column, an SDK function, how
  extensions are served) belongs in
  [Streamline](https://github.com/DataHearth/streamline), not here.

## Setup

Requires Go (the `go` directive in `go.mod`) and [Task](https://taskfile.dev).
The SDK, `github.com/datahearth/streamline/extensions/sdk`, has no tagged
release yet, so `go.mod` pins it to a Streamline commit; move it with
`go get github.com/datahearth/streamline/extensions/sdk@<commit>`. To build
against a local Streamline checkout, `go work use ../streamline/extensions/sdk`
and leave that line out of the commit. The end-to-end suite also needs a
Streamline binary, the engine (from
[streamline-player](https://github.com/Streamline-HQ/streamline-player)), and
`ffmpeg` and `ffprobe` on `PATH`.
Nix users get Go (at exactly the `go.mod` version), Task, ffmpeg and the formatters
from the flake devshell (`nix develop`, or direnv).

```bash
git clone https://github.com/DataHearth/streamline.git
git clone https://github.com/Streamline-HQ/streamline-player-ext.git
cd streamline-player-ext
task build              # player.wasm
```

## The loop

```bash
task lint
task fmt
task test:unit                                        # host-free packages, natively
STREAMLINE_BIN=/path/to/streamline ENGINE_BIN=/path/to/engine task test:e2e
```

`go run ./e2e/cmd/stack --library <json>` boots Streamline, the extension and
the engine on a seeded library and prints one JSON line with the addresses and
the admin login, for trying a change by hand.

## Conventions

- **One Go module for the guest**, built for `wasip1`, importing nothing but
  the SDK and the pure packages at the root (`access/`, `envelope/`, `route/`,
  `scan/`, `sign/`, `tokens/`, ...). The end-to-end suite is its own module
  (`e2e/`).
- **The host contract** (views, folders, services, mounts, cards) is described
  in Streamline's
  [Writing an extension](https://github.com/DataHearth/streamline/wiki/Writing-an-extension)
  page and in `extensions/sdk`. A guest keeps nothing in memory between
  requests: everything it needs again is a row in its own database.
- **The engine is reached only through the `http` service**, at the hosts the
  admin lists in Streamline's config. Never assume another way out.
- **Credentials never reach a log or a response** other than the one that
  creates them: device keys, access tokens, pairing codes, the engine secret.
- **Tests**: Ginkgo + Gomega. Pure packages carry `Label("unit")` specs, the
  end-to-end suite `Label("e2e")`. Each spec uses its own accounts and cleans up
  after itself.
- **Comments**: only where the code cannot explain itself.

## AI-assisted contributions

Streamline is built with heavy AI assistance, so this is not a "no AI" project,
and you don't need to hide that you used a coding agent. There is one rule:

**You are the author. The model is a tool.**

In practice:

- **Understand every line you submit.** If a reviewer asks why a function is
  shaped that way and the honest answer is "the model wrote it", the PR isn't
  ready. Read the diff before you open it.
- **Run `task lint` and the tests yourself.** An agent claiming the tests pass
  is not evidence that they pass. Paste real output if a change is subtle.
- **Verify the code against reality, not plausibility.** Agents invent SDK
  functions, view columns and API fields that look exactly right and don't
  exist. Build it.
- **Mention it in the PR description.** One line, "written with Claude Code",
  so a reviewer knows which failure modes to look for. No stigma attached.

What gets closed without much discussion:

- Large unreviewed diffs the submitter can't explain, or PRs that clearly
  weren't run
- Agent-generated refactors, "modernisations", or comment/docstring sweeps nobody
  asked for
- Scanner or agent output filed as a bug report or security report with no
  reproduction: see [SECURITY.md](SECURITY.md) for what a real report looks like

Reviewer time is the scarce resource here. A small PR you fully understand is
worth more than a large one you don't.

## Commits and PRs

Conventional Commits, subject line only unless the body earns its place:

```
feat(playback): sessions, heartbeats, tracks and halts against the engine
feat(account): Me.player_url, the web player where an app sends a person to approve a TV
```

`type(scope): msg`, scope optional. Keep the PR to one topic. Rebase on `main`
rather than merging it in. Fill in the PR template checklist.

## License

Contributions are licensed under [GPL-3.0-or-later](LICENSE), same as the
project.
