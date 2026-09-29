# Logger revamp exercise

Helpers for running KubeVela locally while working on the logging exercise.
The full instructions are on the exercise page you were given.

| File | What it does |
|---|---|
| `k3d-up.sh` | Creates a k3d cluster, installs the chart with the controller scaled to 0, and points the admission webhooks at your machine |
| `launch.json.example` | VS Code launch config (copy to `.vscode/launch.json`); GoLand settings in the comment at the top |
| `build-and-deploy.sh` | Builds the controller image and runs it inside the cluster with the chart's own webhooks |
| `samples/` | Applications to exercise the controller and the webhooks |

The scripts write and use their own kubeconfig, `~/.kube/vela-interview`; run
`export KUBECONFIG=~/.kube/vela-interview` before using kubectl on the cluster.

Environment variables: `CLUSTER` (default `vela-interview`), `VELA_KUBECONFIG`
(default `~/.kube/vela-interview`), `WEBHOOK_PORT`
(default `9445`), `WEBHOOK_HOST` (overrides the address the cluster uses to reach
your webhook server), `FULL_BUILD=true` (build the image with the root
`Dockerfile` instead of compiling on the host).
