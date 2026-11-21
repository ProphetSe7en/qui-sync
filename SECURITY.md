# Security Policy

## Supported versions

qui-sync is in early development and ships on one channel, `:dev`. There is no
`:latest` tag and no stable release yet.

| Version | Security updates |
|---------|------------------|
| `ghcr.io/prophetse7en/qui-sync:dev` (current build) | Yes |
| Any older image you have pulled | No. Force Update to the current `:dev` first. |

Because there is only one channel, a security fix reaches you by rebuilding
from the newest `:dev` image.

## Reporting a vulnerability

**Please do not open a public GitHub issue for security bugs.** Describing an
attack path in public before a fix ships puts other users at risk.

### Reporting channel

Use GitHub's private vulnerability reporting:
**[Report a vulnerability](https://github.com/ProphetSe7en/qui-sync/security/advisories/new)**

This is the only intended channel. Reports stay private to maintainers, and
there is no email thread to leak.

### What to include

- The version string from the UI footer or the server log line at startup
- Clear reproduction steps, ideally request plus expected and actual response
- What the attacker gains: which data, which repository, which credentials
- Your disclosure timeline preference

### What to expect

- Acknowledgement within 72 hours, usually faster. Solo maintainer, best effort.
- Triage and severity assessment within 7 days, including whether the finding
  is accepted and what the fix and disclosure timeline look like.
- A fix within 14 days for high severity findings. Lower severity may wait for
  the next release cycle.
- Coordinated disclosure. A patched build ships first, then you are credited in
  `CHANGELOG.md` and here, unless you prefer to stay anonymous.

## Security model

qui-sync is a local admin tool. It reads automation rules from Qui, writes them
into a git repository, and pushes that repository to a remote you configure.
The design assumes:

- You control the host it runs on.
- You do not expose port 6070 to the internet without a reverse proxy.
- You protect `/config/` the way you protect any other secret store, since the
  Qui API key and git credentials live there.

### What qui-sync does

- **Authentication is required by default.** A first run sends you to a setup
  wizard that creates the admin account, so there are no default credentials.
  Passwords are hashed with bcrypt at cost 12.
- **Three auth modes.** Forms with a session cookie (default), HTTP Basic for
  reverse-proxy setups, and None, which requires an explicit environment
  variable to confirm you meant it.
- **Trusted Networks.** An allowlist of addresses and CIDR ranges that skip the
  login page, for the common case of a trusted LAN.
- **Host-level lock on the trust boundary.** `TRUSTED_NETWORKS` and
  `TRUSTED_PROXIES` can be set as environment variables, and then the UI cannot
  widen them. This limits what an attacker who reaches the UI can change.
- **Forwarded headers are only honoured from a trusted proxy.** The direct peer
  address has to match a configured proxy before `X-Forwarded-For` is read.
- **CSRF protection** on every state-changing request, using the double-submit
  cookie pattern.
- **Security headers** on responses: `X-Frame-Options: DENY`,
  `X-Content-Type-Options: nosniff`, and `Referrer-Policy: same-origin`.
- **Secrets are stored outside the repository.** The Qui API key and git tokens
  live under `/config/` with mode 0600, never in the share repository and never
  in the state file, which holds paths rather than values.
- **Tokens are stripped from anything displayed.** A git remote URL that carries
  a token is sanitised before it reaches the UI or the logs.
- **Read-only against Qui.** The Qui client refuses any request that is not a
  GET, so an export cannot modify the Qui instance it reads from.
- **One public endpoint.** `/api/health` returns a liveness signal and no data,
  so container healthchecks work in every auth mode. The login, setup and
  static asset paths are public by necessity. Everything else requires auth.

### What qui-sync does not do, by design

- **No rate limiting on the login endpoint.** Put a reverse proxy in front of
  it (fail2ban, CrowdSec, or your proxy's own throttling) if the UI is reachable
  from an untrusted network. Basic auth without a rate limit is a CPU
  amplification vector, so prefer Forms mode in that setup.
- **No account lockout**, for the same reason.
- **No encryption at rest for the Qui API key or git tokens.** They are stored
  as files with mode 0600, the same trust model Radarr and Sonarr use for their
  own API keys. An attacker who can read `/config/` could read a local keystore
  key from the same place.
- **No audit log of admin actions.** The git history of the share repository
  records every exported change, and your reverse proxy logs cover requests.
- **No TLS.** qui-sync serves plain HTTP on port 6070. Terminate TLS in a
  reverse proxy and configure `TRUSTED_PROXIES` so that `X-Forwarded-Proto` is
  honoured and session cookies are marked secure.

## Supply chain

- The image builds on current Go and Alpine releases, and the build fails
  rather than shipping if the one vendored browser library does not match its
  recorded checksum.
- GitHub Actions used by the build are pinned to commit hashes.
- Dependency updates are proposed automatically and reviewed before merge.
- `go vet`, the test suite with the race detector, and `govulncheck` run on
  every push and pull request against `main`.

## Changelog of security-relevant changes

See the Security sections in `CHANGELOG.md`.
