/*
Copyright 2022 The Koordinator Authors.
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package removepodsviolatingnodeaffinity

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	deschedulerconfig "github.com/koordinator-sh/koordinator/pkg/descheduler/apis/config"
	"github.com/koordinator-sh/koordinator/pkg/descheduler/apis/config/validation"
	"github.com/koordinator-sh/koordinator/pkg/descheduler/framework"
	nodeutil "github.com/koordinator-sh/koordinator/pkg/descheduler/node"
	podutil "github.com/koordinator-sh/koordinator/pkg/descheduler/pod"
)

const PluginName = "RemovePodsViolatingNodeAffinity"

// RemovePodsViolatingNodeAffinity evicts pods on nodes which violate node affinity
type RemovePodsViolatingNodeAffinity struct {
	handle    framework.Handle
	args      *deschedulerconfig.RemovePodsViolatingNodeAffinityArgs
	podFilter podutil.FilterFunc
}

var _ framework.Plugin = &RemovePodsViolatingNodeAffinity{}
var _ framework.DeschedulePlugin = &RemovePodsViolatingNodeAffinity{}

func New(args runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	nodeAffinityArgs, ok := args.(*deschedulerconfig.RemovePodsViolatingNodeAffinityArgs)
	if !ok {
		return nil, fmt.Errorf("want args to be of type RemovePodsViolatingNodeAffinityArgs, got %T", args)
	}

	if err := validation.ValidateRemovePodsViolatingNodeAffinityArgs(nil, nodeAffinityArgs); err != nil {
		return nil, err
	}

	var includedNamespaces, excludedNamespaces sets.String
	if nodeAffinityArgs.Namespaces != nil {
		includedNamespaces = sets.NewString(nodeAffinityArgs.Namespaces.Include...)
		excludedNamespaces = sets.NewString(nodeAffinityArgs.Namespaces.Exclude...)
	}

	podFilter, err := podutil.NewOptions().
		WithNamespaces(includedNamespaces).
		WithoutNamespaces(excludedNamespaces).
		WithLabelSelector(nodeAffinityArgs.LabelSelector).
		BuildFilterFunc()
	if err != nil {
		return nil, fmt.Errorf("error initializing pod filter function: %v", err)
	}

	return &RemovePodsViolatingNodeAffinity{
		handle:    handle,
		podFilter: podFilter,
		args:      nodeAffinityArgs,
	}, nil
}

func (d *RemovePodsViolatingNodeAffinity) Name() string {
	return PluginName
}

func (d *RemovePodsViolatingNodeAffinity) Deschedule(ctx context.Context, nodes []*corev1.Node) *framework.Status {
	for _, nodeAffinity := range d.args.NodeAffinityType {
		klog.V(2).InfoS("Executing for nodeAffinityType", "nodeAffinity", nodeAffinity)

		switch nodeAffinity {
		case "requiredDuringSchedulingIgnoredDuringExecution":
			for _, node := range nodes {
				klog.V(1).InfoS("Processing node", "node", klog.KObj(node))

				pods, err := podutil.ListPodsOnANode(
					node.Name,
					d.handle.GetPodsAssignedToNodeFunc(),
					podutil.WrapFilterFuncs(d.podFilter, func(pod *corev1.Pod) bool {
						return d.handle.Evictor().Filter(pod) &&
							!nodeutil.PodFitsCurrentNode(d.handle.GetPodsAssignedToNodeFunc(), pod, node) &&
							nodeutil.PodFitsAnyNode(d.handle.GetPodsAssignedToNodeFunc(), pod, nodes)
					}),
				)
				if err != nil {
					klog.ErrorS(err, "Failed to get pods", "node", klog.KObj(node))
				}

				for _, pod := range pods {
					if pod.Spec.Affinity != nil && pod.Spec.Affinity.NodeAffinity != nil && pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
						klog.V(1).InfoS("Evicting pod", "pod", klog.KObj(pod))
						d.handle.Evictor().Evict(ctx, pod, framework.EvictOptions{Reason: "Pod violating NodeAffinity"})
					}
				}
			}
		default:
			klog.ErrorS(nil, "Invalid nodeAffinityType", "nodeAffinity", nodeAffinity)
		}
	}
	return nil
}
