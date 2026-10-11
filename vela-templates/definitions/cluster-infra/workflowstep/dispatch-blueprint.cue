import (
	"vela/kube"
	"vela/builtin"
)

"dispatch-blueprint": {
	type: "workflow-step"
	annotations: {
		"category": "Cluster Infrastructure"
	}
	labels: {}
	description: "Ship a blueprint revision, its plane revisions and the ComponentDefinitions they use to a spoke, and hand the spoke's own vela-cluster-core the Cluster object to render."
}

template: {
	// What the spoke receives from each hub object: apiVersion, kind, name, namespace, labels
	// and spec. Status, resourceVersion, UID, owner references and managed fields are hub-side
	// state that must not be applied onto another cluster. A ComponentDefinition keeps its
	// healthPolicy and customStatus under spec.status, so spec alone carries them.
	#Trimmed: {
		_in: {...}
		apiVersion: _in.apiVersion
		kind:       _in.kind
		metadata: {
			name:      _in.metadata.name
			namespace: _in.metadata.namespace
			if _in.metadata.labels != _|_ {
				labels: _in.metadata.labels
			}
		}
		spec: _in.spec
	}

	// Hub-side reads: no cluster set, so they hit the control plane the workflow runs on.
	// cuex executes the #do fields in declaration order, one per pass, and re-evaluates the
	// template after each, so the comprehensions below fill in once the read before them returns.
	readBlueprint: kube.#Read & {
		$params: value: {
			apiVersion: "core.oam.dev/v1beta1"
			kind:       "ClusterBlueprintRevision"
			metadata: {
				name:      parameter.revision
				namespace: parameter.namespace
			}
		}
	}

	readPlanes: {
		for p in readBlueprint.$returns.value.spec.planes {
			"\(p.planeRevision)": kube.#Read & {
				$params: value: {
					apiVersion: "core.oam.dev/v1beta1"
					kind:       "ClusterPlaneRevision"
					metadata: {
						name:      p.planeRevision
						namespace: parameter.namespace
					}
				}
			}
		}
	}

	readDefinitions: {
		for name in parameter.definitions {
			"\(name)": kube.#Read & {
				$params: value: {
					apiVersion: "core.oam.dev/v1beta1"
					kind:       "ComponentDefinition"
					metadata: {
						name:      name
						namespace: parameter.namespace
					}
				}
			}
		}
	}

	// Spoke-side applies, through the cluster gateway. The revisions and definitions land before
	// the Cluster so the spoke controller never sees a Cluster whose revisions are missing.
	applyBlueprint: kube.#Apply & {
		$params: {
			cluster: parameter.cluster
			value:   #Trimmed & {_in: readBlueprint.$returns.value}
		}
	}

	applyPlanes: {
		for name, r in readPlanes {
			"\(name)": kube.#Apply & {
				$params: {
					cluster: parameter.cluster
					value:   #Trimmed & {_in: r.$returns.value}
				}
			}
		}
	}

	applyDefinitions: {
		for name, r in readDefinitions {
			"\(name)": kube.#Apply & {
				$params: {
					cluster: parameter.cluster
					value:   #Trimmed & {_in: r.$returns.value}
				}
			}
		}
	}

	// Every lifecycle stage points at the one revision being shipped; the spoke controller
	// picks the planes per stage from the revision by their phase.
	applyCluster: kube.#Apply & {
		$params: {
			cluster: parameter.cluster
			value: {
				apiVersion: "core.oam.dev/v1beta1"
				kind:       "Cluster"
				metadata: {
					name:      parameter.clusterName
					namespace: parameter.namespace
				}
				spec: {
					clusterInit: blueprintRef: name:       parameter.revision
					planeProvisioning: blueprintRef: name: parameter.revision
					healthValidation: blueprintRef: name:  parameter.revision
				}
			}
		}
	}

	// The step is done once the spoke acknowledged the revision by moving the Cluster out of
	// Pending. Readiness is reported separately by the hub's status pull.
	spokeCluster: kube.#Read & {
		$params: {
			cluster: parameter.cluster
			value: {
				apiVersion: "core.oam.dev/v1beta1"
				kind:       "Cluster"
				metadata: {
					name:      parameter.clusterName
					namespace: parameter.namespace
				}
			}
		}
	}

	_phase: *"" | string
	if spokeCluster.$returns.value.status.phase != _|_ {
		_phase: spokeCluster.$returns.value.status.phase
	}

	wait: builtin.#ConditionalWait & {
		$params: {
			continue: _phase != "" && _phase != "Pending"
			message:  "waiting for the spoke to acknowledge revision \(parameter.revision)"
		}
	}

	parameter: {
		// +usage=The spoke to dispatch to, by its cluster-gateway name (the SpokeCluster name)
		cluster: string
		// +usage=Namespace of the revisions on the hub and of every object on the spoke, defaults to the Application's namespace
		namespace: *context.namespace | string
		// +usage=Name of the ClusterBlueprintRevision to ship
		revision: string
		// +usage=Names of the ComponentDefinitions the planes use that the spoke's vela-core does not ship
		definitions: *[] | [...string]
		// +usage=Name of the Cluster object on the spoke, defaults to cluster
		clusterName: *cluster | string
	}
}
