#!/bin/bash
# Installiert wanderwelt-rollout samt Timer auf dem Koordinator-Server (einmalig
# und bei Änderungen am Skript).
set -euo pipefail
host=${WW_ROLLOUT_HOST:-mini}
here=$(cd "$(dirname "$0")" && pwd)
for f in wanderwelt-rollout wanderwelt-rollout.service wanderwelt-rollout.timer; do
	sed 's/\r$//' "$here/$f" | ssh "$host" "cat > /tmp/$f"
done
ssh "$host" 'set -e
install -m 0755 -o root -g root /tmp/wanderwelt-rollout /usr/local/sbin/wanderwelt-rollout
install -m 0644 /tmp/wanderwelt-rollout.service /tmp/wanderwelt-rollout.timer /etc/systemd/system/
rm -f /tmp/wanderwelt-rollout /tmp/wanderwelt-rollout.service /tmp/wanderwelt-rollout.timer
install -d -m 0700 /var/lib/wanderwelt-rollout /var/lib/wanderwelt-rollout/queue
systemctl daemon-reload
systemctl enable --now wanderwelt-rollout.timer
wanderwelt-rollout status'
