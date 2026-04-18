# qui-sync

> **⚠️ EARLY DEVELOPMENT — NOT READY FOR PRODUCTION USE**
>
> Features are incomplete, APIs may change, and data loss is possible. **For testing only.** Feedback welcome.

A web UI for managing [Qui](https://github.com/autobrr/qui) automation rules as code.

## What it does

- **Export** — Pull automations from your Qui instances into a versioned git repo. Your tracker lists are replaced with placeholders for safe sharing. Push to GitHub when ready.
- **Sync** — Subscribe to someone else's repo and import their rules into your Qui. Your trackers, enabled state, and intervals are preserved on every update.
- **Auto-sync** — Keep rules in sync automatically. Per-rule opt-in — you choose what auto-updates and what stays manual.

## Quick start

1. Install the container ([Unraid](#unraid) or [Docker](#docker))
2. Open `http://<your-ip>:6070`
3. Settings → enter Qui URL + API key → add instances
4. Start exporting or subscribing

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

## License

Not yet decided. All rights reserved until a license is chosen.
