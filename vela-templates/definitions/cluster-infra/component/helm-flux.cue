// Derived from the kubevela catalog fluxcd addon's helm definition
// (https://github.com/kubevela/catalog, addons/fluxcd/definitions/helm-release-def.cue,
// Apache-2.0). Trimmed to the HelmRepository source, and the API versions bumped to the
// Flux v2 GA groups (source v1, helm v2) that Flux v2.9.6 serves; the catalog copy still
// emits v1beta2 and v2beta1, which that Flux no longer knows.
"helm-flux": {
	type:        "component"
	annotations: {}
	labels: {}
	description: "Install a Helm chart on the target cluster as a Flux v2 HelmRelease from a HelmRepository. Plane component for the SpokeCluster bootstrap."
	attributes: {
		workload: type: "autodetects.core.oam.dev"
		status: {
			customStatus: #"""
				message: *"waiting for the HelmRelease to report Ready" | string
				if context.output.status != _|_ if context.output.status.conditions != _|_ {
					for c in context.output.status.conditions if c.type == "Ready" if c.message != _|_ {
						message: c.message
					}
				}
				"""#
			healthPolicy: #"""
				isHealth: *false | bool
				if context.output.status != _|_ if context.output.status.conditions != _|_ {
					for c in context.output.status.conditions if c.type == "Ready" && c.status == "True" {
						isHealth: true
					}
				}
				"""#
		}
	}
}

template: {
	outputs: repo: {
		apiVersion: "source.toolkit.fluxcd.io/v1"
		kind:       "HelmRepository"
		metadata: {
			name:      context.name
			namespace: context.namespace
		}
		spec: {
			url:      parameter.url
			interval: parameter.pullInterval
			if parameter.repoType == "oci" {
				type: "oci"
			}
			if parameter.provider != _|_ {
				provider: parameter.provider
			}
			if parameter.secretRef != _|_ {
				secretRef: name: parameter.secretRef
			}
		}
	}

	// The release is the primary output: its Ready condition is the health signal.
	output: {
		apiVersion: "helm.toolkit.fluxcd.io/v2"
		kind:       "HelmRelease"
		metadata: {
			name:      context.name
			namespace: context.namespace
		}
		spec: {
			interval: parameter.interval
			timeout:  parameter.installTimeout
			if parameter.releaseName != _|_ {
				releaseName: parameter.releaseName
			}
			if parameter.targetNamespace != _|_ {
				targetNamespace: parameter.targetNamespace
			}
			install: {
				createNamespace: true
				crds:            "CreateReplace"
				remediation: retries: parameter.retries
			}
			upgrade: {
				crds: "CreateReplace"
				remediation: retries: parameter.retries
			}
			chart: spec: {
				chart:    parameter.chart
				version:  parameter.version
				interval: parameter.pullInterval
				sourceRef: {
					kind:      "HelmRepository"
					name:      context.name
					namespace: context.namespace
				}
			}
			if parameter.values != _|_ {
				values: parameter.values
			}
		}
	}

	parameter: {
		// +usage=Helm repository URL: https:// for an index-based repository, oci:// for an OCI registry
		url: string
		// +usage=Repository type; oci sets HelmRepository spec.type so Flux pulls the chart from a registry
		repoType: *"helm" | "oci"
		// +usage=Flux OIDC provider for registry authentication without a secret; aws uses the node or pod identity
		provider?: "generic" | "aws" | "azure" | "gcp"
		// +usage=Name of a Secret in the same namespace holding repository credentials
		secretRef?: string
		// +usage=Chart name in the repository
		chart: string
		// +usage=Chart version or semver range, default any
		version: *"*" | string
		// +usage=Helm release name, default the component name
		releaseName?: string
		// +usage=Namespace the chart is installed into, created when missing; default the component namespace
		targetNamespace?: string
		// +usage=Chart values
		values?: #nestedmap
		// +usage=Interval at which Flux reconciles the release, default 1m
		interval: *"1m" | string
		// +usage=Interval at which Flux checks the repository and the chart for updates, default 5m
		pullInterval: *"5m" | string
		// +usage=Timeout for the helm install and upgrade operations, default 10m
		installTimeout: *"10m" | string
		// +usage=Remediation retries when an install or upgrade fails, default 3
		retries: *3 | int
	}

	#nestedmap: {
		...
	}
}
