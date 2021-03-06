Prometheus
==========

This directory contains the following files:
- K8s manifests for Prometheus
- Configuration files for Prometheus

Alert rules
-----------

YAML files for alert rules and tests are placed as follows:

```console
neco-apps
├── monitoring
|   ├── base
|   │   ├── prometheus
|   │   |   ├── alert_rules # alert rules for each ArgoCD app and particular components (e.g. k8s node)
|   |   |   │   ├── argocd.yaml
|   |   |   │   ├── ...
|   |   |   │   └── vault.yaml
|   |   |   ├── record_rules.yaml # record rules used in `alert_rules`
|   │   |   └── ...
|   │   └── ...
|   └── overlays
└── test
    ├── alert_test # test for each application
    │   ├── argocd.yaml
    │   ├── ...
    │   └── vault.yaml
    └── ...
```

Each YAML file contains tests for the corresponding application.

Run the unit test with the following command:

```console
$ cd $GOPATH/src/github.com/cybozu-go/neco-apps

# Run all tests
$ promtool test rules ./test/alert_test/*.yaml

# Run a single test
$ promtool test rules ./test/alert_test/argocd.yaml
```

Severity Levels
---------------

All alert rules should have the `severity` labels. This label indicates the level of the severity of the alert.

- `info`: No problem is occurred, but just notify.
- `warning`: Investigate to decide whether any action is required.
- `minor`: Action is required, but the situation is not serious at this time.
- `critical`: Action is required immediately because the problem gets worse. Investigate and resolve the causes of alert as soon as possible.

Notice
------

[Some alert rules](./alert_rules/kubernetes.yaml) come from [coreos/kube-prometheus project](https://github.com/coreos/kube-prometheus).
