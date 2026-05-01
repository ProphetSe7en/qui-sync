# Changelog

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
