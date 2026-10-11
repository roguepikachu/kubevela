#!/usr/bin/env bash
source /workspaces/oam/cluster-provision-poc/scripts/env.sh >/dev/null 2>&1; unset AWS_PROFILE
H="kubectl --kubeconfig /tmp/cc-chub1.kubeconfig --request-timeout=15s"
for i in $(seq 1 45); do
  apps=$($H get app -n vela-system -l spokecluster.core.oam.dev/name=cpspoke1 -o jsonpath='{range .items[*]}{.metadata.name}={.status.status} {end}' 2>&1)
  steps=$($H get app sc-cpspoke1 -n vela-system -o jsonpath='{range .status.workflow.steps[*]}{.name}:{.phase} {end}' 2>/dev/null)
  conds=$($H get spokecluster cpspoke1 -n vela-system -o jsonpath='{range .status.conditions[*]}{.type}={.status}/{.reason} {end}' 2>&1)
  echo "$(date -u +%H:%M:%S) apps: $apps | steps: $steps"
  echo "    conds: $conds"
  err=$($H logs deploy/vela-core-cluster-core -n vela-system --since=25s 2>/dev/null | grep -E '"level":"error"|^E[0-9]{4}' | tail -2 | cut -c1-300); [ -n "$err" ] && echo "    ERR: $err"
  fails=$($H get app sc-cpspoke1 -n vela-system -o jsonpath='{range .status.workflow.steps[?(@.phase=="failed")]}{.name}:{.message} {end}' 2>/dev/null); [ -n "$fails" ] && { echo "STEP FAILED: $fails"; $H get app sc-cpspoke1 -n vela-system -o jsonpath='{.status.conditions}' | cut -c1-600; exit 1; }
  legacy=$(echo "$apps" | tr ' ' '\n' | grep -c -E 'sc-cpspoke1-(infra|bootstrap|dispatch)')
  if [ "$legacy" -eq 0 ] && echo "$conds" | grep -q 'Dispatched=True' && echo "$conds" | grep -q 'SpokeReady=True' && echo "$conds" | grep -q 'Bootstrapped=True' && echo "$conds" | grep -q 'InfraProvisioned=True'; then echo "MIGRATED"; exit 0; fi
  sleep 20
done
echo TIMEOUT; exit 2
