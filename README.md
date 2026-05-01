# qui-sync

> **⚠️ EARLY DEVELOPMENT — NOT READY FOR PRODUCTION USE**
>
> Features are incomplete, APIs may change, and data loss is possible. **For testing only.** Feedback welcome.

A web UI for managing [Qui](https://github.com/autobrr/qui) automation rules as code.

## What it does

- **Publish** — Pull automations from your Qui instances into a versioned git repo. Your tracker lists are replaced with placeholders for safe sharing. Push to GitHub when ready.
- **Subscribe** — Subscribe to someone else's repo and import their rules into your Qui. Your trackers, enabled state, and intervals are preserved on every update.
- **Backup & Restore** — Full 1:1 snapshots of every automation in a Qui instance. Run on a schedule or manually. Restore to the same instance, or migrate to a different one.
- **Auto-sync** — Keep subscribed rules in sync automatically. Per-rule opt-in — you choose what auto-updates and what stays manual.

Files are organised as `<folder>/<Rule Name>.json` — the same convention used by community-maintained qui automation repos, so qui-sync can read and write them interchangeably.

## Quick start

1. Install the container ([Unraid](#unraid) or [Docker](#docker))
2. Open `http://<your-ip>:6070` — first run prompts you to create an admin user
3. Log in, then Settings → enter Qui URL + API key → add instances
4. Start publishing, subscribing, or backing up

For detailed walkthroughs of every feature, see **[How-To Guide](docs/how-to.md)**.

## Running

### Unraid

Place `my-qui-sync.xml` in `/boot/config/plugins/dockerMan/templates-user/`. Add Container from template.

### Docker

```bash
docker run -d --name qui-sync \
  -p 6070:6070 \
  -e PUID=99 -e PGID=100 \
  -v /path/to/appdata/qui-sync:/config \
  -v /path/to/appdata/qui-sync/data:/data \
  ghcr.io/prophetse7en/qui-sync:dev
```

No manual file editing needed — config is auto-created on first run, everything configured from the web UI.

## Data layout

```
/config/          Config + secrets (auto-created)
/data/
├── repo/         Share-repo — push this to GitHub
├── state/        Local machine state (never pushed)
├── backups/      Archived old rule versions
└── sources/      Cloned subscription repos
```

## Support

- **Discord:** [`#qui-sync`](https://discordapp.com/channels/492590071455940612/1495687451531018352) on the [TRaSH Guides Discord](https://trash-guides.info/discord) (under Community Apps). Questions, usage help, feedback on the early-dev build.
- **GitHub issues:** [prophetse7en/qui-sync/issues](https://github.com/prophetse7en/qui-sync/issues) — bug reports.

## Disclaimer

qui-sync is in early development and may contain bugs that could affect your Qui automations. Always **Backup** before applying a subscription update, and keep an extra snapshot of your Qui rules outside qui-sync until the project leaves early development.

The authors are not responsible for any unintended changes to your Qui automations. **Use at your own risk.**

qui-sync is developed with active AI assistance (Claude, Anthropic) under human direction. Architectural decisions, code review, testing against real Qui instances, and releases are done by ProphetSe7en. Issues and PRs go through a human review.

## License

[MIT](LICENSE) — © 2026 ProphetSe7en.
