#!/usr/bin/env bash
# Scenario 4: a labels trait on the plane component reaches the Application and the CAPI Cluster. KNOWN BUG: the merge patch below replaces the component list and is rejected; rewrite as a JSON patch adding /spec/components/0/traits before rerunning.
source /workspaces/oam/cluster-provision-poc/scripts/env.sh >/dev/null 2>&1; unset AWS_PROFILE
H="kubectl --kubeconfig /tmp/cc-chub1.kubeconfig --request-timeout=15s"
cat > /workspaces/oam/cluster-provision-poc/out/trait-patch.json <<'JSON'
{"spec":{"components":[{"name":"cluster","traits":[{"type":"labels","properties":{"poc.cluster.core.oam.dev/trait":"applied"}}]}]}}
JSON
comp=$($H get clusterplanes.core.oam.dev eks-capi-foundation -n vela-system -o jsonpath='{.spec.components[0].name}')
echo "plane component: $comp"; [ "$comp" = cluster ] || { echo "unexpected component name, adjust the patch"; exit 2; }
$H patch clusterplanes.core.oam.dev eks-capi-foundation -n vela-system --type merge --patch-file /workspaces/oam/cluster-provision-poc/out/trait-patch.json >/dev/null
echo "$(date -u +%H:%M:%S) trait patched into the plane"; t0=$(date +%s)
for i in $(seq 1 36); do
  sleep 10
  tr=$($H get app sc-cpspoke1 -n vela-system -o jsonpath='{.spec.components[?(@.name=="foundation-cluster")].traits[0].type}' 2>/dev/null)
  lbl=$($H get clusters.cluster.x-k8s.io cpspoke1 -n vela-system -o jsonpath='{.metadata.labels.poc\.cluster\.core\.oam\.dev/trait}' 2>/dev/null)
  steps=$($H get app sc-cpspoke1 -n vela-system -o jsonpath='{range .status.workflow.steps[*]}{.name}:{.phase} {end}')
  infra=$($H get spokecluster cpspoke1 -n vela-system -o jsonpath='{.status.conditions[?(@.type=="InfraProvisioned")].status}')
  echo "$(date -u +%H:%M:%S) app trait=$tr capi label=$lbl InfraProvisioned=$infra | $steps"
  if [ "$tr" = labels ] && [ "$lbl" = applied ] && echo "$steps" | grep -q 'platform:succeeded' && [ "$infra" = True ]; then echo "SCENARIO 4 PASS after $(( $(date +%s) - t0 )) s"; exit 0; fi
  echo "$steps" | grep -q failed && { echo "STEP FAILED"; $H get app sc-cpspoke1 -n vela-system -o jsonpath='{range .status.workflow.steps[?(@.phase=="failed")]}{.name}: {.message}{"\n"}{end}' | cut -c1-600; exit 1; }
done
echo TIMEOUT; exit 2
