# OpenTofu Crossplane Provider

Provider OpenTofu is a [Crossplane](https://crossplane.io/) provider that
can run Terraform code (HCL), using the OpenTofu fork, and enables defining new Crossplane Composite Resources (XRs)
that are composed of a mix of 'native' Crossplane managed resources and your
existing Terraform modules.

The provider adds support for a `Workspace` managed resource that
represents an OpenTofu workspace. The configuration of each workspace may be
either fetched from a remote source (e.g. git), or simply specified inline.

```yaml
apiVersion: opentofu.upbound.io/v1beta1
kind: Workspace
metadata:
  name: example-inline
  annotations:
    # The workspace will be named 'coolbucket'. If you omitted this
    # annotation it would be derived from metadata.name - i.e. 'example-inline'.
    crossplane.io/external-name: coolbucket
spec:
  forProvider:
    # For simple cases you can use an inline source to specify the content of
    # main.tf as opaque, inline HCL.
    source: Inline
    module: |
      // All outputs are written to the connection secret.  Non-sensitive outputs
      // are stored in the status.atProvider.outputs object.
      output "url" {
        value       = google_storage_bucket.example.self_link
      }

      resource "random_id" "example" {
        byte_length = 4
      }

      // The google provider and remote state are configured by the provider
      // config - see examples/providerconfig.yaml.
      resource "google_storage_bucket" "example" {
        name = "crossplane-example-${opentofu.workspace}-${random_id.example.hex}"
      }
  writeConnectionSecretToRef:
    namespace: default
    name: opentofu-workspace-example-inline
```

```yaml
apiVersion: opentofu.upbound.io/v1beta1
kind: Workspace
metadata:
  name: example-remote
  annotations:
    crossplane.io/external-name: myworkspace
spec:
  forProvider:
    # Use any module source supported by tofu init -from-module. 
    source: Remote
    module: https://github.com/upbound/tf-example
    # Environment variables can be passed through
    env:
      - name: TF_VAR_varFromValue
        value: 'value'
      - name: ENV_FROM_CONFIGMAP
        configMapKeyRef:
          namespace: my-namespace
          name: my-config-map
          key: target-key
      - name: ENV_FROM_SECRET
        secretKeyRef:
          namespace: my-namespace
          name: my-secret
          key: target-key
    # Variables can be specified inline as a list of key-value pairs or as an json object, or loaded from a ConfigMap or Secret.
    vars:
    - key: region
      value: us-west-1
    varmap:
      account:
        region: us-west-1
        owners:
        - example-owner-1
        - example-owner-2
    varFiles:
    - source: SecretKey
      secretKeyRef:
        namespace: default
        name: opentofu
        key: example.tfvar.json
  # All outputs are written to the connection secret.
  writeConnectionSecretToRef:
    namespace: default
    name: opentofu-workspace-example-inline
```

## Getting Started

<!-- TODO Update link -->
Follow the quick start guide [here](https://marketplace.upbound.io/providers/upbound/provider-opentofu/latest/docs/quickstart).

<!-- TODO Update link -->
You can find a detailed API reference for all the managed resources with examples in the [Upbound Marketplace](https://marketplace.upbound.io/providers/upbound/provider-opentofu/latest/managed-resources).

## Further Configuration

<!-- TODO Update link -->
You can find more information about configuring the provider further [here](https://marketplace.upbound.io/providers/upbound/provider-opentofu/latest/docs/configuration).

### Polling Interval
The default polling interval has been updated to 10 minutes from 1 minute.
This affects how often the provider will run `tofu plan` on existing
`Workspaces` to determine if there are any resources out of sync and whether
`tofu apply` needs to be re-executed to recover the desired state.
A 1-minute polling interval is often too short when the time required for
running `tofu init`, `tofu plan` and `tofu apply` is taken
into account.  Workspaces with large numbers of resources can take longer
than 1 minute to run `tofu plan`.  Changes to the `Workspace` object
`spec` will still be reconciled immediately.  The poll interval is
configurable using `DeploymentRuntimeConfig`.

## Known limitations:

* You must either use remote state or ensure the provider container's `/tf`
  directory is not lost. `provider-opentofu` __does not persist state__;
  consider using the [Kubernetes](https://opentofu.org/docs/language/settings/backends/kubernetes/) remote state backend.
* If the module takes longer than the value of `--timeout` (default is 20m) to apply the
  underlying `tofu` process will be killed. You will potentially lose state
  and leak resources.  The workspace lock will also likely be left in place and need to be manually removed
  before the Workspace can be reconciled again.
* The provider won't emit an event until _after_ it has successfully applied the
  module, which can take a long time.
* Setting `--max-reconcile-rate` to a value greater than 1 will potentially cause the provider
  to use up to the same number of CPUs.  Add a resources section to the `DeploymentRuntimeConfig` to restrict
  CPU usage as needed.