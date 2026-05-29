# Changelog

## v0.6.2-dev

### Added

- **A tip on the Detect changes screen.** When you capture a customization, a short note now reminds you that it saves everything currently different from the repo. If something should stay in sync with the repo instead, sync first, change only what you want to keep, then detect again.

## v0.6.1-dev

### Fixed: customize now handles grouped conditions

Rules that wrap their conditions in groups (an OR with several AND
branches, the usual shape for delete rules) are now customised
correctly. Adding, editing, or removing a single condition inside a
group is captured as just that one change. Maintainer updates to the
other conditions in the same group, such as a changed torrent-age
threshold, now come through on sync instead of being held back by your
saved copy.

If you customised a grouped rule on the previous build, open it, run
Detect changes once, and Save to re-capture it in the new form.

## v0.6.0-dev

Customize now survives upstream reorganisations cleanly.

### Changed — Customize subscribed rules

Previously, adding a condition near the top of a rule's conditions list
could revert maintainer changes elsewhere in the same list on the next
sync. That happened because each captured edit was tied to its index
position, so anything that shifted indices read as "user changed all of
this." Inserting at the end was the workaround.

The new engine matches edits by the content of each condition, not by
its position. You can add, modify, or remove conditions anywhere — at
the top, in the middle, at the end — and only your specific change is
captured. Maintainer updates to unrelated conditions still apply on
every sync.

A few other improvements that fall out of the rewrite:

- **Smaller customize files.** A single inserted condition is now one
  entry, not a dozen position-replacements.
- **Cleaner Detect-changes view.** The change-list shows the actual
  things you changed (added condition, modified value, removed
  condition) instead of raw position-by-position diffs. The visual
  presentation is intentionally minimal in this release — a richer
  human-readable summary lands in the next update.
- **Better conflict messages.** If you and the maintainer both edited
  the same field between syncs, you get a "both sides changed this"
  notice with the two values shown — instead of your edit silently
  winning.
- **Stricter same-field detection.** A change to (for example) a
  `GREATER_THAN` condition no longer accidentally matches a sibling
  `LESS_THAN` condition on the same field.

### Migration — re-create existing customizations

Customizations saved before this release are in the previous format,
which the new engine cannot read. They stay on disk untouched, but they
will not be applied during sync until you re-create them in the new
format.

**To migrate:** open each customized rule, click **Detect changes** on
the Customize toggle, review the change list, and click Save. The new
format takes over from there.

A one-click migration with a startup banner is planned for the next
release; for now the manual click-through is the safe path.

## v0.5.0-dev

### New — Customize subscribed rules

You can now keep your personal edits to subscribed rules across upstream
updates.

On the Subscribe tab, every linked rule has a new **Customize** toggle.
Turn it on, edit the rule directly in Qui, then click **Detect changes**
to save your edits as a personal customization. qui-sync re-applies them
on every sync — so when the maintainer updates the rule, you get their
changes alongside your personal touches.

Two safety nets:

- **Setup-time check.** When you Plan a fresh subscription against a Qui
  rule that already differs from upstream, qui-sync flags it before the
  first sync overwrites anything.
- **Conflict resolution.** If upstream restructures a rule in a way that
  breaks your customization, you get a clear notice with three choices —
  Re-capture (edit again to match the new shape), Drop (revert to
  upstream), or Close (leave it for later).

Tracker patterns, enabled state, intervals, and other user-owned fields
are still preserved automatically — Customize is only needed for
structural edits like adding/modifying conditions or changing an
action's mode.

### New — Browser URL setting for "Open in Qui"

Settings → Qui connection has a new optional **Browser URL** field. Set
it when the URL qui-sync uses to talk to Qui (often a Docker container
name like `http://qui:7476`) differs from what your browser reaches
Qui at (e.g. `http://192.168.0.10:7476`). Leave it blank if your URL is
already browser-reachable. Used by the "Open in Qui ↗" deep-link in
the Customize flow.

## v0.4.4-dev

### Fixed — Hide backups from GitHub

The "Hide backups from GitHub" toggle now covers per-rule export
backups in addition to full-instance snapshots. Before, only
snapshots from the Backup tab were ignored — the smaller
`<rule>-<date>.json` files Publish creates when a rule is modified
or replaced still ended up pushed on the next Commit. One check now
stops both kinds.

Files already on GitHub stay there until you remove them manually —
the toggle only stops future Commits from including them.

### Changed — Trusted proxies accepts CIDR ranges

`TRUSTED_PROXIES` now accepts CIDR ranges (e.g. `172.19.0.0/24`)
alongside literal IPs. Useful for reverse-proxy deployments where
the proxy runs on a Docker bridge with dynamic container IPs — one
CIDR covers every container instead of listing each one.

## v0.4.3-dev

Newest entries land at the top of each section. v0.4.2 merged
same-day Commits but appended new bullets to the bottom; reading the
file you couldn't tell what you just did versus what you did this
morning. Same-day activity now reads chronologically reverse — the
most recent change is the first line.

### Fixed — Publish

- **Newest at top inside each group.** A later same-day Commit
  touching a different rule now prepends that bullet to the top of
  its group instead of appending after the morning's entries.
- **Same-rule upsert moves to top.** Re-touching a rule later in the
  day (e.g. to add a comment) replaces the old bullet AND moves it
  to the top of the group — so the most recent change to a rule is
  always at the top.
- **New group lands on top.** If your afternoon Commit introduces a
  group that wasn't in the morning section (e.g. you renamed
  something for the first time today), the new `### Renamed` block
  appears above the existing `### Updated` block.

## v0.4.2-dev

Same-day Commits now stack in `CHANGELOG.md` instead of overwriting
each other. This pairs with v0.4.1's per-rule include filter: an
afternoon Commit that publishes a single rule no longer wipes the
morning's entries from the file.

### Fixed — Publish

- **CHANGELOG.md preserves earlier-today entries on a same-day Commit.**
  Before: the second Commit of the day rewrote today's section with
  only its own changes, dropping the morning's bullets. After: bullets
  from earlier commits stay; new entries are merged in (same rule
  appearing twice = later wins, comment updates).
- The global "Edit note" still wins when you type a new one. A blank
  note on a follow-up Commit leaves the existing note alone instead of
  blanking it out.
- Hand-edited heading suffixes (e.g. `## 2026-05-17 (release)`) and
  manual bullets you added by hand survive the merge.

### Notes

Git history of `CHANGELOG.md` was always intact (every export creates
its own commit and the file's prior state is in `git log`). The fix is
in the working-copy file you actually look at on GitHub — now reads
"everything I pushed today" instead of "only the last batch".

## v0.4.1-dev

Publish-side ergonomics: pick which changes go up this round, optionally
attach a per-change reason, and get a real commit message on GitHub.

### New — Publish

- **Uncheck rows to leave them for next time.** Every line in the
  Preview list (Added / Updated / Renamed / Archived / Moved) now has
  a checkbox. Uncheck a row and Commit will skip it — the file on disk
  stays as-is, your state stays as-is, and the change comes back into
  the next Preview so you can revisit it. Handy when a rule is still
  WIP or you want to push one rename in its own commit. Each section
  also has an **Include all / Exclude all** toggle.

- **Add an optional reason per change.** Click the 💬 button on a row
  to open an inline reason field. What you type lands two places:
  - The CHANGELOG.md bullet as an italic suffix
    (`"Foo" → "Foo (HD)" — *added HD suffix for Sonarr 4K profile*`).
  - The git commit message body, indented under the rule's bullet —
    so anyone reading the GitHub commit gets the *why*, not just the *what*.
  Empty reasons render nothing — they're entirely optional. The global
  "Edit note" field is unchanged: that's still for *why this batch as
  a whole*; the per-rule reasons are for *why this specific change*.

- **Real git commit messages.** Was: every export produced a commit
  with subject `qui-sync export`. Now: the subject describes what
  actually changed. A single rename becomes
  `qui-sync: rename movies/Foo → Foo (HD)`. A batch of renames becomes
  `qui-sync export — renamed 5 rules`. A mixed batch becomes
  `qui-sync export — 12 changes`. The commit body lists every rule
  grouped by section, with any per-rule reasons attached.

### Notes

- Selection state survives between Previews of the same batch — you
  can adjust the list, hit Preview again to double-check, then Commit
  without re-doing your checkboxes or typed reasons.
- Excluded rows fade with a strikethrough so the context stays visible.
  You still see *what* you chose to skip, not just *that* you skipped
  something.

## v0.4.0-dev

Bigger Subscribe-side overhaul plus a handful of paper-cut fixes.
The headline change is that auto-sync can now react when a maintainer
removes or renames a rule — previously it only handled updates.

### New — Subscribe

- **Auto-deactivate when the maintainer removes a rule.** If you have
  **Auto** on for a rule and the maintainer pulls it from their repo,
  qui-sync flips that rule's on/off switch to *off* in your Qui on the
  next auto-sync. The rule itself stays in place — you can re-enable it
  manually if you disagree. Rules without Auto land in a new
  **Removed by maintainer** section with a per-row **Turn off now**
  button so the choice is yours.

- **Auto-create new rules as disabled.** When the maintainer adds a
  brand-new rule and Auto-sync is on, qui-sync now creates it in your
  Qui automatically — but turned off. Review it, then enable it in Qui
  when you're ready. qui-sync never enables a rule on its own.

- **Maintainer renames are handled smoothly.** Previously a rename
  looked like a "delete + create" pair; now your linked Qui rule just
  gets the new name (your trackers, intervals, and on/off state are
  preserved as always).

- **Excluded by you.** Uncheck a rule on the Plan tab — or pick
  "Skip this rule" from the dropdown — and it moves to its own
  **Excluded by you** section below the main list. Click **Include
  again** to bring it back. Your choices are saved immediately and
  survive every Pull.

- **Pure subscribers can pick a Qui instance.** Previously the
  "Apply to which Qui instance?" dropdown only listed instances
  configured on the Publish tab — useless if you only consume rules,
  never publish. Now it lists every instance on your Qui server.

- **Test connection works before Save.** The Test button now probes
  whatever's in the URL + API key fields right now, not the saved
  values. Verify a fresh Qui connection without committing it first.

- **Disconnect button.** Settings → Qui connection has a red
  **Disconnect** button that clears the saved URL + API key. Reset
  is renamed to **Discard changes** to match what it actually does
  (revert form edits, not remove the connection).

- **Friendlier subscription name.** The Name field in Add subscription
  accepts free-form text (e.g. *Prophet's Movies*). qui-sync derives
  a safe identifier (*prophets-movies*) and shows it live below the
  field so you see exactly what's stored.

### New — Publish

- **Hide archive folder from GitHub.** New checkbox in the Push to
  GitHub panel: when on, qui-sync adds `archive/` to `.gitignore` so
  the soft-delete history of removed rules stays local instead of
  going public.

- **Rename cleanup.** Renaming a rule in Qui no longer leaves the old
  filename orphaned in the share-repo. Next Commit export removes the
  old file (timestamped backup goes to `/data/backups/` first).

### Fixed

- Add export instance modal opens from the Publish tab regardless of
  which tab you were on. Previously the modal markup lived inside the
  Settings section, so clicking the button on Publish appeared to do
  nothing.

- Test connection now reports the count of instances on your Qui
  server, not the count you've added as export instances locally.
  A fresh Qui with three qBit instances no longer reports "0 found".

### Internal

- Auto-sync no longer silently overwrites your private Qui rules when
  a maintainer pushes a rule with the same name. Brand-new repo rules
  go through `create_new` only; match-by-name update_existing requires
  state (you must have applied that link at least once).

## v0.3.0

User accounts, multiple backup schedules, and a simpler Subscribe flow.
Tabs were renamed to make the actions clearer.

### New

- **First-run setup.** The first time you open qui-sync after updating,
  you'll create an admin user. Log in once per browser after that.
  LAN clients still bypass by default.

- **Multiple backup schedules.** Add as many as you want — e.g. "TV
  nightly" every day at 03:00 plus "Weekly full" every Sunday on every
  instance. Each has its own name, instances, schedule, and retention.

- **Simpler Subscribe.**
  - Pick the Qui instance once when you add the subscription —
    Plan and Apply use it without re-asking.
  - **Look up what's in this repo** finds the available folders for
    you so you don't have to type a category name.
  - **Match by name** auto-links every repo rule to your matching Qui
    rule (case-insensitive). One click for large imports.
  - **Set all to Create new** if you'd rather start fresh.

- **Migration restore.** Restoring a backup to a *different* Qui
  instance is now flagged clearly so you know it's a migration, not a
  same-instance recovery.

- **Tabs renamed.** Export → **Publish**. Sync → **Subscribe**.
  Backup is the default landing tab.

### Changed

- **Backups don't catch up after a restart.** If the container was down
  at the scheduled time, that slot is skipped (standard cron). Earlier
  versions ran the missed slot immediately on the next start.

- **Old config upgrades automatically.** Your existing `backup:` block
  becomes the first entry in the new schedule list with your retention
  preserved. Migrated schedules start disabled — open the Backup tab,
  edit "Default backup", and set when it should run.

### Fixed

- AutoSync toggle now persists when turned off (was silently ignored).
- Plan view no longer shows "Create new" while an Action is set to
  Update.
- Backup list no longer offers interrupted snapshots as restorable.
- Saving two different settings at the same time doesn't lose either
  change anymore.
