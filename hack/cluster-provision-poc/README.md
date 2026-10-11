# Cluster provisioning POC scripts

Bash scripts used to prove the Cluster KEP provisioning slices on real AWS (scratch hub `chub1`,
spoke `cpspoke1`). They are a lab notebook, not a product: hard-wired to one account, one VPC and
one operator, and they expect the kubeconfigs and the AWS session the comments name. Read `env.sh`
first and adjust it before running anything.

Order, by slice:

| Slice | Scripts | What they prove |
| --- | --- | --- |
| 1 provision | `02-iam.sh`, `01-image.sh`, `03-capi.sh`, `04-hub.sh`, `05-defs.sh`, `06-spoke.sh` | an EKS cluster from a SpokeCluster in mode provision, through CAPI |
| 1b harmony | `07-mode-harmony.sh` | connect, adopt and provision on one cluster, release and re-adopt |
| 2a bootstrap | `08-chart.sh`, `09-bootstrap-defs.sh`, `10-bootstrap.sh`, `11-bootstrap-proof.sh`, `12-spoke-flux-identity.sh` | Flux and vela-core on the spoke from a plane, repair and version bump |
| 2b dispatch | `16-spoke-access.sh`, `13-planes-clusterinit.sh`, `14-dispatch.sh`, `15-dispatch-proof.sh` | blueprint revisions shipped to the spoke, rendered by the spoke's own vela-core, hub-down self-heal |
| 2c one Application | `22-oam-defs.sh`, `25-watch-one-app.sh`, `23-scenario4-trait.sh` (patch needs fixing), `24-scenario6-release-restore.sh` | one authorable Application per SpokeCluster with CUE workflow steps |
| cleanup | `99-teardown.sh --yes` | everything above removed in dependency order; follow with a tag sweep |

`backup.sh` pushes the branch; `00-status.sh` prints the live state. Logs and generated manifests
were written to an `out/` directory that is not committed.
