"eks-capi": {
	type: "component"
	annotations: {"definition.oam.dev/scope": "cluster"}
	labels: {}
	description: "Provision an EKS cluster through Cluster API Provider AWS. Substrate component for SpokeCluster mode: provision."
	attributes: {
		workload: type: "autodetects.core.oam.dev"
		status: {
			customStatus: #"""
				ready: *false | bool
				if context.output.status != _|_ if context.output.status.ready != _|_ {
					ready: context.output.status.ready
				}
				message: *"EKS \(context.output.spec.eksClusterName) ready=\(ready)" | string
				if context.output.status != _|_ if context.output.status.failureMessage != _|_ {
					message: "EKS \(context.output.spec.eksClusterName) failed: \(context.output.status.failureMessage)"
				}
				"""#
			healthPolicy: #"""
				isHealth: *false | bool
				if context.output.status != _|_ if context.output.status.ready != _|_ {
					isHealth: context.output.status.ready
				}
				"""#
		}
	}
}

template: {
	// The control plane is the primary output: its status.ready is the health signal.
	output: {
		apiVersion: "controlplane.cluster.x-k8s.io/v1beta2"
		kind:       "AWSManagedControlPlane"
		metadata: name: "\(parameter.name)-control-plane"
		spec: {
			region:         parameter.region
			eksClusterName: parameter.eksClusterName
			version:        parameter.version
			roleName:       parameter.controlPlaneRoleName
			network: {
				vpc: id: parameter.vpcId
				subnets: [for s in parameter.subnetIds {id: s}]
				// The hub reaches a same-VPC cluster through its private endpoint ENIs, which
				// carry CAPA's control plane security group. Without this rule that group admits
				// only the cluster's own nodes and the hub's probe times out.
				if len(parameter.controlPlaneIngressCidrs) > 0 {
					additionalControlPlaneIngressRules: [{
						description: "hub and VPC clients to the EKS API server"
						protocol:    "tcp"
						fromPort:    443
						toPort:      443
						cidrBlocks:  parameter.controlPlaneIngressCidrs
					}]
				}
			}
			endpointAccess: {public: true, private: true}
			accessConfig: authenticationMode: "api_and_config_map"
			accessEntries: [{
				principalARN: parameter.spokePrincipalArn
				type:         "standard"
				accessPolicies: [{
					policyARN: "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
					accessScope: type: "cluster"
				}]
			}]
			if len(parameter.addons) > 0 {
				addons: [for a in parameter.addons {name: a.name, version: a.version, conflictResolution: "overwrite"}]
			}
			if len(parameter.tags) > 0 {
				additionalTags: parameter.tags
			}
		}
	}

	outputs: {
		cluster: {
			apiVersion: "cluster.x-k8s.io/v1beta1"
			kind:       "Cluster"
			metadata: name: parameter.name
			spec: {
				clusterNetwork: pods: cidrBlocks: ["192.168.0.0/16"]
				infrastructureRef: {
					apiVersion: "infrastructure.cluster.x-k8s.io/v1beta2"
					kind:       "AWSManagedCluster"
					name:       parameter.name
				}
				controlPlaneRef: {
					apiVersion: "controlplane.cluster.x-k8s.io/v1beta2"
					kind:       "AWSManagedControlPlane"
					name:       "\(parameter.name)-control-plane"
				}
			}
		}
		managedCluster: {
			apiVersion: "infrastructure.cluster.x-k8s.io/v1beta2"
			kind:       "AWSManagedCluster"
			metadata: name: parameter.name
			spec: {}
		}
		machinePool: {
			apiVersion: "cluster.x-k8s.io/v1beta1"
			kind:       "MachinePool"
			metadata: name: "\(parameter.name)-pool-0"
			spec: {
				clusterName: parameter.name
				replicas:    parameter.nodes.desired
				template: spec: {
					clusterName: parameter.name
					bootstrap: configRef: {
						apiVersion: "bootstrap.cluster.x-k8s.io/v1beta2"
						kind:       "NodeadmConfig"
						name:       "\(parameter.name)-pool-0"
					}
					infrastructureRef: {
						apiVersion: "infrastructure.cluster.x-k8s.io/v1beta2"
						kind:       "AWSManagedMachinePool"
						name:       "\(parameter.name)-pool-0"
					}
				}
			}
		}
		managedMachinePool: {
			apiVersion: "infrastructure.cluster.x-k8s.io/v1beta2"
			kind:       "AWSManagedMachinePool"
			metadata: name: "\(parameter.name)-pool-0"
			spec: {
				eksNodegroupName: "\(parameter.eksClusterName)-pool-0"
				roleName:         parameter.nodeRoleName
				subnetIDs:        parameter.subnetIds
				amiType:          "AL2023_x86_64_STANDARD"
				instanceType:     parameter.nodes.instanceType
				scaling: {minSize: parameter.nodes.min, maxSize: parameter.nodes.max}
				if len(parameter.tags) > 0 {
					additionalTags: parameter.tags
				}
			}
		}
		nodeadmConfig: {
			apiVersion: "bootstrap.cluster.x-k8s.io/v1beta2"
			kind:       "NodeadmConfig"
			metadata: name: "\(parameter.name)-pool-0"
			spec: {}
		}
	}

	parameter: {
		// +usage=CAPI object name prefix, also the Cluster name on the hub
		name: string
		// +usage=EKS cluster name in AWS (defaults to name)
		eksClusterName: *name | string
		region:         string
		// +usage=EKS version in CAPA form, for example v1.35
		version: *"v1.35" | string
		vpcId:   string
		subnetIds: [...string]
		// +usage=Pre-created IAM role names; CAPA never creates roles here
		controlPlaneRoleName: string
		nodeRoleName:         string
		// +usage=IAM principal granted AmazonEKSClusterAdminPolicy through an access entry (the SpokeCluster's spoke role)
		spokePrincipalArn: string
		// +usage=CIDRs allowed to reach the API server on 443 besides the cluster's own nodes; set the hub's VPC CIDR for a same-VPC hub
		controlPlaneIngressCidrs: *[] | [...string]
		// +usage=Adopting an EKS cluster CAPA did not create requires the cluster to carry the tag sigs.k8s.io/cluster-api-provider-aws/cluster/<eksClusterName>=owned; CAPA refuses an untagged cluster as not owned. The rendered objects are identical; this flag records intent on the plane and lets other substrates switch to their import form (Crossplane managementPolicies Observe, ACK adoption annotation)
		adopt: *false | bool
		// +usage=EKS addons with explicit versions
		addons: *[] | [...{name: string, version: string}]
		nodes: {
			instanceType: *"m5.large" | string
			min:          *1 | int
			max:          *3 | int
			desired:      *2 | int
		}
		tags: *{} | {[string]: string}
	}
}
