#!/bin/sh
# entrypoint.sh — fix upload-volume ownership then drop to appuser.
#
# Named volumes are initialised by Docker from the image directory permissions
# on first use, so fresh deployments work out of the box.  Existing volumes
# that were created while the container ran as root (uid=0) need a one-time
# chown; this entrypoint handles that transparently on startup.
#
# With no-new-privileges:true in the compose security_opt, we cannot gain
# privileges via setuid binaries, but we CAN drop from root to appuser via
# su-exec (which calls setuid()/setgid() on the current process before exec —
# a privilege drop, not a gain).
set -e

if [ "$(id -u)" = "0" ]; then
    # Fix ownership of the uploads volume so appuser (uid=1001) can write.
    # Idempotent: already-correct dirs cost one lstat per entry.
    chown -R appuser:appuser /data/uploads 2>/dev/null || true
    exec su-exec appuser "$@"
fi

# Already running as non-root (e.g. in a dev override); exec directly.
exec "$@"
