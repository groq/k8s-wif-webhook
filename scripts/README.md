# Installation Scripts

This directory contains scripts for installing and managing the k8s-wif-webhook.

## install-webhook.sh

A comprehensive script that installs the k8s-wif-webhook in a kind cluster.

### Features

- ✅ Automatically creates a kind cluster with worker nodes if one doesn't exist
- ✅ Installs cert-manager (required for webhook TLS certificates)
- ✅ Builds and loads the webhook Docker image into the kind cluster
- ✅ Deploys the webhook using kustomize configurations
- ✅ Configurable WIF settings via environment variables or CLI arguments
- ✅ Comprehensive verification of the installation
- ✅ Colored output for better readability

### Prerequisites

The following tools must be installed and available in your PATH:
- `kind` - for managing Kubernetes clusters
- `kubectl` - for interacting with Kubernetes
- `docker` - for building container images
- `make` - for running build targets

### Usage

#### Basic Installation

```bash
./scripts/install-webhook.sh
```

This will:
1. Create a kind cluster named `k8s-wif-webhook` (if it doesn't exist)
2. Install cert-manager
3. Build and deploy the webhook

#### Installation with WIF Configuration

```bash
./scripts/install-webhook.sh \
  --wif-provider "projects/123/locations/global/workloadIdentityPools/pool/providers/provider" \
  --project "my-gcp-project"
```

#### Using Environment Variables

```bash
export WORKLOAD_IDENTITY_PROVIDER="projects/123/locations/global/workloadIdentityPools/pool/providers/provider"
export GOOGLE_CLOUD_PROJECT="my-gcp-project"
export CLUSTER_NAME="my-webhook-cluster"
./scripts/install-webhook.sh
```

### Options

| Option | Environment Variable | Description | Default |
|--------|---------------------|-------------|---------|
| `-n, --cluster-name` | `CLUSTER_NAME` | Name of the kind cluster | `k8s-wif-webhook` |
| `-t, --image-tag` | `IMAGE_TAG` | Docker image tag | `latest` |
| `-w, --wif-provider` | `WORKLOAD_IDENTITY_PROVIDER` | WIF provider path | (empty) |
| `-p, --project` | `GOOGLE_CLOUD_PROJECT` | GCP project ID | (empty) |
| `-h, --help` | - | Show help message | - |

### Examples

#### Development Setup

```bash
# Create a development cluster with custom name
./scripts/install-webhook.sh --cluster-name dev-webhook
```

#### Production-like Setup with WIF

```bash
# Full setup with WIF configuration
WORKLOAD_IDENTITY_PROVIDER="projects/my-project/locations/global/workloadIdentityPools/my-pool/providers/my-provider" \
GOOGLE_CLOUD_PROJECT="my-project" \
./scripts/install-webhook.sh --cluster-name prod-webhook
```

### Testing the Installation

After installation, test the webhook:

```bash
# Create a test pod
kubectl run test-pod --image=nginx --restart=Never

# Check if WIF configuration was injected
kubectl get pod test-pod -o yaml | grep -A 10 -B 10 GOOGLE_APPLICATION_CREDENTIALS

# Clean up
kubectl delete pod test-pod
```

### Troubleshooting

#### Check webhook logs
```bash
kubectl logs -n workload-identity-system deployment/webhook-controller-manager
```

#### Check webhook configuration
```bash
kubectl get mutatingwebhookconfiguration webhook-workload-identity-injection -o yaml
```

#### Verify certificates
```bash
kubectl get certificate -n workload-identity-system
kubectl get secret -n workload-identity-system
```

### Cleanup

To remove the webhook:
```bash
make undeploy
```

To delete the entire kind cluster:
```bash
kind delete cluster --name k8s-wif-webhook
```

## Script Structure

The installation script follows these steps:

1. **Validation** - Check required tools and parse arguments
2. **Cluster Setup** - Create kind cluster with worker nodes if needed
3. **cert-manager** - Install and wait for cert-manager to be ready
4. **WIF Configuration** - Create configuration patches if WIF settings provided
5. **Build & Load** - Build webhook image and load into kind cluster
6. **Deploy** - Use kustomize to deploy all webhook components
7. **Verification** - Confirm webhook is running and configured correctly

The script provides detailed logging throughout the process and will exit on any errors.
