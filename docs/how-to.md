# qui-sync — How-To Guide

Detailed walkthrough of every feature. For a quick overview, see [README.md](../README.md).

---

## Table of Contents

1. [First-Time Setup](#first-time-setup)
2. [Export — Sharing Your Automations](#export)
3. [Sync — Importing Someone Else's Automations](#sync)
4. [Backup & Restore](#backup--restore)
5. [Common Scenarios](#common-scenarios)

---

## First-Time Setup

### 1. Install the container

**Unraid:** Place `my-qui-sync.xml` in `/boot/config/plugins/dockerMan/templates-user/`. Go to Docker → Add Container → select qui-sync from the template dropdown. Click Apply.

**Docker:** 
```bash
docker run -d --name qui-sync \
  -p 6070:6070 \
  -e PUID=99 -e PGID=100 \
  -v /path/to/appdata/qui-sync:/config \
  -v /path/to/appdata/qui-sync/data:/data \
  ghcr.io/prophetse7en/qui-sync:dev
```

### 2. Open the web UI

Go to `http://<your-server-ip>:6070` in your browser.

You'll see a yellow "Setup needed" banner in Settings. That's expected — you haven't connected to Qui yet.

### 3. Connect to Qui

1. Go to the **Settings** tab.
2. Under **Qui connection**, enter:
   - **URL:** Your Qui server URL (e.g. `http://192.168.1.100:7476`). No trailing slash.
   - **API key:** Your **full API key** from Qui → Settings → API Keys. **Not** a Client Proxy key — those have limited permissions and will fail when qui-sync tries to create or update rules.
3. Click **Save**.

### 4. Add your Qui instances

Still in Settings, under **Export instances**:
1. Click **+ Add instance**.
2. Select a Qui instance from the dropdown (they're fetched live from Qui).
3. Give it a **folder name** (e.g. `movies`, `tv`, `misc`). This becomes the subfolder in your share-repo.
4. Click **Add**.
5. Repeat for each instance you want to manage.

### 5. Set up git push (optional, for sharing)

If you want to push your exported rules to GitHub:
1. Create a [fine-grained Personal Access Token](https://github.com/settings/tokens?type=beta) on GitHub with **Contents: Read and Write** permission on your share-repo.
2. In Settings → **Git push**, paste the token and click Save.

---

## Export

Export pulls automations from your Qui instances and saves them as versioned JSON files. Other users can subscribe to your repo to import them.

### Understanding what gets exported

When you export a rule, qui-sync transforms it for sharing:

| Field | What happens | Why |
|---|---|---|
| `name` | Kept as-is | |
| `conditions` | Kept as-is | The actual rule logic |
| `sortOrder` | Kept (or overridden) | Maintainer sets the execution order |
| `intervalSeconds` | Kept as-is | How often Qui checks the rule |
| `trackerPattern` | Replaced with `"tracker_1"` | Your tracker list is personal — consumers fill in their own |
| `trackerDomains` | Replaced with `["tracker.xyz"]` | Same reason |
| `enabled` | Stripped | Consumer decides what to enable |
| `dryRun` | Stripped | Consumer decides |
| `notify` | Stripped | Consumer decides |
| `freeSpaceSource` | Stripped | Consumer decides |
| `id`, `instanceId`, `createdAt`, `updatedAt` | Stripped | Server-internal |

**Exception:** Rules with `trackerPattern: "*"` (all trackers) keep the wildcard — it's not personal data.

### Step-by-step: your first export

1. Go to the **Export** tab.
2. You'll see your Qui instances listed. Click on one to expand it.
3. **Review the rules:**
   - Each rule has a checkbox. **Uncheck** any rules you don't want to share (private, experimental, etc.).
   - Excluded rules are never exported. The exclusion is remembered.
4. **Set priorities:**
   - The **Priority** column shows the execution order. Edit it to control the order consumers receive your rules in.
   - If you change a priority, a ↺ reset button appears to revert to Qui's value.
5. **Rename the folder** (optional):
   - Click the blue `→ movies` badge to rename the repo folder for this instance. The folder is renamed on disk too.
6. Click **Preview** to see what would change:
   - `+ Added` — new rules not yet in the repo
   - `~ Updated (conditions, sortOrder)` — changed rules, with which fields differ
   - `R Renamed` — rules whose name changed in Qui
   - `⌫ Archived` — rules deleted from Qui. These move to `/data/repo/archive/<folder>/<Rule Name>.json` instead of being removed — history stays visible on GitHub. Consumers syncing from your repo never apply archived rules.
7. (optional) Fill in **Note for this export** — a short explanation of *why* the rules changed. The note becomes the top paragraph of today's entry in `CHANGELOG.md`, above the auto-generated rule list. One note per export; cleared automatically on success.
8. Click **Commit export** to write the changes:
   - Files are written to `/data/repo/<folder>/<Rule Name>.json` — the same layout used by community-maintained qui automation repos, so your repo is compatible with anyone else using qui-sync
   - A git commit is made automatically
   - Old versions are backed up to `/data/backups/`
   - CHANGELOG.md is updated, with your note at the top of today's entry if you provided one

### Pushing to GitHub

After committing, the **Share-repo** panel shows your repo's sync status:

- **"In sync with remote"** — nothing to push
- **"2 commits ahead"** — you have changes ready to push

Below the status you'll find **Review changes before push**. This lists every rule that differs from the remote, grouped as:

- **Added** — new rules you are about to publish
- **Modified** — existing rules whose settings changed. Click a row to see exactly which fields differ (`sortOrder: 3 → 5`, `tags (added): [cross-seed]`).
- **Removed** — rules that exist on the remote but not locally. Pushing deletes them from the remote.

Click **Push to remote** when the list matches what you intend. Requires a push token configured in Settings.

### Verifying your GitHub token

In Settings, after pasting and saving your PAT, click **Validate stored token**. qui-sync asks GitHub who the token belongs to and shows the result inline:

- **"● Valid — \<your-username\>"** — token works, push will succeed
- **"● GitHub rejected the token (401)"** — token is wrong, expired, or mistyped. Regenerate and paste again.
- **"● GitHub denied the request (403)"** — token is real but does not have write access to the target repo. Check that the fine-grained PAT has Contents: Read and Write scoped to the correct repository.

### Pulling from GitHub (accepting PRs)

If someone submitted a PR to your repo and you merged it:

1. The Share-repo panel shows **"1 behind remote"**.
2. Click **Pull from remote** to fetch the changes.
3. The files in `/data/repo/` are now updated.
4. To apply these changes to your Qui: click **Apply to Qui** → switch to Sync tab → Plan → Apply.

### First-time setup against an existing remote

If you are pointing qui-sync at a repo that already has rule files on GitHub (your fork, or a repo you migrated from elsewhere), the first Push can fail with `non-fast-forward` or `unrelated histories`. That is because qui-sync's fresh `git init` starts a separate history from what is already on the remote.

Click **Reset local to remote** on the Share-repo panel. This fetches the remote branch and replaces `/data/repo` with an exact copy, discarding the empty local init. After the reset:

1. Re-run Export — your Qui rules are written on top of the remote content.
2. Review changes — you now see only real differences between your Qui and the remote.
3. Push — sends just your changes, no history conflict.

The button is destructive (it throws away any local commits that are not on the remote), but on a first-time setup there are no meaningful local commits to lose.

### Excluding an entire instance

Click **Exclude instance** on the instance row. All rules from that instance are skipped during export, including any new rules added to Qui later. Click **Include instance** to reverse.

---

## Sync

Sync lets you import automations from someone else's shared repo into your own Qui instance.

### Key concept: the 3-layer merge

When you update an existing rule via Sync, qui-sync does NOT blindly overwrite it. Instead:

1. **Layer 1 — Repo rule:** The shared automation's logic (conditions, tags, limits, name)
2. **Layer 2 — Your live Qui rule:** Your personal settings are preserved:
   - `trackerPattern` (your tracker list)
   - `trackerDomains` (your tracker domains)
   - `intervalSeconds` (your check interval)
   - `freeSpaceSource` (your free space setting)
   - `enabled` (whether you have it turned on)
   - `dryRun` and `notify`
3. **Layer 3 — Your overrides:** Sort order and any per-rule settings from the Plan view

**Result:** You get the maintainer's latest rule logic, but your personal setup is never touched.

### Step-by-step: subscribing to a repo

1. Go to the **Sync** tab.
2. Click **+ Add subscription**.
3. Fill in:
   - **Name:** A short label (e.g. `friends-movies`, `community-tv-rules`)
   - **URL:** The git repo URL (e.g. `https://github.com/someone/qui-automations.git`)
   - **Branch:** Usually `main`
   - **Auth:** Choose based on the repo:
     - **Public repo:** No auth needed
     - **SSH deploy key:** qui-sync generates a keypair. You paste the public key into the repo's Settings → Deploy keys (read-only). The private key stays on disk.
     - **Personal access token:** Paste a GitHub PAT with read access to the repo.
4. Click **Add**.

### Step-by-step: importing rules

1. **Expand** the subscription row.
2. Click **Pull** to fetch the latest from the repo.
3. Select which **Qui instance** to sync to from the dropdown.
4. Click **Plan** to preview.
5. The plan table shows every rule in the repo:
   - **Category filter:** Click `movies` / `tv` / `misc` to filter. Only filtered rules are applied.
   - **Action per rule:**
     - `CREATE` (green) — new rule will be created in Qui, disabled by default
     - `UPDATE ⚠` (amber) — existing Qui rule will be updated (logic only, your trackers preserved)
     - `SKIP` (gray) — rule ignored
   - **Link dropdown:** For each rule, choose "Create new" or link to an existing Qui rule. qui-sync auto-suggests matches by name.
   - **Priority:** Edit the number to control execution order.
   - **Select all / Deselect all:** Bulk-toggle for the filtered rules.
6. Click **Apply selected** to execute:
   - Review the confirmation modal (shows create/update/skip counts)
   - Click Apply
   - New rules appear in Qui as disabled — go to Qui UI to review and enable them
   - Updated rules have new logic but your trackers are untouched

### Auto-sync

After your first manual Apply, you can enable auto-sync per rule:

1. In the Plan table, the **Auto** column shows a checkbox per rule.
2. Check it to enable auto-sync for that rule (disabled until first manual Apply).
3. In Settings, set a **Pull interval** (e.g. every 6 hours).
4. The background worker will automatically:
   - Pull from the repo
   - Apply changes to rules marked Auto
   - Skip new rules (they always need manual review)
   - Log results to container stdout

### Testing with a local repo

Instead of a GitHub URL, you can use a local path for testing:

- **URL:** `/data/repo` (points to your own export folder)
- **Auth:** Public (no auth needed for local paths)

This lets you test the full Sync flow (Pull → Plan → Apply) without pushing to GitHub first.

---

## Backup & Restore

Full 1:1 snapshots of every automation in a Qui instance, including trackers, enabled state, and intervals — no stripping, no placeholders.

### Manual backup

Backup tab → click **Backup now** on an instance row. qui-sync writes a timestamped snapshot to `/data/backups-full/<instance-id>/<timestamp>/` with one JSON file per rule.

### Scheduled backups

Settings tab → **Backup** panel:

- **Schedule** — how often the background worker snapshots every configured instance. Choose off / 6h / 12h / 24h / 3d / 7d. Default is off.
- **Retention days** — snapshots older than this are pruned after the next run. 0 = never expire by age.
- **Keep last N** — minimum number of snapshots kept per instance regardless of age. 0 = no minimum.
- **Gitignored** — keeps backups out of any git commits in the share-repo.

When both retention rules are set, they combine: a snapshot is pruned only when it is older than Retention days AND outside the Keep-last-N floor. This protects the most recent N from age-based eviction even if they're all ancient.

### Restore

Pick any backup from the Backup tab and restore it into the same or a different Qui instance. Existing rules with matching names are updated; new rules are created. All fields restore verbatim.

---

## Common Scenarios

### "I changed a regex in 20 rules and want to share the fix"

1. Edit the JSON files directly in `/data/repo/<folder>/` (use VSCode, find-and-replace).
2. Go to Export tab → Share-repo → Review changes → Push to remote.
3. Others who subscribe will see the changes on their next Pull → Plan → Apply.

### "Someone sent me a PR fixing a bug in my shared rules"

1. Review and merge the PR on GitHub.
2. Export tab → Share-repo shows "1 behind remote".
3. Click **Pull from remote**.
4. Click **Apply to Qui** → Sync tab opens → Plan → Apply.
5. Your Qui now has the fix, with your trackers still intact.

### "I want to try someone's shared Qui automations"

1. Sync tab → Add subscription.
2. URL: the repo URL they shared with you.
3. Auth: Public repo (or SSH/token if private).
4. Pull → Plan → filter to the category you want → Apply.
5. All rules created disabled — review in Qui, set your trackers, enable.

### "I want to use the same automations on two Qui instances"

1. Export from Instance A.
2. Sync tab → add subscription with URL `/data/repo` (local).
3. Plan against Instance B → Apply.
4. Instance B now has the same rules. Your trackers on B are preserved independently.

### "I messed up and want to revert a rule"

Old versions of every changed rule are kept in `/data/backups/` with date suffixes (e.g. `Tag Tier 1-2026-04-18.json`). You can manually copy a backup file back to `/data/repo/<folder>/` and then Apply to Qui.

### "Auto-sync updated a rule I didn't want changed"

1. Go to Sync tab → expand the subscription → uncheck the **Auto** toggle for that rule.
2. In Qui, manually revert the change if needed.
3. The rule stays synced but now requires manual Apply for future updates.
