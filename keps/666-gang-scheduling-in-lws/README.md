# KEP-666: Workload-Aware Scheduling for LWS and DisaggregatedSet

<!-- toc -->
- [Summary](#summary)
- [Goals](#goals)
- [Non-Goals](#non-goals)
- [Proposal](#proposal)
- [Design Details](#design-details)
  - [API Example](#api-example)
  - [Ownership and Object Mapping](#ownership-and-object-mapping)
  - [Topology and Placement](#topology-and-placement)
  - [Feature Gates and Compatibility](#feature-gates-and-compatibility)
  - [Validation](#validation)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
<!-- /toc -->

## Summary

This KEP integrates the Kubernetes [Workload-Aware Scheduling controller
APIs][kep-6089] with LeaderWorkerSet (LWS) and DisaggregatedSet. Each LWS
replica maps to one `PodGroup`. Each `(DisaggregatedSet slice, revision)` maps
to one `CompositePodGroup`.

[kep-6089]: https://github.com/kubernetes/enhancements/tree/master/keps/sig-scheduling/6089-was-controller-apis
[jobset-1253]: https://github.com/kubernetes-sigs/jobset/pull/1253

## Goals

- Add optional `spec.scheduling` fields to LWS and DisaggregatedSet.
- Schedule an LWS replica as one unit.
- Schedule all replica groups in a DisaggregatedSet slice as one unit.
- Apply topology constraints at the replica and slice boundaries.
- Preserve existing behavior when scheduling is not configured.

## Non-Goals

- Role-level CompositePodGroups or partial quorum.
- Leader, worker, or subgroup PodGroups within one LWS replica.
- Replacing DisaggregatedSet `placementPolicy`.
- Defining scheduler behavior, Kueue admission, or third-party gang adapters.

## Proposal

This KEP does not define LWS-specific scheduling types. `spec.scheduling`
directly reuses the KEP-6089 policy, constraints, disruption mode, and resource
claim types. It only defines group boundaries and controller ownership.

When the field is set, the policy defaults to `Gang`; `Basic` is the explicit
opt-out. An LWS gang includes one replica. A DisaggregatedSet gang includes one
slice. DisaggregatedSet uses the CompositePodGroup variants; resource claims
remain leaf-scoped.

The controller handoff follows the model in [JobSet PR 1253][jobset-1253].
DisaggregatedSet owns the revision `Workload` and slice
`CompositePodGroup`. LWS owns the leaf `PodGroup` and pods.

## Design Details

### API Example

Each slice admits one prefill replica and two decode replicas together in one
accelerator block. Each LWS replica is also a gang.

```yaml
apiVersion: disaggregatedset.x-k8s.io/v1
kind: DisaggregatedSet
metadata:
  name: serving
spec:
  slices: 2
  scheduling:
    policy:
      gang: {}
    constraints:
      topology:
      - level: topology.example.com/accelerator-block
  placementPolicy:
    type: ExclusiveSlice
    topology: topology.example.com/accelerator-block
  roles:
  - name: prefill
    spec:
      replicas: 1
      scheduling:
        policy:
          gang: {}
        constraints:
          topology:
          - level: topology.example.com/rack
      leaderWorkerTemplate:
        size: 4
        workerTemplate:
          spec:
            containers:
            - {name: server, image: example.com/prefill:latest}
  - name: decode
    spec:
      replicas: 2
      scheduling:
        policy:
          gang: {}
        constraints:
          topology:
          - level: topology.example.com/rack
      leaderWorkerTemplate:
        size: 2
        workerTemplate:
          spec:
            containers:
            - {name: server, image: example.com/decode:latest}
```

The top-level scheduling block configures the slice CompositePodGroup. Each
embedded block configures that role's replica PodGroups. A standalone LWS uses
the same embedded `spec.scheduling` shape.

### Ownership and Object Mapping

```text
DisaggregatedSet revision Workload
└─ slice CompositePodGroup
   ├─ prefill replica PodGroup
   └─ decode replica PodGroup
```

For a standalone LWS, LWS owns a `Workload` with one PodGroup template and
creates one runtime PodGroup per replica. Replica count is not limited by the
number of templates.

For DisaggregatedSet, the revision Workload has one PodGroup template per
role. DisaggregatedSet passes the KEP-6089 `group-template-name` and
`parent-composite-podgroup` annotations to each managed LWS. LWS creates the
runtime PodGroups before their pods and sets `pod.spec.schedulingGroup`.

### Topology and Placement

LWS scheduling constraints apply to one replica. DisaggregatedSet scheduling
constraints apply to one slice. These are the TAS boundaries.

`spec.placementPolicy` continues to provide slice spread and exclusivity.
Scheduling constraints do not replace it.

### Feature Gates and Compatibility

`WorkloadAwareScheduling` gates standalone LWS Workload and PodGroup support.
`CompositePodGroupScheduling` separately gates the DisaggregatedSet hierarchy,
so CompositePodGroup maturity does not block ordinary LWS support.

With `spec.scheduling` unset, behavior is unchanged. Admission rejects the
field when its feature gate or required upstream API is unavailable.

### Validation

- LWS scheduling requires `WorkloadAwareScheduling`.
- DisaggregatedSet scheduling requires both feature gates.
- An LWS `Gang` requires `startupPolicy: LeaderCreated`; `LeaderReady` does not
  create the full replica before admission.
- A `Gang` represents the full replica or slice; lower quorum is rejected.
- A DisaggregatedSet with `spec.scheduling` may have at most eight roles,
  matching the Workload PodGroup template limit. Without scheduling, the
  existing ten-role limit remains.
- Runtime LWS replicas are not subject to the template limit.

### Test Plan

[x] I/we understand the owners of the involved components may require updates
to existing tests before implementing this enhancement.

- Unit tests cover defaulting, validation, Workload compilation, and handoff
  annotations.
- Integration tests cover ownership, creation order, scaling, rollout
  isolation, deletion, and disabled gates.
- End-to-end tests cover replica gang scheduling, slice gang scheduling, and
  TAS constraints.

### Graduation Criteria

Alpha requires opt-in gates and full test coverage. Beta requires stable
upstream APIs, end-to-end coverage, and upgrade guidance.

## Implementation History

- 2025-10-13: Initial gang-scheduling design drafted.
- 2026-08-03: Reworked around KEP-6089 and DisaggregatedSet slices.
