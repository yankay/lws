# KEP-666: Gang Scheduling in LWS

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [LWS-level Gang Scheduling](#lws-level-gang-scheduling)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [LWS API](#lws-api)
  - [Lifecycle of the Workload Object](#lifecycle-of-the-workload-object)
    - [Manifest Lifecycle](#manifest-lifecycle)
    - [Default-created Lifecycle](#default-created-lifecycle)
    - [Lifecycle Management](#lifecycle-management)
  - [Examples](#examples)
    - [User-created Workload](#user-created-workload)
    - [LWS-created Workload](#lws-created-workload)
  - [Limitations of the alpha Workload API](#limitations-of-the-alpha-workload-api)
  - [Test Plan](#test-plan)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

Integrate the upstream Kubernetes Workload API (alpha, [kubernetes/enhancements#5558][workload-kep]) into LWS as a gang-scheduling provider.
The pods of an LWS replica (1 leader + (size − 1) workers) are treated as a single all-or-nothing scheduling unit, via **one PodGroup per replica**.

[workload-kep]: https://github.com/kubernetes/enhancements/pull/5558

## Motivation

When cluster resources are tight, the scheduler may schedule a replica's leader while leaving its workers pending.
The replica consumes resources but cannot serve any request, since the model needs all pods to start (see [issue #167](https://github.com/kubernetes-sigs/lws/issues/167)).
Gang scheduling at the scheduler layer prevents this deadlock.

[KEP-407][kep407] already covers gang scheduling via third-party PodGroup CRDs (Volcano / coscheduling / YuniKorn).
This KEP adds a parallel, upstream-native path for clusters that have the Workload API enabled.

[kep407]: https://github.com/kubernetes-sigs/lws/tree/main/keps/407-gang-scheduling

### Goals

- Integrate the alpha Workload API as a gang-scheduling provider for LWS.
- Support both an LWS-managed lifecycle and a user-managed lifecycle for the Workload object.
- Make each LWS replica an independent all-or-nothing scheduling unit.

### Non-Goals

- Defining the upstream Workload API itself.
- Replacing [KEP-407][kep407].
- Per-role minimums across multiple LWS objects (the [DisaggregatedSet][kep766] use case); see [Limitations](#limitations-of-the-alpha-workload-api).

[kep766]: https://github.com/kubernetes-sigs/lws/tree/main/keps/766-DisaggregatedSet

## Proposal

LWS gains a new `spec.schedulingPolicy.gang` field.
When set, every replica gets a PodGroup whose `MinCount` defaults to `LeaderWorkerTemplate.Size`, so all pods of a replica must co-schedule.
The pod webhook injects the per-pod PodGroup reference based on the pod's `leaderworkerset.sigs.k8s.io/group-index` label.

### User Stories

#### LWS-level Gang Scheduling

As a user running distributed inference services with LWS, I want a replica's pods to be scheduled only if there are enough resources for all of them; otherwise none should be scheduled.
Other replicas of the same LWS should not be blocked because one replica is starved of resources.

### Risks and Mitigations

The upstream alpha API may rename fields before beta; LWS-side decisions may need follow-ups.
Mitigation: keep the LWS surface minimal (one struct, one optional field) and track [#5558][workload-kep].

## Design Details

### LWS API

```go
type SchedulingPolicy struct {
    // Gang opts the LWS into gang scheduling via the upstream Workload API.
    // When nil, no gang scheduling is performed by LWS.
    Gang *GangSchedulingPolicy `json:"gang,omitempty"`
}

type GangSchedulingPolicy struct {
    // MinCount is the minimum number of pods within a single PodGroup that
    // must be co-scheduled. Defaults to LeaderWorkerTemplate.Size; values
    // smaller than Size weaken the per-replica gang guarantee.
    // +optional
    MinCount *int32 `json:"minCount,omitempty"`
}
```

### Lifecycle of the Workload Object

#### Manifest Lifecycle

The user creates and owns the Workload.
LWS only injects the per-pod PodGroup reference via the pod webhook, derived as `<base>-<group-index>` where `<base>` is taken from the per-template `workload.name` field.
LWS does not create, validate, or delete the Workload in this mode.

The user is responsible for keeping the PodGroup count aligned with LWS `replicas`, and (typically) setting per-PodGroup `MinCount = size`.

#### Default-created Lifecycle

When `spec.schedulingPolicy.gang` is set and templates do not point at an external Workload, LWS creates a Workload with:

- **Name** — `lws-workload-<lws-name>`
- **PodGroups** — one per LWS replica (`replicas` PodGroups in total)
- **PodGroup.Name** — `lws-podgroup-<lws-name>-<group-index>`
- **MinCount** — defaults to LWS `size`

A replica is the smallest self-contained unit of an LWS workload, and replicas are independent of each other; using one PodGroup per replica ensures pods of one replica gang-schedule together while a starved replica does not block the others.
This matches the per-replica boundary chosen by [KEP-407][kep407].

#### Lifecycle Management

The Workload is created before the leader StatefulSet, so the PodGroup exists by the time the leader pod is created.
The LWS object is the controller owner of the Workload, so it is GC'd on LWS deletion.
On replica scale up/down, the controller patches `podGroups[]` in place rather than recreating the Workload.
PodGroups are reused across rolling updates, since they are keyed by `group-index`, not by revision.

### Examples

#### User-created Workload

```yaml
apiVersion: scheduling/v1alpha1
kind: Workload
metadata:
  name: my-lws-gang
spec:
  podGroups:
    - { name: my-lws-gang-0, policy: { gang: { minCount: 2 } } }
    - { name: my-lws-gang-1, policy: { gang: { minCount: 2 } } }
    - { name: my-lws-gang-2, policy: { gang: { minCount: 2 } } }
    - { name: my-lws-gang-3, policy: { gang: { minCount: 2 } } }
---
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
        # Base "my-lws-gang"; webhook appends "-<group-index>".
        workload: { name: my-lws-gang }
    workerTemplate:
      spec:
        workload: { name: my-lws-gang }
```

#### LWS-created Workload

```yaml
apiVersion: leaderworkerset.x-k8s.io/v1
kind: LeaderWorkerSet
metadata:
  name: leaderworkerset-sample
spec:
  schedulingPolicy:
    gang: {}    # MinCount defaults to size (= 2)
  replicas: 4
  leaderWorkerTemplate:
    size: 2
    leaderTemplate: { spec: {} }
    workerTemplate: { spec: {} }
```

The Workload that LWS creates:

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

In general, `replicas: N, size: M` produces **N PodGroups, M pods each**.

### Limitations of the alpha Workload API

`MinCount` only enforces "M pods within this PodGroup co-schedule"; it has no notion of per-role minimums.
This is enough for the LWS-level case (one replica = one PodGroup, all M pods co-schedule).
It is **not** enough for the [DisaggregatedSet][kep766] case, where *"≥1 prefill replica AND ≥1 decode replica must be ready simultaneously"*.
Lumping prefill and decode into one PodGroup with `MinCount = sum` does not help — the scheduler may satisfy `MinCount` with M prefill and zero decode pods.
The concrete API for that case is left to KEP-766 and is out of scope here.

### Test Plan

- **Unit**: API validation and defaulting; webhook PodGroup-name injection from `group-index`; controller Workload construction.
- **Integration**: default-created mode (create / scale / delete / GC); manifest mode (LWS does not touch the Workload); pods of the same `group-index` end up in the same PodGroup.
- **e2e**: requires a cluster with the Workload API enabled; once available, add a deadlock-prevention test on a resource-constrained cluster.

### Graduation Criteria

Targets `alpha` while the upstream API is alpha.
Promotion past alpha is gated on the upstream API reaching beta with stable field names and on integration coverage for both lifecycle modes.

## Implementation History

- **2025-10-13** — Initial external draft by @Edwinhr716 ([Google Doc](https://docs.google.com/document/d/1QlcIBtR2KyOKYRUTGubhhxuy7NfjHs1fXMJlvdUCyhM)).
- **2026-05-03** — Imported as KEP-666; switched to per-replica PodGroup and added the alpha-API limitation section.

## Alternatives

**One shared PodGroup per LWS with `Replicas=N, MinCount=M`** (Edwin's original draft).
Rejected: `MinCount` only requires M pods to co-schedule, with no notion of which replica they belong to.
The scheduler may legally pick M pods from different replicas, none complete, and the model still cannot start.
Per-replica PodGroups make each replica an independent all-or-nothing unit.

**Rely on [KEP-407][kep407] only**.
KEP-407 needs a third-party scheduler / PodGroup CRD.
This KEP adds an upstream-native path for clusters that don't run one.
The two are not mutually exclusive at the cluster level; a single LWS object should opt into at most one.
