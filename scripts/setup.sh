#!/bin/bash

# A script to build, push, and deploy the telera-knowledge services (Phase 1 API-First Architecture).

# Exit immediately if a command exits with a non-zero status.
set -e

# Resolve repo paths
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
GO_SERVICES_DIR="${REPO_ROOT}/services"

cd "${REPO_ROOT}"

# --- CONFIGURATION ---
HELM_RELEASE_NAME="telera-knowledge"
HELM_NAMESPACE="telera-knowledge"
HELM_CHART_PATH="helm"

# New Phase 1 services (API-first architecture)
SERVICES=(
    "knowledge-service"     # Unified ingestion service
    "search-service"        # Consolidated search/query/graph/rag service
    "indexing-service"      # Indexing service
    # Legacy services (disabled by default but available for migration)
    # "code-embedding"
    # "image-embedding"
    # "tabular-embedding"
    # "video-embedding"
    # "unstructured-parsing-service"
    # "dispatcher"
)

# --- HELPER FUNCTIONS ---
info() {
    echo
    echo "#################################################################"
    echo "## $1"
    echo "#################################################################"
    echo
}

error() {
    echo "ERROR: $1" >&2
    exit 1
}

# --- DEFAULTS ---
BUILD_ENABLED=true
PUSH_ENABLED=true
TARGET_ENV="local"
REPO_URL=""
CLEAN=false
VERBOSE=false
NO_CACHE_FLAG=""

usage() {
    echo "Usage: $0 [options]"
    echo
    echo "This script builds and deploys the telera-knowledge services (Phase 1 API-First)."
    echo
    echo "Options:"
    echo "  --env <env>             Target environment: 'local', 'aws', 'gcp', 'azure'. (Default: local)"
    echo "  --repo <url>            Container repository URL. Required for non-local environments."
    echo "  --no-build              Skip the 'docker build' step."
    echo "  --no-push               Skip the 'docker push' or 'minikube image load' step."
    echo "  --clean                 Clean build artifacts before building."
    echo "  --verbose               Enable verbose output."
    echo "  --no-cache              Skip docker build cache."
    echo "  -h, --help              Display this help message."
    echo
    echo "Phase 1 Services:"
    echo "  - knowledge-service     Unified data ingestion with API-based embeddings"
    echo "  - search-service        Consolidated search/query/graph/rag functionality"
    echo
    exit 1
}

# --- ARGUMENT PARSING ---
while [[ "$#" -gt 0 ]]; do
    case $1 in
        --env) TARGET_ENV="$2"; shift ;;
        --repo) REPO_URL="$2"; shift ;;
        --no-build) BUILD_ENABLED=false ;;
        --no-push) PUSH_ENABLED=false ;;
        --clean) CLEAN=true ;;
        --verbose) VERBOSE=true ;;
        --no-cache) NO_CACHE_FLAG="--no-cache" ;;
        -h|--help) usage ;;
        *) echo "Unknown parameter passed: $1"; usage ;;
    esac
    shift
done

# --- VALIDATION ---
if [[ "$TARGET_ENV" != "local" ]] && [[ -z "$REPO_URL" ]]; then
    echo "Error: --repo <url> is required for environment '$TARGET_ENV'."
    usage
fi

# --- SCRIPT BODY ---

# Set appropriate repo URL based on environment
if [ "$TARGET_ENV" = "local" ]; then
    # For local deployment, use simple local prefix
    if [[ -z "$REPO_URL" ]]; then
        REPO_URL="telera"  # Default fallback
    fi
fi

# Clean build artifacts if requested
if [ "$CLEAN" = true ]; then
    info "Cleaning build artifacts"
    rm -rf */target/ */__pycache__/ */.pytest_cache/
fi

# 1. Build Docker Images
if [ "$BUILD_ENABLED" = true ]; then
    info "Building Phase 1 Knowledge Services"
    
    for service in "${SERVICES[@]}"; do
        echo "--> Building service: $service"
        
        # Convert service name to directory path
        service_dir_rel="services/${service}"
        service_dir="${GO_SERVICES_DIR}/${service}"
        
        if [ -d "$service_dir" ]; then
            IMAGE_NAME="telera/${service}"
            echo "--> Building Docker image: $IMAGE_NAME:latest"
            
            dockerfile_path="${service_dir}/Dockerfile"
            if [ ! -f "$dockerfile_path" ]; then
                error "No Dockerfile found for $service at $dockerfile_path"
            fi

            echo "Building $service ($IMAGE_NAME)..."
            echo "Context: $GO_SERVICES_DIR"
            echo "Dockerfile: $service_dir_rel/Dockerfile"

            # Build the image
            if [ "$VERBOSE" = true ]; then
                echo "docker build --ssh default ${NO_CACHE_FLAG} -t \"$IMAGE_NAME:latest\" -f \"$dockerfile_path\" \"$GO_SERVICES_DIR\""
            fi
            
            if ! docker build --ssh default ${NO_CACHE_FLAG} -t "$IMAGE_NAME:latest" -f "$dockerfile_path" "$GO_SERVICES_DIR"; then
                error "Failed to build $service"
            fi
            
            echo "✅ Successfully built $IMAGE_NAME:latest"
        else
            echo "⚠️  Warning: Service directory $service_dir not found, skipping..."
        fi
    done
fi

# 2. Push or Load Docker Images
if [ "$PUSH_ENABLED" = true ]; then
    info "Processing images for environment: $TARGET_ENV"
    if [ "$TARGET_ENV" = "local" ]; then
        info "Loading images into Minikube"
        for service in "${SERVICES[@]}"; do
            service_dir="${GO_SERVICES_DIR}/${service}"
            if [ -d "$service_dir" ]; then
                IMAGE_NAME="telera/${service}"
                echo "--> Loading $IMAGE_NAME:latest into Minikube..."
                if ! minikube image load "$IMAGE_NAME:latest"; then
                    echo "⚠️  Warning: Failed to load $IMAGE_NAME into Minikube"
                fi
            fi
        done
    else
        info "Pushing images to remote repository: $REPO_URL"
        echo "--> NOTE: This script assumes you have already logged into your container registry."
        echo "    (e.g., via 'aws ecr get-login-password', 'gcloud auth configure-docker', or 'az acr login')"

        for service in "${SERVICES[@]}"; do
            service_dir="${GO_SERVICES_DIR}/${service}"
            if [ -d "$service_dir" ]; then
                LOCAL_IMAGE_NAME="telera/${service}"
                REMOTE_IMAGE_NAME="${REPO_URL}/${service}"

                echo "--> Tagging $LOCAL_IMAGE_NAME:latest as $REMOTE_IMAGE_NAME:latest"
                docker tag "$LOCAL_IMAGE_NAME:latest" "$REMOTE_IMAGE_NAME:latest"
                
                echo "--> Pushing $REMOTE_IMAGE_NAME:latest..."
                if ! docker push "$REMOTE_IMAGE_NAME:latest"; then
                    error "Failed to push $REMOTE_IMAGE_NAME:latest"
                fi
            fi
        done
    fi
else
    info "Skipping image push/load step (--no-push)"
fi

# 3. Deploy Helm Chart
if [ -d "$HELM_CHART_PATH" ]; then
    info "Deploying Helm chart for Phase 1 Knowledge Services"
    VALUES_FILE="${HELM_CHART_PATH}/values.yaml"
    
    # Use environment-specific values if available
    if [ "$TARGET_ENV" == "local" ] || [ "$TARGET_ENV" == "staging" ]; then
        ENV_VALUES_FILE="${HELM_CHART_PATH}/values-${TARGET_ENV}.yaml"
        if [ -f "$ENV_VALUES_FILE" ]; then
            VALUES_FILE="$ENV_VALUES_FILE"
            info "Using environment-specific values file: $VALUES_FILE"
        else
            info "WARNING: No specific values file found for '$TARGET_ENV' at '$ENV_VALUES_FILE'. Using default 'values.yaml'."
        fi
    elif [ -f "${HELM_CHART_PATH}/values.yaml" ]; then
        VALUES_FILE="${HELM_CHART_PATH}/values.yaml"
        info "Using production values file: $VALUES_FILE"
    fi

    HELM_OPTS=""
    if [[ "$TARGET_ENV" != "local" ]]; then
        # This assumes your Helm chart uses a global.imageRegistry value
        HELM_OPTS="--set global.imageRegistry=${REPO_URL}"
    fi

    echo "--> Using values file: $VALUES_FILE"
    echo "--> Deploying to namespace: $HELM_NAMESPACE"

    # Deploy the telera-knowledge chart
    if ! helm upgrade --install "$HELM_RELEASE_NAME" "$HELM_CHART_PATH" \
        --namespace "$HELM_NAMESPACE" \
        --create-namespace \
        -f "$VALUES_FILE" \
        --timeout 600s \
        --wait \
        $HELM_OPTS; then
        error "Helm deployment failed"
    fi

    info "Helm deployment completed successfully"
else
    info "No Helm chart found at $HELM_CHART_PATH, skipping deployment"
fi

info "Phase 1 Knowledge Services deployment completed successfully!"
echo
echo "🚀 New Architecture Summary:"
echo "  ✅ Knowledge Service:  Unified ingestion with API-based embeddings"
echo "  ✅ Search Service:     Consolidated search/query/graph/rag functionality"
echo
echo "📡 Key Benefits:"
echo "  • API-first embedding providers (Voyage AI, OpenAI, Google, Twelve Labs)"
echo "  • Reduced service complexity (2 services vs 9 microservices)"
echo "  • No local model hosting requirements"
echo "  • Backward compatibility via /beta/ endpoints"
echo
echo "🔗 Access the services via the gateway at /v1/knowledge and /v1/search"