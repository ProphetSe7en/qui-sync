# Changelog

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
