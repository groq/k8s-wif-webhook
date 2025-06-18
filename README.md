# Kubernetes Workload Identity Federation Webhook

A Kubernetes admission webhook that automatically injects Google Cloud Workload Identity Federation (WIF) configuration into pods, eliminating the need for service account keys.

## Overview

This webhook transparently configures pods to use WIF for Google Cloud authentication by:
- Injecting projected ServiceAccount tokens as volumes
- Setting the `GOOGLE_APPLICATION_CREDENTIALS` environment variable
- Configuring WIF provider endpoints

## Features

- **Zero-touch WIF injection**: Automatically configures all pods by default
- **Opt-out model**: Use annotations to disable injection when needed
- **Two WIF modes**: Direct federated identity or service account impersonation
- **Environment-driven config**: Configure via environment variables
- **Minimal overhead**: Lightweight admission controller with fast processing

## Configuration

The webhook is configured via environment variables:

- `WORKLOAD_IDENTITY_PROVIDER`: WIF provider path (e.g., `projects/123/locations/global/workloadIdentityPools/pool/providers/provider`)
- `GOOGLE_CLOUD_PROJECT`: GCP project ID for workload identity

## Opt-out

To disable WIF injection:

**Namespace-level** (all pods in namespace):
```yaml
metadata:
  annotations:
    workload-identity.io/injection: "disabled"
```

**Pod-level**:
```yaml
metadata:
  annotations:
    workload-identity.io/inject: "false"
```

## Service Account Impersonation

For service account impersonation mode, add:
```yaml
metadata:
  annotations:
    iam.gke.io/gcp-service-account: "my-service-account@project.iam.gserviceaccount.com"
```

## Deployment

The webhook is distributed as a container image:

```yaml
image: ghcr.io/fujin/k8s-wif-webhook:latest
```

See the [releases page](https://github.com/fujin/k8s-wif-webhook/releases) for specific versions.

## License

Apache 2.0
