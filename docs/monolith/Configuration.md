---
title: Configuration
weight: 2
---
# Configuration Options

There are several ways to provide configurations to the OpenTofu
provider that will propagate to the underlying workspace. In the
following sections, we will cover the most common ones.

## IAM Roles for Service Accounts (IRSA)

You can setup the OpenTofu Provider using AWS [IAM Roles for Service Accounts
(IRSA)](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html).
For more information, check out the example
[setup](../examples/aws-eks-irsa-setup.yaml), the process is similar to what you
would use for the
[provider-aws](https://github.com/upbound/provider-aws/blob/main/AUTHENTICATION.md#authentication-using-irsa).

## Provider Performance and Throughput

The performance and throughput of the provider can be tuned using the `--poll`
and `--max-reconcile-rate` arguments in a `ControllerConfig`.

The `poll` option determines how often the provider will compare the desired
`Workspace` configuration with the actual deployed resources (`tofu plan`)
and reconcile any differences (`tofu apply`).  The default value is 10m.
Shorter `poll` intervals increase the load on the provider by reconciling
existing `Workspaces` more often to allow for faster reconciliation of
differences, but this can cause the provider to take longer to process new
`Workspace` objects that are created.  Longer poll intervals will reduce the
load on the provider by reconciling existing `Workspaces` less often, taking a
longer time to identify and reconcile differences, but also shortening the
amount of time required for the provider to respond to new `Workspaces`.

The `max-reconcile-rate` option determines how many `Workspace` objects can be
reconciled in parallel concurrently.  The default value is 1.  Increasing this
value will allow the provider to process more `Workspaces` but will consume
more CPU, as the provider must run `tofu plan` for each `Workspace`.  The
provider could potentially use the same number of CPUs as the value set for
`max-reconcile-rate`, so plan accordingly or use `resources.requests` and
`resources.limits` to control the number of CPUs available to the provider.

For example, to set a polling interval of 5m and process 10 `Workspaces`
concurrently:

```yaml
apiVersion: pkg.crossplane.io/v1alpha1
kind: ControllerConfig
metadata:
  name: opentofu
  labels:
    app: crossplane-provider-opentofu
spec:
  args:
    - -d
    - --poll=5m
    - --max-reconcile-rate=10
```

and set the `spec.controllerConfigRef.name` in the Provider to `opentofu`.

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-opentofu
spec:
  package: xpkg.upbound.io/upbound/provider-opentofu:<version>
  controllerConfigRef:
    name: opentofu
```


## Private Git repository support

To securely propagate git credentials create a `git-credentials` secret in [git
credentials store] format.

```sh
cat .git-credentials
https://<user>:<token>@github.com

kubectl create secret generic git-credentials --from-file=.git-credentials
```

Reference it in ProviderConfig.

```yaml
apiVersion: opentofu.upbound.io/v1beta1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
  - filename: .git-credentials # use exactly this filename
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: git-credentials
      key: .git-credentials
...
```

Standard `.git-credentials` filename is important to keep so provider-opentofu
controller will be able to automatically pick it up.

## `.tofurc` repository support

```yaml
spec:
  credentials:
  - filename: .tofurc # use exactly this filename by convention
    source: Secret
    secretRef:
      namespace: upbound-system
      name: tofurc
      key: .tofurc
```

will enable [Tofu CLI Configuration File](https://opentofu.org/docs/cli/config/config-file/)
installed from Kubernetes secret

## Terraform Output support

Non-sensitive outputs are mapped to the status.atProvider.outputs section as
strings so they can be referenced by the Composition. Strings, numbers and
booleans can be referenced directly in Compositions and can be used in the
_convert_ transform if type conversion is needed. Tuple and object outputs will
be available in the corresponding JSON form. This is required because undefined
object attributes are not specified in the Workspace CRD and so will be
sanitized before the status is stored in the database.

That means that any output values required for use in the Composition must be
published explicitly and individually, and they cannot be referenced inside a
tuple or object.

For example, the following terraform outputs:
```hcl
      output "string" {
        value = "bar"
        sensitive = false
      }
      output "number" {
        value = 1.9
        sensitive = false
      }
      output "object" {
        // This will be a JSON string - the key/value pairs are not accessible
        value = {"a": 3, "b": 2}
        sensitive = false
      }
      output "tuple" {
        // This will be a JSON string - the elements will not be accessible
        value = ["foo", "bar"]
        sensitive = false
      }
      output "bool" {
        value = false
        sensitive = false
      }
      output "sensitive" {
        value = "SENSITIVE"
        sensitive = true
      }
```
Appear in the corresponding outputs section as:
```yaml
  status:
    atProvider:
      outputs:
        bool: "false"
        number: "1.9"
        object: '{"a":3,"b":2}'
        string: bar
        tuple: '["foo", "bar"]'
```
Note that the "sensitive" output is not included in status.atProvider.outputs

## OpenTofu CLI Command Arguments
Additional arguments can be passed to the `tofu plan`, `tofu apply`, and `tofu destroy`
commands by specifying the `planArgs`, `applyArgs` and `destroyArgs` options.

For example:
```yaml
apiVersion: opentofu.upbound.io/v1beta1
kind: Workspace
metadata:
  name: example-args
spec:
  forProvider:
    # Run the opentofu init command with -upgrade=true to upgrade any stored providers
    initArgs:
      - -upgrade=true
    # Run the opentofu plan command with the -parallelism=2 argument
    planArgs:
      - -parallelism=2
    # Run the opentofu apply command with the -target=specificresource argument
    applyArgs:
      - -target=specificresource
    # Run the opentofu destroy command with the -refresh=false argument
    destroyArgs:
      - -refresh=false
    # Use any module source supported by opentofu init -from-module.
    source: Remote
    module: https://github.com/crossplane/tf
  # All outputs are written to the connection secret.
  writeConnectionSecretToRef:
    namespace: default
    name: opentofu-workspace-example-inline
```
This will cause the `tofu init` command to be run with the "-upgrade=true"
argument, the `tofu plan` command to be run with the -parallelism=2
argument, the `tofu apply` command to be run with the
-target=specificresource argument, and the `tofu destroy` command to be run
with the -refresh=false argument.

Note that by default the `tofu init` command is run with the
"-input=false", and "-no-color" arguments, the `tofu apply` and `tofu destroy`
commands are run with the "-no-color", "-auto-approve", and "-input=false"
arguments, and the `tofu plan` command is run with the "-no-color",
"-input=false", and "-detailed-exitcode" arguments.  Arguments specified in
`applyArgs`, `destroyArgs` and `planArgs` will be added to these default arguments.

## Custom Entrypoint for Terraform Invocation

In some cases, you might want to initialize and apply the terraform code in the
subdirectory of the repository checkout. It is most relevant for the cases when
your terraform modules contain inline [relative paths](#83).

To enable it, the `Workspace` spec has an **optional** `Entrypoint` field.

Consider this example:

```yml
apiVersion: opentofu.upbound.io/v1beta1
kind: Workspace
metadata:
  name: relative-path-test
spec:
  forProvider:
    module: git::https://github.com/crossplane/provider-opentofu-test-module.git
    source: Remote
    entrypoint: relative-path-iam
    vars:
      - key: iamRole
        value: relative-path-test
```

In this case, the whole repository will be checked out but terraform will be
initialized in the `relative-path-iam` subdirectory with the module that
contains relative path reference to the `iam` module located in the root of the
tree.

```HCL
module "relative-path-iam" {
  source  = "../iam"
  iamRole = var.iamRole
}
```

## Provider Plugin Cache (enabled by default)

[Provider Plugin Cache](https://opentofu.org/docs/cli/config/config-file/#provider-plugin-cache)
is enabled by default to speed up reconciliation.

In case you need to disable it, set optional `pluginCache` to `false` in
`ProviderConfig`:

```console
apiVersion: opentofu.upbound.io/v1beta1
kind: ProviderConfig
metadata:
  name: default
spec:
  pluginCache: false
...
```

Please note that disabling the plugin cache feature increases memory consumption significantly.
When disabled, a new set of providers is pulled for each workspace resource.
This then causes provider-opentofu to keep all of the providers in memory during reconciliation. 

## Enable External Secret Support

If you need to store the sensitive output to an external secret store like
Vault, you can specify the `--enable-external-secret-stores` flag to enable it:

```yaml
apiVersion: pkg.crossplane.io/v1alpha1
kind: ControllerConfig
metadata:
  name: opentofu-config
  labels:
    app: crossplane-provider-opentofu
spec:
  image: crossplane/provider-opentofu-controller:v0.1.0
  args:
    - -d
    - --enable-external-secret-stores
  metadata:
    annotations:
      vault.hashicorp.com/agent-inject: "true"
      vault.hashicorp.com/agent-inject-token: "true"
      vault.hashicorp.com/role: "crossplane"
      vault.hashicorp.com/agent-run-as-user: "2000"
```

Prepare a `StoreConfig` for Vault:
```yaml
apiVersion: opentofu.upbound.io/v1beta1
kind: StoreConfig
metadata:
  name: vault
spec:
  type: Vault
  defaultScope: crossplane-system
  vault:
    server: http://vault.vault-system:8200
    mountPath: secret/
    version: v2
    auth:
      method: Token
      token:
        source: Filesystem
        fs:
          path: /vault/secrets/token
```

Specify it in `spec.publishConnectionDetailsTo`:
```yaml
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: ...
  labels:
    feature: ess
spec:
  compositeTypeRef:
    apiVersion: ...
    kind: ...
  resources:
    - name: foo
      base:
        apiVersion: opentofu.upbound.io/v1beta1
        kind: Workspace
        metadata:
          name: foo
        spec:
          forProvider:
            ...
          publishConnectionDetailsTo:
            name: bar
            configRef:
              name: vault
```

At Vault side configuration is also needed to allow the write operation, see
[example](https://docs.crossplane.io/knowledge-base/integrations/vault-as-secret-store/)
here for inspiration.


## Enable OpenTofu CLI logs

OpenTofu CLI output can be written to the container logs to assist with debugging and to view detailed information about OpenTofu operations.
To enable it, the `Workspace` spec has an **optional** `EnableTofuCLILogging` field.
```yaml
apiVersion: opentofu.upbound.io/v1beta1
kind: Workspace
metadata:
  name: example-random-generator
  annotations:
    meta.upbound.io/example-id: opentofu/v1beta1/workspace
    crossplane.io/external-name: random
spec:
  forProvider:
    source: Inline
    enableTofuCLILogging: true
...
```

- `enableTofuCLILogging`: Specifies whether logging is enabled (`true`) or disabled (`false`). When enabled, OpenTofu CLI command output will be written to the container logs. Default is `false`

## Plan history

The provider runs `tofu plan` on every observation to decide whether the
workspace needs to be applied. By default that plan is discarded. Setting the
**optional** `writePlanToSecretRef` field keeps a history of plans as Secrets,
in a similar way to how Helm stores releases.

```yaml
apiVersion: opentofu.m.upbound.io/v1beta1
kind: Workspace
metadata:
  name: example-plan-history
  namespace: default
spec:
  forProvider:
    source: Inline
    module: |
      ...
    writePlanToSecretRef:
      name: example-plan
      historyLimit: 10
```

- `name`: base name of the Secrets. Revision `N` is stored in `<name>.vN`.
- `historyLimit`: maximum number of revisions to keep (default `10`). The
  oldest revisions are deleted first.
- `namespace`: namespace of the Secrets. Only for the cluster-scoped
  `opentofu.upbound.io` Workspace; the namespaced `opentofu.m.upbound.io`
  Workspace always uses its own namespace.

A new revision is only created when the plan changes, so periodic polls of an
unchanged workspace do not add revisions. Each revision is a Secret of type
`opentofu.upbound.io/plan.v1` with two keys:

- `plan.txt`: the human-readable plan (`tofu show`).
- `plan.json.gz`: the machine-readable plan (`tofu show -json`), gzipped, for
  example for policy checks with OPA/conftest. JSON plans include the whole
  state several times and compress very well, which keeps large workspaces
  within the Secret size limit.

Each revision is labelled with `opentofu.upbound.io/plan-revision` and
`opentofu.upbound.io/plan-status`, which is one of:

| Status       | Meaning                                                        |
|--------------|----------------------------------------------------------------|
| `pending`    | The plan has changes that have not been applied yet.           |
| `applied`    | The plan was applied (see the `applied-at` annotation).         |
| `failed`     | Applying the plan failed (see the `failed-at` annotation).      |
| `superseded` | A different plan was recorded before this one was applied.     |
| `no-op`      | The plan had no changes: the workspace had converged.          |

To list the history of a Workspace:

```console
$ kubectl get secret -l opentofu.upbound.io/workspace=example-plan-history \
    -L opentofu.upbound.io/plan-revision,opentofu.upbound.io/plan-status
NAME              TYPE                          DATA   AGE   PLAN-REVISION   PLAN-STATUS
example-plan.v1   opentofu.upbound.io/plan.v1   2      2h    1               applied
example-plan.v2   opentofu.upbound.io/plan.v1   2      2h    2               no-op
example-plan.v3   opentofu.upbound.io/plan.v1   2      5m    3               pending

$ kubectl get secret example-plan.v3 -o jsonpath='{.data.plan\.txt}' | base64 -d
$ kubectl get secret example-plan.v3 -o jsonpath='{.data.plan\.json\.gz}' | base64 -d | gunzip
```

The `opentofu.upbound.io/workspace` label is only set when the Workspace name
is a valid label value (63 characters or less). The
`opentofu.upbound.io/workspace-uid` label is always set.

### Reviewing a plan before it is applied

When the provider is allowed to apply, it plans and applies in the same
reconciliation, so a `pending` revision quickly becomes `applied`. To review a
plan first, set `managementPolicies: ["Observe"]` on the Workspace: plans are
recorded as `pending` but never applied. Once satisfied, set
`managementPolicies: ["*"]` to let the provider apply.

Note that the provider does not apply the saved plan file: `tofu apply` runs
its own plan a few seconds later. An `applied` revision means that the plan was
applied in the same reconciliation, not that the applied changes are
guaranteed to be byte-for-byte identical.

### Things to be aware of

- **Plans can contain secrets.** `plan.json.gz` contains sensitive values in
  clear text, and `plan.txt` can too, because OpenTofu does not always
  propagate sensitivity to derived attributes. Restrict who can read Secrets
  in the namespace where the history is stored.
- **Best effort.** Plan history never makes a Workspace fail. If a revision
  cannot be recorded, for example because of missing RBAC or a name collision
  with an existing Secret, the error is logged by the provider and the
  Workspace is reconciled as usual. If saving the plan makes `tofu plan` fail,
  for example with a backend that does not support saved plans, the provider
  runs a plain `tofu plan` instead.
- **`-out` in `planArgs`.** If `planArgs` already contains `-out`, plan
  history is disabled for that Workspace so that the requested plan file is
  not overridden.
- **Size.** Secrets are limited to 1MiB. If `plan.txt` and the compressed
  `plan.json.gz` do not fit, `plan.json.gz` is dropped (the revision is
  annotated with `opentofu.upbound.io/plan-json-omitted`) and `plan.txt` is
  truncated if needed.
- **Cost.** When enabled, every observation also runs `tofu show` twice to
  render the plan. The plan file is written to a temporary file in `/tmp`,
  outside of the workspace directory, and deleted right away.
- **Lifecycle.** Revisions are owned by the Workspace and deleted with it.
