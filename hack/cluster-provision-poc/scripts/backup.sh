#!/usr/bin/env bash
# Checkpoint backup for the provisioning POC. Safe to run at any time, including while a
# subagent is editing the worktree: it never commits or stashes, it only pushes what is
# already committed and snapshots the notes and scripts.
#   1. Push branch poc/cluster-provision to origin (roguepikachu/kubevela), the off-machine copy.
#   2. Tar the vault folder "Cluster KEP/Provisioning POC" and this POC folder (minus backups and
#      out/*.log) into backups/<utc-timestamp>.tgz on the host mount.
#   3. Print what is committed, what is not, and where the newest backup is.
set -uo pipefail
WORKTREE=/workspaces/oam/opensource/kubevela.worktrees/cluster-provision
POC=/workspaces/oam/cluster-provision-poc
VAULT_DIR="/home/node/obsidian/ayush-gw/Cluster KEP/Provisioning POC"
mkdir -p "$POC/backups"
ts=$(date -u +%Y%m%dT%H%M%SZ)

echo "== git"
git -C "$WORKTREE" log --oneline -1
n=$(git -C "$WORKTREE" status --short | wc -l | tr -d ' ')
echo "uncommitted files in worktree: $n (left alone; the running task owns them)"
if git -C "$WORKTREE" push -q -u origin poc/cluster-provision 2>"$POC/backups/push-$ts.err"; then
  echo "pushed poc/cluster-provision to origin ($(git -C "$WORKTREE" rev-parse --short HEAD))"; rm -f "$POC/backups/push-$ts.err"
else
  echo "push FAILED, see $POC/backups/push-$ts.err"
fi

echo "== snapshot"
tar -czf "$POC/backups/$ts.tgz" \
  -C "$(dirname "$VAULT_DIR")" "$(basename "$VAULT_DIR")" \
  -C /workspaces/oam cluster-provision-poc/scripts cluster-provision-poc/defs cluster-provision-poc/manifests 2>/dev/null
ls -la "$POC/backups/$ts.tgz" | awk '{print $5, $9}'
ls -t "$POC/backups"/*.tgz | tail -n +6 | xargs -r rm -f   # keep the newest five
echo "newest backups:"; ls -t "$POC/backups"/*.tgz | head -3
