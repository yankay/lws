# KEP-666: Gang Scheduling in LWS

**Authors**: @Edwinhr716

**Status**: Proposal

## Update History

- **Oct 13, 2025** — Initial draft covering integration with alpha.
- **May 03, 2026** — Imported into the LWS repo as KEP-666; added a section on
  the limitation of the alpha Workload API for disaggregated serving.

## Background

Kubernetes is introducing the concept of gang scheduling to the scheduler with
the addition of the Workload API. Gang scheduling allows a group of pods to be
scheduled in an all-or-nothing manner.

## Alpha Scope

The alpha version of the Workload API
(see [kubernetes/enhancements#5558][workload-kep] for context) introduces the
following fields that are relevant to LWS:

- `PodGroup`
- `GangSchedulingPolicy`
- `GangSchedulingPolicy.MinCount`

And adds the following field to the PodSpec to link the pod to its PodGroup:

- `PodGroup`

[workload-kep]: https://github.com/kubernetes/enhancements/pull/5558

## LWS User Stories

### LWS-level Gang Scheduling

As a user running distributed inference services with LeaderWorkerSet, I want
to prevent resource deadlocks and ensure service availability in
resource-constrained environments.

I am deploying a model which requires more than one pod to run (one leader
plus one or more workers per replica). I want a replica's pods to be
scheduled only if there are enough resources to run all of them; otherwise
none of them should be scheduled.

Currently, when cluster resources are limited, the scheduler might only
schedule leader pods while leaving worker pods pending. The result is an
inference service that consumes cluster resources but is unable to serve any
request, because the model cannot start without all of its pods. With gang
scheduling, leader and workers within a replica are treated as a single
scheduling unit — they are either all scheduled together or all remain
pending — preventing this resource-deadlock situation.

## Lifecycle of Workload Object

### Manifest Lifecycle

This is the pattern where the user owns the entire lifecycle of the resource.
We make the following assumptions:

- Users must delete the resource.
- Users avoid sharing the resource across workloads.

The LWS controller will check if `PodSpec.WorkloadRef` is set to determine
whether or not the user will own the lifecycle of the resource. The user must
make sure that:

- The Workload contains **at least `LeaderWorkerSetSpec.Replicas` PodGroups**,
  named so that the LWS pod webhook can derive the per-pod PodGroup name from
  the `leaderworkerset.sigs.k8s.io/group-index` label (default convention:
  `<base>-<group-index>`, where `<base>` is configured via the LWS spec).
- Each such PodGroup's `MinCount` should be the LWS `size`.

Given the above assumptions, the controller will inject the PodGroup name
into the PodSpec via the pod webhook, derived from the label
`leaderworkerset.sigs.k8s.io/group-index` so that pods of the same replica
end up in the same PodGroup.

### Default-created Lifecycle

In this pattern, LWS will create a Workload resource. LWS ensures that it is
deleted when the workload is deleted.

We will add a new field to the LWS API to trigger the creation of the
workload resource:

```go
type SchedulingPolicy struct {
    Gang *GangSchedulingPolicy
}

type GangSchedulingPolicy struct {
    // Optional. Minimum number of pods within a single PodGroup that must be
    // co-scheduled. Defaults to LeaderWorkerTemplate.Size, which is the
    // recommended value: under the per-replica PodGroup model each PodGroup
    // contains exactly Size pods, so MinCount = Size means "all pods of this
    // replica are co-scheduled or none of them are" — the LWS-level gang
    // semantics. Setting MinCount < Size weakens the guarantee (some pods of
    // a replica may be scheduled while others remain pending) and is
    // generally not useful.
    MinCount *int
}
```

The workload resource will have the following default values:

- **Name** — set to `lws-workload-<lws-name>`.
- **PodGroups** — one PodGroup per LWS replica (i.e. `LeaderWorkerSetSpec.Replicas`
  PodGroups in total). A replica (1 leader + (size-1) workers) is the smallest
  self-contained unit of an LWS workload, and replicas are independent of each
  other; using one PodGroup per replica ensures that pods of one replica are
  gang-scheduled together while a resource-starved replica does not block the
  others. This also matches the per-replica boundary chosen by [KEP-407] and
  the per-replica PodGroup model adopted by upstream KEP-4671 v1alpha2 (where
  each replica is a standalone PodGroup object).
- **PodGroup.Name** — set to `lws-podgroup-<lws-name>-<group-index>`, where
  `group-index` is the LWS replica ordinal (`0..Replicas-1`).
- **PodGroup.Policy.Gang.MinCount** — defaults to the LWS `size`, which is
  the recommended value (each PodGroup contains exactly `size` pods, so
  `MinCount = size` enforces full per-replica gang). Users may override via
  `GangSchedulingPolicy.MinCount`, though values smaller than `size` weaken
  the guarantee and are generally not useful.

Similar to the manifest lifecycle, the controller also injects the PodGroup
name into each pod via the webhook (using the
`leaderworkerset.sigs.k8s.io/group-index` label to pick the right PodGroup).

[KEP-407]: https://github.com/kubernetes-sigs/lws/tree/main/keps/407-gang-scheduling

### Lifecycle Management

The workload resource will be created before the leader StatefulSet, to
ensure that at the time of the leader pod creation the workload resource
already exists.

To ensure that the workload is deleted once the LWS object is, the controller
will set the LWS object as the owner of the workload. In the case of a
controller restart, this also provides a way to check if there already exists
a workload for a specific LWS object.

## Examples

### User-created Workload

The user creates one PodGroup per intended LWS replica (4 replicas, size 2):

```yaml
apiVersion: scheduling/v1alpha1
kind: Workload
metadata:
  name: lws
spec:
  podGroups:
    - { name: lws-gang-0, policy: { gang: { minCount: 2 } } }
    - { name: lws-gang-1, policy: { gang: { minCount: 2 } } }
    - { name: lws-gang-2, policy: { gang: { minCount: 2 } } }
    - { name: lws-gang-3, policy: { gang: { minCount: 2 } } }
```

```yaml
apiVersion: leaderworkerset.x-k8s.io/v1
kind: LeaderWorkerSet
metadata:
  name: leaderworkerset-sample
spec:
  replicas: 4
  leaderWorkerTemplate:
    size: 2
    leaderTemplate:
      spec:
        workload:
          name: lws
          # podGroup is injected per-pod by the LWS pod webhook,
          # set to lws-gang-<group-index>.
    workerTemplate:
      spec:
        workload:
          name: lws
          # podGroup is injected per-pod by the LWS pod webhook,
          # set to lws-gang-<group-index>.
```

### LWS-created Workload

```yaml
apiVersion: leaderworkerset.x-k8s.io/v1
kind: LeaderWorkerSet
metadata:
  name: leaderworkerset-sample
spec:
  schedulingPolicy:
    gang: {}    # MinCount defaults to leaderWorkerTemplate.size (= 2)
  replicas: 4
  leaderWorkerTemplate:
    size: 2
    leaderTemplate:
      spec:
    workerTemplate:
      spec:
```

And the Workload that is created by LWS will look like:

```yaml
apiVersion: scheduling/v1alpha1
kind: Workload
metadata:
  name: lws-workload-leaderworkerset-sample
  ownerReferences:
    - apiVersion: leaderworkerset.x-k8s.io/v1
      kind: LeaderWorkerSet
      name: leaderworkerset-sample
      controller: true
spec:
  podGroups:
    - { name: lws-podgroup-leaderworkerset-sample-0, policy: { gang: { minCount: 2 } } }
    - { name: lws-podgroup-leaderworkerset-sample-1, policy: { gang: { minCount: 2 } } }
    - { name: lws-podgroup-leaderworkerset-sample-2, policy: { gang: { minCount: 2 } } }
    - { name: lws-podgroup-leaderworkerset-sample-3, policy: { gang: { minCount: 2 } } }
```

A `replicas: 3, size: 4` LWS would produce 3 PodGroups, each with
`minCount: 4`. The N-and-M relation in general: an LWS with `replicas: N` and
`size: M` produces **N PodGroups**, each containing exactly **M pods** (1
leader + (M-1) workers).

## Future Thoughts: Extension to DisaggregatedSet

> **Not in scope for this KEP.** This section is a forward-looking sketch
> only, recording how the gang-scheduling story might be extended to
> `DisaggregatedSet` ([KEP-766][kep766]) in the future. No commitment is made
> here.

Where the LWS-level user story is *"a single replica's pods co-schedule
all-or-nothing"*, `DisaggregatedSet` introduces a higher-level availability
requirement that spans multiple roles:

> *At least 1 prefill replica AND at least 1 decode replica must be ready
> simultaneously for the disaggregated serving system to be usable.*

This cannot be expressed by a single `MinCount` on a single PodGroup: the
alpha Workload API's `MinCount` only enforces "M pods within this PodGroup
co-schedule", with no notion of per-role minimums. Lumping prefill and decode
pods into one PodGroup with `MinCount = sum` does not help either — the
scheduler may legally satisfy `MinCount` with M prefill pods and zero decode
pods, and the system still cannot serve.

The concrete API for handling this case is left to KEP-766 (or a follow-up
KEP) and is explicitly out of scope here.

[kep766]: https://github.com/kubernetes-sigs/lws/tree/main/keps/766-DisaggregatedSet
