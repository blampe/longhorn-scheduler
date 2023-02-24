# 🐮 longhorn-scheduler

This is a fork of [linstor-scheduler-extender] modified to work with [Longhorn]
volumes. It uses [Stork] to schedule pods alongside existing replicas for
optimal performance.

This is in contrast to Longhorn's built-in [data locality] functionality, which
only affects pods that have already been scheduled, and which re-allocates
replicas to achieve locality.

## Behavior

Scheduling is best-effort -- if no replica nodes have capacity the pod will be
scheduled without locality.

In situations where a pod has multiple Longhorn volumes attached, preference is
given to nodes with more replicas.

RWX volumes will attempt to schedule pods on the same node as the NFS share
manager, but the share manager may not have data locality _unless_ the admission
controller is also deployed (see below).

## Deployment

```
kubectl apply -f deploy/longhorn-scheduler.yaml
```

## Usage

Use `schedulerName: longhorn` in your pod spec to take advantage of the
scheduler.

## Admission controller

You can also deploy the admission controller to automatically assign
`schedulerName: longhorn` to all pods with Longhorn volumes:

```
kubectl apply -f deploy/longhorn-scheduler-admission.yaml
```

[linstor-scheduler-extender]: https://github.com/piraeusdatastore/linstor-scheduler-extender
[Longhorn]: http://longhorn.io
[Stork]: https://github.com/libopenstorage/stork
[data locality]: https://longhorn.io/docs/1.4.0/high-availability/data-locality/#data-locality-settings
