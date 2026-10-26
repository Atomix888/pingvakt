#!/bin/sh
# Startar servern som den oprivilegierade användaren pingvakt. Körs containern
# som root ges datamappen rätt ägare först (behövs när ./data monteras från värden).
set -e
if [ "$(id -u)" = 0 ]; then
  chown -R pingvakt:pingvakt "${PINGVAKT_DATA:-/data}" 2>/dev/null || true
  exec su-exec pingvakt pingvakt-server "$@"
fi
exec pingvakt-server "$@"
