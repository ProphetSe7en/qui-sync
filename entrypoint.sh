#!/bin/sh
# qui-sync entrypoint — honours PUID/PGID/UMASK so volumes end up owned
# by the host user instead of container-root.
#
# Env vars (all optional):
#   PUID   user id  (default 99  — Unraid nobody)
#   PGID   group id (default 100 — Unraid users)
#   UMASK  file creation mask (default 002)

set -e

PUID=${PUID:-99}
PGID=${PGID:-100}
UMASK=${UMASK:-002}

umask "$UMASK"

# Reconcile the baked-in nobody:users uid/gid with what the host wants.
# Only touches /etc/passwd|group entries — no chown on the image.
current_uid=$(id -u nobody 2>/dev/null || echo 99)
current_gid=$(getent group users | cut -d: -f3 2>/dev/null || echo 100)

if [ "$current_gid" != "$PGID" ]; then
  # Remove any existing group with the target GID so we can reassign cleanly.
  existing_group=$(getent group "$PGID" | cut -d: -f1 || true)
  if [ -n "$existing_group" ] && [ "$existing_group" != "users" ]; then
    delgroup "$existing_group" 2>/dev/null || true
  fi
  sed -i "s/^users:x:${current_gid}:/users:x:${PGID}:/" /etc/group
fi

if [ "$current_uid" != "$PUID" ]; then
  existing_user=$(getent passwd "$PUID" | cut -d: -f1 || true)
  if [ -n "$existing_user" ] && [ "$existing_user" != "nobody" ]; then
    deluser "$existing_user" 2>/dev/null || true
  fi
  sed -i "s/^nobody:x:${current_uid}:[0-9]*:/nobody:x:${PUID}:${PGID}:/" /etc/passwd
fi

# Ensure volumes are writable by the target user. Best-effort — skip if the
# dir doesn't exist (user may only mount one of them).
for d in /config /data; do
  if [ -d "$d" ]; then
    chown -R "$PUID:$PGID" "$d" 2>/dev/null || true
  fi
done

echo "[entrypoint] running as uid=$PUID gid=$PGID umask=$UMASK"

# Drop privileges and exec the server.
exec su-exec "$PUID:$PGID" /usr/local/bin/qui-sync-server "$@"
