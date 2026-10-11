#!/usr/bin/env bash
# Scenario 6: clearing spec.blueprintRef removes the platform step and the Dispatched condition; spoke objects stay. Then restore it.
source /workspaces/oam/cluster-provision-poc/scripts/env.sh >/dev/null 2>&1; unset AWS_PROFILE
H="kubectl --kubeconfig /tmp/cc-chub1.kubeconfig --request-timeout=15s"; S="kubectl --kubeconfig /tmp/cp-spoke.kubeconfig --request-timeout=15s"
$H patch spokecluster cpspoke1 -n vela-system --type json -p '[{"op":"remove","path":"/spec/blueprintRef"}]' >/dev/null && echo "$(date -u +%H:%M:%S) blueprintRef cleared"
for i in $(seq 1 18); do
  sleep 10
  steps=$($H get app sc-cpspoke1 -n vela-system -o jsonpath='{range .spec.workflow.steps[*]}{.name} {end}')
  disp=$($H get spokecluster cpspoke1 -n vela-system -o jsonpath='{.status.conditions[?(@.type=="Dispatched")].status}')
  rev=$($H get spokecluster cpspoke1 -n vela-system -o jsonpath='{.status.dispatchedRevision}')
  cl=$($S get clusters.core.oam.dev cpspoke1 -n vela-system -o jsonpath='{.status.phase}' 2>/dev/null)
  echo "$(date -u +%H:%M:%S) steps=[$steps] Dispatched=${disp:-absent} rev=${rev:-empty} spoke Cluster=${cl:-gone}"
  if ! echo "$steps" | grep -q platform && [ -z "$disp" ] && [ "$cl" = Ready ]; then echo "SCENARIO 6 PASS (release half)"; break; fi
done
$H patch spokecluster cpspoke1 -n vela-system --type merge -p '{"spec":{"blueprintRef":{"name":"cpspoke1-platform"}}}' >/dev/null && echo "$(date -u +%H:%M:%S) blueprintRef restored"
for i in $(seq 1 24); do
  sleep 10
  c=$($H get spokecluster cpspoke1 -n vela-system -o jsonpath='{range .status.conditions[*]}{.type}={.status} {end}')
  echo "$c" | grep -q 'Dispatched=True' && echo "$c" | grep -q 'SpokeReady=True' && { echo "SCENARIO 6 PASS (restore half) at $(date -u +%H:%M:%S)"; exit 0; }
done
echo "restore did not complete"; exit 1
