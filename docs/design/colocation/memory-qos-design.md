# Memory QoS Design

## Overview

## Scope

## Constraints

- Only Cgroup V2 is supported.

## Design Proposal

### CRD 定义

```yaml
apiVersion: colocation.volcano.sh/v1alpha1
kind: QOSConfig
metadata:
  name: cfg1
spec:
  selector:
    matchLabels:
      app: offline-test
    memoryQOS:
      highThreshold: 1 # Memory throttling percentage; default=1, range: 0~1
      lowThreshold: 0  # Memory priority protection percentage; default=0, range: 0~1
      minThreshold: 0  # Absolute memory protection percentage; default=0, range: 0~1
```

### Design

![memory-qos](images/memory-qos.png)

- The controller will update the Pod annotation `volcano.sh/memory-qos` based on the memory QoS configuration. The value is of JSON type and follows the same format as `QOSConfig.spec.memoryQOS`.
- When the `spec.selector` field of a `QOSConfig` instance is updated, the controller must enqueue all Pods matched before and after the update.
- During processing in the controller’s work queue, if a Pod has the annotation `volcano.sh/memory-qos` but no matching `QOSConfig` is found, the annotation value should be cleared (without removing the annotation key).
- When handling a Pod, volcano-agent modifies the Pod's cgroup interface files according to the configuration in the Pod annotation `volcano.sh/memory-qos`:
  ```
  memory.high = resources.limits[memory] * highThreshold
  memory.low = resources.requests[memory] * lowThreshold
  memory.min = resources.requests[memory] * minThreshold
  ```
- If the Pod has the annotation `volcano.sh/memory-qos` with an empty value, volcano-agent resets the memcg settings for the Pod as follows:
  ```
  memory.high = max
  memory.low = 0
  memory.min = 0
  ```
  If the Kubernetes feature gate `MemoryQoS` is enabled, the reset values should be calculated according to: [Quality-of-Service for Memory Resources](https://kubernetes.io/blog/2023/05/05/qos-memory-resources/)
