#!/bin/sh
#
# Fix ownership of bind-mounted volumes, then drop privileges.
#
# Docker (and docker compose) create missing bind-mount directories (e.g.
# ./data and ./geo from docker-compose.yml) as root:root, while the checker
# runs as appuser (uid/gid 1000). Without this step a fresh deployment
# crash-loops with "permission denied" when geo databases are downloaded
# or state files are persisted.
#
# When the container starts as root: chown the well-known mount points and
# re-exec as appuser via su-exec. When it starts unprivileged (--user /
# securityContext): run as-is; the application degrades gracefully (geo
# download and store persistence become best-effort with warnings).

set -e

APP_UID=1000
APP_GID=1000

if [ "$(id -u)" = "0" ]; then
    for dir in /app/data /app/geo; do
        if [ -d "$dir" ]; then
            owner="$(stat -c %u "$dir" 2>/dev/null || echo unknown)"
            if [ "$owner" != "$APP_UID" ]; then
                echo "entrypoint: fixing ownership of ${dir} (was uid=${owner})"
                if ! chown -R "$APP_UID:$APP_GID" "$dir"; then
                    echo "entrypoint: WARNING: cannot chown ${dir} (read-only mount?); continuing" >&2
                fi
            fi
        fi
    done
    exec su-exec "$APP_UID:$APP_GID" "$@"
fi

exec "$@"
