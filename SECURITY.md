# Security Policy

## Supported versions

| Version              | Supported          |
| -------------------- | ------------------ |
| `main`               | :white_check_mark: |

There is no release yet: fixes land on `main`. streamline-player-ext is
maintained by one person.

## Reporting a vulnerability

**Do not open a public issue.**

Use GitHub's private vulnerability reporting:
[Report a vulnerability](https://github.com/Streamline-HQ/streamline-player-ext/security/advisories/new).
If that is unavailable to you, email dev@antoine-langlois.net instead.

Please include:

- The commit you built and the Streamline version it runs on
- What an attacker can do: sign in as someone else, pair a TV to another
  account, play or download a title without credentials, read another account's
  watch state, etc.
- Steps to reproduce, ideally with the requests involved (tokens and secrets
  redacted)

Expect an acknowledgement within a week and a fix or a decision within 30 days
for anything exploitable. This is a spare-time project, not a vendor with an
on-call rotation: if something is actively being exploited, say so up front and
I will prioritise it. I will credit you in the advisory unless you'd rather stay
anonymous.

Please give me a chance to ship a fix before disclosing publicly. There is no
bug bounty.

## Scope

In scope: anything that breaks the security model of the extension on a
correctly configured Streamline:

- The sign-in flow at `/authorize`: authorization codes, PKCE, `redirect_uri`
  checks against `player.origins`, open redirects
- Device pairing: approving a TV for an account that did not ask for it,
  guessing or replaying a pairing code
- Device keys and access tokens: one that outlives its revocation, one account
  using another's, a token leaking into a log or a response
- Playback tokens and the engine secret: minting a playback token for a title
  or session that is not yours, the secret leaking past Server settings
- One account reading or changing another's devices, sessions, watch state,
  activity or downloads, or a member reaching the admin routes
- A path the extension hands the engine or serves itself reaching a file
  outside the granted library folders
- SQL injection through any parameter

Out of scope:

- Vulnerabilities in Streamline itself (the extension host, its sandbox, its
  views): report those to
  [Streamline](https://github.com/DataHearth/streamline/security/advisories/new).
- Vulnerabilities in the player apps or the engine: report those to
  [streamline-player](https://github.com/Streamline-HQ/streamline-player/security/advisories/new).
- The API over plain HTTP. Streamline is built to sit behind TLS.
- Anything requiring existing admin credentials. Admins set the origins and the
  engine secret by design.
- Scanner or agent output with no demonstrated path to exploitation.
