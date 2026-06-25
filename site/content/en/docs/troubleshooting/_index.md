---

title: "Troubleshooting"
linkTitle: "Troubleshooting"
weight: 10
date: 2025-03-25
description: >
  LWS troubleshooting tips.
no_list: true
---

## 1. Infinite StatefulSet Creation Loops

When creating StatefulSets, you might encounter an issue where the `kubectl get pod` output shows an infinite loop of pod creation, as illustrated below:

```
vllm-alpha-distributed-serving-0                             0/1     Running             0          10s
vllm-alpha-distributed-serving-0-0                           1/1     Running             0          10s
vllm-alpha-distributed-serving-0-0-0                         0/1     ContainerCreating   0          10s
vllm-alpha-distributed-serving-0-0-0-0                       0/1     ContainerCreating   0          10s
vllm-alpha-distributed-serving-0-0-0-0-0                     0/1     ContainerCreating   0          10s
vllm-alpha-distributed-serving-0-0-0-0-0-0                   0/1     ContainerCreating   0          10s
```

### Cause

This issue arises on Kubernetes clusters running versions earlier than 1.27, where the `StatefulSetStartOrdinal` feature gate is not enabled. In such cases, the LeaderWorkerSet controllers enter infinite reconciliation loops, potentially exhausting cluster resources.

### Solution

To resolve this issue:
- Upgrade your Kubernetes cluster to version **1.26 or higher**. Versions below 1.26 may exhibit unexpected behavior.
- For Kubernetes version 1.26, manually enable the `StatefulSetStartOrdinal` feature gate.
- For versions above 1.26, this feature gate is enabled by default.

---

## 2. Rolling Update of Leader Pods During LWS Upgrade

When upgrading from LWS version 0.5.0 to 0.6.0 or later, all Leader Pods configured with SubGroup will undergo a rolling update.

### Cause

In LWS version 0.5.0, an annotation key was set as `leaderworkerset.gke.io/subgroup-size`. Starting from version 0.6.0, this key was changed to `leaderworkerset.sigs.k8s.io/subgroup-size` as part of [this pull request](https://github.com/kubernetes-sigs/lws/pull/434). As a result, upgrading the LWS controller triggers a rolling update.

### Solution

Rolling updates typically do not impact system operations. However, it is recommended to monitor the system closely during the upgrade process.

---

## 3. Unable to Create LWS Object with a Name Exceeding 51 Characters

When creating an LWS object with a name longer than 51 characters, the worker pods fail to start. The error message appears as follows:

```
Pod "<worker-pod-name>" is invalid: metadata.labels: Invalid value: <worker-sts-name>-<10-character-hash>": must be no more than 63 characters
```

### Cause

This issue occurs because StatefulSet names exceeding 57 characters prevent pods from starting, as described in [this Kubernetes issue](https://github.com/kubernetes/kubernetes/issues/64023). Since LWS relies on StatefulSets, it is not possible to create an LWS object with a name longer than 51 characters.

### Solution

The name limit for LWS objects is calculated as `(51 - int(replicas / 10))`. This is because the worker StatefulSet name grows by one character for replicas above 9, another character for replicas above 99, and so on. Ensure that the LWS object name adheres to this limit to avoid issues.

---

## 4. LeaderWorkerSet CRD Missing After Helm Upgrade

When upgrading an LWS Helm release from `v0.7.0` or earlier to `v0.8.0` or
later, the `leaderworkersets.leaderworkerset.x-k8s.io` CRD might be deleted.
If this happens, existing LeaderWorkerSet objects are also removed by Kubernetes
garbage collection.

### Cause

LWS charts before `v0.8.0` stored the LeaderWorkerSet CRD in the regular Helm
templates. Starting with `v0.8.0`, the CRDs are stored in the chart `crds/`
directory. During an upgrade across this layout change, Helm can see the CRD in
the old release manifest but not in the new rendered templates, then treat it as
a deleted resource.

### Solution

Before upgrading a Helm installation from `v0.7.0` or earlier to `v0.8.0` or
later, run the helper script before `helm upgrade`:

```shell
CHART_VERSION=0.9.0
hack/prepare-helm-legacy-crd-upgrade.sh --chart-version "$CHART_VERSION"
```

Then run `helm upgrade` with the same `--values` and `--set` options used by the
existing release.

If the CRD was already deleted, restore the LeaderWorkerSet resources from your
cluster backup, then run the helper script before the next upgrade attempt.
