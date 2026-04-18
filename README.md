# qui-sync

> **⚠️ EARLY DEVELOPMENT — NOT READY FOR PRODUCTION USE**
>
> Features are incomplete, APIs may change, and data loss is possible. **For testing only.** Feedback welcome.

A web UI for managing [Qui](https://github.com/autobrr/qui) automation rules as code.

## What it does

### Export (for maintainers who share their rules)

You have Qui automations that work well. You want to share them with others.

1. **Open the Export tab** — see all your Qui instances and their rules.
2. **Choose what to share** — exclude private rules, set the priority order.
3. **Preview** — see exactly what changed since your last export.
4. **Commit export** — writes rule files to a git repo + auto-commits.
5. **Push to remote** — send to GitHub so others can subscribe.

Your personal tracker lists are automatically replaced with placeholders (`tracker_1` / `tracker.xyz`) — consumers fill in their own. Rules that use `*` (all trackers) keep the wildcard.

### Sync (for consumers who import shared rules)

Someone shared their Qui automations. You want to use them.

1. **Add a subscription** — paste the git repo URL. Public repos need no auth; private repos use SSH deploy keys or a personal access token.
2. **Pull** — fetches the latest rules from the repo.
3. **Plan** — preview what will happen. For each rule, choose:
   - **Create new** — adds a new rule to your Qui (disabled by default — you enable it after review).
   - **Link to existing** — updates an existing Qui rule with the repo's logic. **Your trackers, enabled state, intervals, and other personal settings are preserved.** Only the rule logic (conditions, tags, limits) is updated.
   - **Skip** — ignore this rule.
4. **Apply** — executes your choices. New rules are created disabled. Linked rules are updated with the 3-layer merge.
5. **Auto-sync** — toggle per rule. Rules marked "Auto" are automatically applied when the repo updates. New rules always require manual review first.

### Settings

- **Qui connection** — URL + API key (full API key from Qui Settings → API Keys, not a Client Proxy key).
- **Export instances** — which Qui instances to export, and what folder name each gets in the repo.
- **Git push** — token for pushing your share-repo to GitHub.
- **Auto-sync interval** — how often to check for updates (off / 5m / 1h / 6h / 12h / 24h).
- **Backup retention** — how long to keep old versions of changed rules.
- **Text scale** — compact / default / large.

## How data is organized

```
/config/                    (mounted volume — config + secrets)
├── config.yml              (auto-created on first run)
├── api-key                 (your Qui API key, chmod 600)
├── git-push-token          (GitHub PAT for pushing, chmod 600)
└── git-keys/               (SSH deploy keys per subscription)

/data/                      (mounted volume — all data)
├── repo/                   ← YOUR SHARE-REPO. Push this to GitHub.
│   ├── rules/
│   │   ├── movies/*.json
│   │   ├── tv/*.json
│   │   └── misc/*.json
│   ├── CHANGELOG.md
│   └── README.md
├── state/                  (local — never pushed)
│   ├── maintainer.state.json
│   └── consumer.state.json
├── backups/                (local — archived old rule versions)
└── sources/                (local — cloned subscription repos)
```

## What's in a shared rule file

```json
{
  "_slug": "tag-tier1",
  "name": "Tag: Tier 1",
  "trackerPattern": "tracker_1",
  "trackerDomains": ["tracker.xyz"],
  "conditions": { ... },
  "sortOrder": 1,
  "intervalSeconds": 900
}
```

- `trackerPattern` / `trackerDomains`: placeholder values — replace with your own trackers after import. Rules with `"*"` (all trackers) keep the wildcard.
- `sortOrder`: the maintainer's recommended execution order.
- `intervalSeconds`: how often Qui should check this rule.
- `_slug`: stable identifier used for filenames and linking.
- Fields like `enabled`, `dryRun`, `notify`, `freeSpaceSource` are stripped — they're personal preferences you set yourself in Qui after import.

## Advanced: editing rules directly

You can edit the JSON files in `/data/repo/` directly (with VSCode, nano, or any editor). This is useful for bulk changes like find-and-replace across many rules.

After editing:
- **To push changes to GitHub:** go to Export tab → Share-repo panel → Push to remote.
- **To apply changes back to your Qui:** go to Export tab → click "Apply to Qui" → switch to Sync tab → Plan → Apply. Your tracker lists are preserved.
- **To accept a PR from GitHub:** go to Export tab → Pull from remote → then Apply to Qui as above.

## Running

### Unraid

Use the template file (`my-qui-sync.xml`) — place it in `/boot/config/plugins/dockerMan/templates-user/`. Then Add Container from the template.

### Docker

```bash
docker run -d --name qui-sync \
  -p 6070:6070 \
  -e PUID=99 -e PGID=100 \
  -v /path/to/appdata/qui-sync:/config \
  -v /path/to/appdata/qui-sync/data:/data \
  ghcr.io/prophetse7en/qui-sync:dev
```

Open `http://<your-ip>:6070`. Go to Settings → enter your Qui URL + API key → done.

### First run

No manual file editing needed. The container auto-creates `config.yml` on first start. Open the web UI, go to Settings, fill in your Qui URL and API key, add your instances. Everything else is configured from the UI.

## Compatible with

- [TRaSH-/qui_workflows](https://github.com/TRaSH-/qui_workflows) — qui-sync's export format is compatible with TRaSH's shared automations format (placeholder trackers, same field structure).

## License

Not yet decided. All rights reserved until a license is chosen.
