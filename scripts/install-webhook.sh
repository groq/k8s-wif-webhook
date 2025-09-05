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
    
    # Create a kind cluster configuration with at least one worker node
    cat <<EOF | kind create cluster --name "$CLUSTER_NAME" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
- role: worker
EOF

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

# Function to create WIF configuration patch if environment variables are provided
create_wif_config() {
    if [[ -n "$WORKLOAD_IDENTITY_PROVIDER" && -n "$GOOGLE_CLOUD_PROJECT" ]]; then
        log "INFO" "Creating WIF configuration patch..."
        
        cat <<EOF > config/default/manager_wif_config_patch.yaml
# WIF Configuration Patch
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
        - name: WORKLOAD_IDENTITY_PROVIDER
          value: "$WORKLOAD_IDENTITY_PROVIDER"
        - name: GOOGLE_CLOUD_PROJECT
          value: "$GOOGLE_CLOUD_PROJECT"
EOF
        
        # Uncomment the WIF patch in kustomization.yaml
        sed -i.bak 's/# - path: manager_wif_config_patch.yaml/- path: manager_wif_config_patch.yaml/' config/default/kustomization.yaml
        
        log "SUCCESS" "WIF configuration patch created"
    else
        log "WARN" "WORKLOAD_IDENTITY_PROVIDER and/or GOOGLE_CLOUD_PROJECT not set, skipping WIF configuration"
    fi
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
    -w, --wif-provider WIF_PROVIDER        Workload Identity Provider path
    -p, --project GCP_PROJECT              GCP Project ID
    -h, --help                            Show this help message

ENVIRONMENT VARIABLES:
    CLUSTER_NAME                          Kind cluster name
    IMAGE_TAG                             Docker image tag
    WORKLOAD_IDENTITY_PROVIDER            WIF provider path
    GOOGLE_CLOUD_PROJECT                  GCP project ID

EXAMPLES:
    # Basic installation
    $0

    # Installation with custom cluster name and WIF config
    $0 --cluster-name my-cluster --wif-provider "projects/123/locations/global/workloadIdentityPools/pool/providers/provider" --project "my-project"

    # Installation using environment variables
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
    
    # Create WIF configuration if provided
    create_wif_config
    
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
    echo -e "2. Check if WIF configuration was injected:"
    echo -e "   ${BLUE}kubectl get pod test-pod -o yaml | grep -A 10 -B 10 GOOGLE_APPLICATION_CREDENTIALS${NC}"
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
