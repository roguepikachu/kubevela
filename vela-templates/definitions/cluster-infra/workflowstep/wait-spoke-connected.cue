import (
	"vela/kube"
	"vela/builtin"
)

"wait-spoke-connected": {
	type: "workflow-step"
	annotations: {
		"category": "Cluster Infrastructure"
	}
	labels: {}
	description: "Hold the workflow until the SpokeCluster this Application provisions is reachable through the cluster gateway."
}

template: {
	// Read on the hub: no cluster set, so the read hits the control plane the workflow runs on.
	spoke: kube.#Read & {
		$params: value: {
			apiVersion: "core.oam.dev/v1beta1"
			kind:       "SpokeCluster"
			metadata: {
				name:      parameter.name
				namespace: parameter.namespace
			}
		}
	}

	// A SpokeCluster that has not been reconciled yet carries no conditions at all.
	_conditions: *[] | [...{...}]
	if spoke.$returns.value.status.conditions != _|_ {
		_conditions: spoke.$returns.value.status.conditions
	}
	_connected: [for c in _conditions if c.type == "Connected" && c.status == "True" {c}]

	wait: builtin.#ConditionalWait & {
		$params: {
			continue: len(_connected) > 0
			message:  "waiting for SpokeCluster \(parameter.namespace)/\(parameter.name) to be Connected"
		}
	}

	parameter: {
		// +usage=Name of the SpokeCluster to wait for
		name: string
		// +usage=Namespace of the SpokeCluster, defaults to the Application's namespace
		namespace: *context.namespace | string
	}
}
