#!/usr/bin/env bash

set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
CLUSTER_NAME="${CLUSTER_NAME:-k8s-wif-webhook}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
NAMESPACE="${NAMESPACE:-workload-identity-system}"
WORKLOAD_IDENTITY_PROVIDER="${WORKLOAD_IDENTITY_PROVIDER:-}"
GOOGLE_CLOUD_PROJECT="${GOOGLE_CLOUD_PROJECT:-}"
WEBHOOK_MODE="${WEBHOOK_MODE:-development}"
DEV_CREDENTIALS_PATH="${DEV_CREDENTIALS_PATH:-$HOME/.config/gcloud/application_default_credentials.json}"

# Function to print colored output
log() {
    local level="$1"
    shift
    case "$level" in
        "INFO")  echo -e "${BLUE}[INFO]${NC} $*" ;;
        "WARN")  echo -e "${YELLOW}[WARN]${NC} $*" ;;
        "ERROR") echo -e "${RED}[ERROR]${NC} $*" ;;
        "SUCCESS") echo -e "${GREEN}[SUCCESS]${NC} $*" ;;
    esac
}

# Function to check if a command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Function to check if kind cluster exists
cluster_exists() {
    kind get clusters 2>/dev/null | grep -q "^${CLUSTER_NAME}$"
}

# Function to wait for pods to be ready
wait_for_pods() {
    local namespace="$1"
    local selector="$2"
    local timeout="${3:-300}"
    
    log "INFO" "Waiting for pods in namespace '$namespace' with selector '$selector' to be ready..."
    if kubectl wait --for=condition=ready pod -l "$selector" -n "$namespace" --timeout="${timeout}s"; then
        log "SUCCESS" "Pods are ready!"
    else
        log "ERROR" "Pods failed to become ready within ${timeout} seconds"
        return 1
    fi
}

# Function to create kind cluster
create_kind_cluster() {
    log "INFO" "Creating kind cluster: $CLUSTER_NAME"
    
    if [[ "$WEBHOOK_MODE" == "development" ]]; then
        # Create a kind cluster configuration with host path mounts for development mode
        log "INFO" "Configuring Kind cluster for development mode with gcloud credentials"
        cat <<EOF | kind create cluster --name "$CLUSTER_NAME" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraMounts:
  - hostPath: $(dirname "$DEV_CREDENTIALS_PATH")
    containerPath: /host-gcloud
    readOnly: true
- role: worker
  extraMounts:
  - hostPath: $(dirname "$DEV_CREDENTIALS_PATH")
    containerPath: /host-gcloud
    readOnly: true
- role: worker
  extraMounts:
  - hostPath: $(dirname "$DEV_CREDENTIALS_PATH")
    containerPath: /host-gcloud
    readOnly: true
EOF
    else
        # Create a standard kind cluster for production mode
        cat <<EOF | kind create cluster --name "$CLUSTER_NAME" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
- role: worker
EOF
    fi

    log "SUCCESS" "Kind cluster '$CLUSTER_NAME' created successfully"
}

# Function to install cert-manager
install_cert_manager() {
    log "INFO" "Installing cert-manager..."
    
    # Install cert-manager
    kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.16.3/cert-manager.yaml
    
    # Wait for cert-manager to be ready
    wait_for_pods "cert-manager" "app=cert-manager"
    wait_for_pods "cert-manager" "app=cainjector"
    wait_for_pods "cert-manager" "app=webhook"
    
    log "SUCCESS" "cert-manager installed and ready"
}

# Function to build and load docker image
build_and_load_image() {
    log "INFO" "Building webhook Docker image..."
    
    # Build the image
    make docker-build IMG="ghcr.io/groq/k8s-wif-webhook:${IMAGE_TAG}"
    
    # Load the image into kind cluster
    kind load docker-image "ghcr.io/groq/k8s-wif-webhook:${IMAGE_TAG}" --name "$CLUSTER_NAME"
    
    log "SUCCESS" "Docker image built and loaded into kind cluster"
}

# Function to deploy the webhook
deploy_webhook() {
    log "INFO" "Deploying k8s-wif-webhook..."
    
    # Generate manifests and ensure kustomize is available
    make manifests kustomize
    
    # Set the image in kustomization
    cd config/manager
    ../../bin/kustomize edit set image controller="ghcr.io/groq/k8s-wif-webhook:${IMAGE_TAG}"
    cd ../..
    
    # Build and apply the manifests
    ./bin/kustomize build config/default | kubectl apply -f -
    
    # Wait for the webhook deployment to be ready
    wait_for_pods "$NAMESPACE" "control-plane=controller-manager"
    
    log "SUCCESS" "k8s-wif-webhook deployed successfully"
}

# Function to create webhook configuration patch
create_webhook_config() {
    log "INFO" "Creating webhook configuration patch for mode: $WEBHOOK_MODE"
    
    cat <<EOF > config/default/manager_webhook_config_patch.yaml
# Webhook Configuration Patch
apiVersion: apps/v1
kind: Deployment
metadata:
  name: controller-manager
  namespace: system
spec:
  template:
    spec:
      containers:
      - name: manager
        env:
        - name: WEBHOOK_MODE
          value: "$WEBHOOK_MODE"
EOF

    if [[ "$WEBHOOK_MODE" == "production" ]]; then
        if [[ -n "$WORKLOAD_IDENTITY_PROVIDER" && -n "$GOOGLE_CLOUD_PROJECT" ]]; then
            cat <<EOF >> config/default/manager_webhook_config_patch.yaml
        - name: WORKLOAD_IDENTITY_PROVIDER
          value: "$WORKLOAD_IDENTITY_PROVIDER"
        - name: GOOGLE_CLOUD_PROJECT
          value: "$GOOGLE_CLOUD_PROJECT"
EOF
            log "SUCCESS" "Production WIF configuration added"
        else
            log "ERROR" "Production mode requires WORKLOAD_IDENTITY_PROVIDER and GOOGLE_CLOUD_PROJECT"
            return 1
        fi
    elif [[ "$WEBHOOK_MODE" == "development" ]]; then
        # Check if gcloud credentials exist
        if [[ ! -f "$DEV_CREDENTIALS_PATH" ]]; then
            log "ERROR" "Development credentials not found at $DEV_CREDENTIALS_PATH"
            log "ERROR" "Please run: gcloud auth application-default login"
            return 1
        fi
        
        cat <<EOF >> config/default/manager_webhook_config_patch.yaml
        - name: DEV_CREDENTIALS_PATH
          value: "/host-gcloud/application_default_credentials.json"
        volumeMounts:
        - name: gcloud-credentials
          mountPath: /host-gcloud
          readOnly: true
      volumes:
      - name: gcloud-credentials
        hostPath:
          path: /host-gcloud
          type: Directory
EOF
        log "SUCCESS" "Development mode configuration added"
        log "INFO" "Mounting host gcloud credentials from: $DEV_CREDENTIALS_PATH"
    fi
    
    # Add the patch to kustomization.yaml if not already present  
    if ! grep -q "manager_webhook_config_patch.yaml" config/default/kustomization.yaml; then
        # Create a backup and add the patch entry to the patches section
        cp config/default/kustomization.yaml config/default/kustomization.yaml.bak
        
        # Find the webhook patch line and add target + our config patch after it
        python3 -c "
import sys
lines = []
with open('config/default/kustomization.yaml', 'r') as f:
    lines = f.readlines()

output = []
for i, line in enumerate(lines):
    output.append(line)
    if 'manager_webhook_patch.yaml' in line and line.strip().startswith('- path:'):
        # Add target for the existing webhook patch
        output.append('  target:\n')
        output.append('    kind: Deployment\n')
        # Add our webhook config patch
        output.append('# [WEBHOOK_CONFIG] Webhook configuration patch\n')
        output.append('- path: manager_webhook_config_patch.yaml\n')
        output.append('  target:\n')
        output.append('    kind: Deployment\n')

with open('config/default/kustomization.yaml', 'w') as f:
    f.writelines(output)
"
    fi
    
    log "SUCCESS" "Webhook configuration patch created"
}

# Function to verify installation
verify_installation() {
    log "INFO" "Verifying webhook installation..."
    
    # Check if the webhook deployment is running
    if kubectl get deployment webhook-controller-manager -n "$NAMESPACE" >/dev/null 2>&1; then
        local ready_replicas
        ready_replicas=$(kubectl get deployment webhook-controller-manager -n "$NAMESPACE" -o jsonpath='{.status.readyReplicas}')
        if [[ "$ready_replicas" == "1" ]]; then
            log "SUCCESS" "Webhook deployment is running with 1 ready replica"
        else
            log "ERROR" "Webhook deployment is not ready (ready replicas: $ready_replicas)"
            return 1
        fi
    else
        log "ERROR" "Webhook deployment not found"
        return 1
    fi
    
    # Check if the MutatingWebhookConfiguration exists
    if kubectl get mutatingwebhookconfiguration webhook-workload-identity-injection >/dev/null 2>&1; then
        log "SUCCESS" "MutatingWebhookConfiguration found"
    else
        log "ERROR" "MutatingWebhookConfiguration not found"
        return 1
    fi
    
    log "SUCCESS" "Installation verification completed successfully"
}

# Function to print usage
usage() {
    cat <<EOF
Usage: $0 [OPTIONS]

Install k8s-wif-webhook in a kind cluster.

OPTIONS:
    -n, --cluster-name CLUSTER_NAME        Name of the kind cluster (default: k8s-wif-webhook)
    -t, --image-tag IMAGE_TAG              Docker image tag (default: latest)
    -w, --wif-provider WIF_PROVIDER        Workload Identity Provider path (production mode)
    -p, --project GCP_PROJECT              GCP Project ID (production mode)
    -m, --mode MODE                        Webhook mode: development (default) or production
    -d, --dev-credentials DEV_PATH         Path to gcloud credentials (development mode)
    -h, --help                            Show this help message

ENVIRONMENT VARIABLES:
    CLUSTER_NAME                          Kind cluster name
    IMAGE_TAG                             Docker image tag
    WEBHOOK_MODE                          Webhook mode (development or production)
    WORKLOAD_IDENTITY_PROVIDER            WIF provider path (production mode)
    GOOGLE_CLOUD_PROJECT                  GCP project ID (production mode)
    DEV_CREDENTIALS_PATH                  Path to gcloud credentials (development mode)

EXAMPLES:
    # Basic installation
    $0

    # Development mode (default)
    $0

    # Production mode with WIF configuration
    $0 --mode production --wif-provider "projects/123/locations/global/workloadIdentityPools/pool/providers/provider" --project "my-project"

    # Development mode with custom credentials path
    $0 --mode development --dev-credentials "/path/to/custom/credentials.json"

    # Using environment variables for production
    WEBHOOK_MODE="production" \\
    WORKLOAD_IDENTITY_PROVIDER="projects/123/locations/global/workloadIdentityPools/pool/providers/provider" \\
    GOOGLE_CLOUD_PROJECT="my-project" \\
    $0

EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        -n|--cluster-name)
            CLUSTER_NAME="$2"
            shift 2
            ;;
        -t|--image-tag)
            IMAGE_TAG="$2"
            shift 2
            ;;
        -w|--wif-provider)
            WORKLOAD_IDENTITY_PROVIDER="$2"
            shift 2
            ;;
        -p|--project)
            GOOGLE_CLOUD_PROJECT="$2"
            shift 2
            ;;
        -m|--mode)
            WEBHOOK_MODE="$2"
            shift 2
            ;;
        -d|--dev-credentials)
            DEV_CREDENTIALS_PATH="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            log "ERROR" "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

# Main installation function
main() {
    log "INFO" "Starting k8s-wif-webhook installation..."
    log "INFO" "Cluster name: $CLUSTER_NAME"
    log "INFO" "Image tag: $IMAGE_TAG"
    log "INFO" "Webhook mode: $WEBHOOK_MODE"
    
    # Check required tools
    local missing_tools=()
    for tool in kind kubectl docker make; do
        if ! command_exists "$tool"; then
            missing_tools+=("$tool")
        fi
    done
    
    if [[ ${#missing_tools[@]} -gt 0 ]]; then
        log "ERROR" "Missing required tools: ${missing_tools[*]}"
        log "ERROR" "Please install the missing tools and try again"
        exit 1
    fi
    
    # Check if cluster exists, create if it doesn't
    if cluster_exists; then
        log "INFO" "Kind cluster '$CLUSTER_NAME' already exists, using existing cluster"
        kubectl cluster-info --context="kind-$CLUSTER_NAME"
    else
        create_kind_cluster
    fi
    
    # Set kubectl context
    kubectl config use-context "kind-$CLUSTER_NAME"
    
    # Install cert-manager (required for webhook certificates)
    install_cert_manager
    
    # Create webhook configuration
    create_webhook_config
    
    # Build and load the webhook image
    build_and_load_image
    
    # Deploy the webhook
    deploy_webhook
    
    # Verify installation
    verify_installation
    
    log "SUCCESS" "k8s-wif-webhook installation completed successfully!"
    
    # Print next steps
    echo ""
    echo -e "${GREEN}Next Steps:${NC}"
    echo -e "1. Test the webhook by creating a test pod:"
    echo -e "   ${BLUE}kubectl run test-pod --image=nginx --restart=Never${NC}"
    echo ""
    if [[ "$WEBHOOK_MODE" == "development" ]]; then
        echo -e "2. Check if development credentials were injected:"
        echo -e "   ${BLUE}kubectl get pod test-pod -o yaml | grep -A 10 -B 10 GOOGLE_APPLICATION_CREDENTIALS${NC}"
        echo ""
        echo -e "   Expected path: ${BLUE}/var/secrets/google/application_default_credentials.json${NC}"
    else
        echo -e "2. Check if WIF configuration was injected:"
        echo -e "   ${BLUE}kubectl get pod test-pod -o yaml | grep -A 10 -B 10 GOOGLE_APPLICATION_CREDENTIALS${NC}"
        echo ""
        echo -e "   Expected path: ${BLUE}/etc/workload-identity/credentials.json${NC}"
    fi
    echo ""
    echo -e "3. Clean up test pod:"
    echo -e "   ${BLUE}kubectl delete pod test-pod${NC}"
    echo ""
    echo -e "4. To uninstall the webhook:"
    echo -e "   ${BLUE}make undeploy${NC}"
    echo ""
    echo -e "5. To delete the kind cluster:"
    echo -e "   ${BLUE}kind delete cluster --name $CLUSTER_NAME${NC}"
    echo ""
}

# Run main function
main "$@"
