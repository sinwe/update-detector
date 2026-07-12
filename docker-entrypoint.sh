#!/bin/sh
# STATE_DIR (see docker-compose.yml) is a bind mount, not a named volume --
# unlike a named volume, Docker never auto-chowns a bind-mounted host
# directory to match the image, so a freshly created host path (or one
# `docker compose` auto-created because it didn't exist yet) starts out
# root:root and the non-root update-detector user below can't write to it.
# Fix that here, once, before dropping privilege, so nobody deploying this
# has to remember a manual chown.
set -eu

mkdir -p /var/lib/update-detector
chown -R update-detector:update-detector /var/lib/update-detector

exec runuser -u update-detector -- /usr/local/bin/update-detector
