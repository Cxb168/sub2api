#!/bin/bash
# =============================================================================
# Sub2API Docker Deployment Preparation Script
# =============================================================================
# This script prepares deployment files for Sub2API:
#   - Uses the compose/env files from this repository when run inside the repo,
#     otherwise downloads them from the configured GitHub repository
#   - Generates secure secrets (JWT_SECRET, TOTP_ENCRYPTION_KEY, POSTGRES_PASSWORD)
#   - Creates necessary data directories
#
# After running this script, you can start services with:
#   docker compose up -d                                                             # prebuilt image
#   docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build    # build from source (--build)
#
# Environment overrides:
#   SUB2API_REPO    GitHub owner/repo used when files must be downloaded
#                   (default: Cxb168/sub2api)
#   SUB2API_REF     branch/tag to download from (default: main)
#   SUB2API_IMAGE   image reference written into .env
#                   (default: ghcr.io/cxb168/sub2api:latest, or sub2api:local with --build)
#   GITHUB_RAW_URL  fully override the raw-content base URL
# =============================================================================

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

SUB2API_REPO="${SUB2API_REPO:-Cxb168/sub2api}"
SUB2API_REF="${SUB2API_REF:-main}"
# GitHub raw content base URL
GITHUB_RAW_URL="${GITHUB_RAW_URL:-https://raw.githubusercontent.com/${SUB2API_REPO}/${SUB2API_REF}/deploy}"

BUILD_FROM_SOURCE=0
for arg in "$@"; do
    case "$arg" in
        --build) BUILD_FROM_SOURCE=1 ;;
        -h|--help)
            echo "Usage: $0 [--build]"
            echo "  --build    write SUB2API_IMAGE=sub2api:local and print the build+up command"
            exit 0
            ;;
    esac
done

# Print colored message
print_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Generate random secret
generate_secret() {
    openssl rand -hex 32
}

# Check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Download a file with curl or wget
download_file() {
    local remote_path="$1"
    local target="$2"
    if command_exists curl; then
        curl -sSL "${GITHUB_RAW_URL}/${remote_path}" -o "$target"
    elif command_exists wget; then
        wget -q "${GITHUB_RAW_URL}/${remote_path}" -O "$target"
    else
        print_error "Neither curl nor wget is installed. Please install one of them."
        exit 1
    fi
}

# 本地仓库模式：脚本所在的 deploy/ 目录里就有 compose 与 env 模板时，直接复制本仓库文件，
# 保证部署内容与所克隆的版本一致（不需要联网，也不会误取其他分支的文件）。
LOCAL_DEPLOY_DIR=""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -f "${SCRIPT_DIR}/docker-compose.local.yml" ] && [ -f "${SCRIPT_DIR}/.env.example" ]; then
    LOCAL_DEPLOY_DIR="${SCRIPT_DIR}"
fi

# Main installation function
main() {
    echo ""
    echo "=========================================="
    echo "  Sub2API Deployment Preparation"
    echo "=========================================="
    echo ""

    # Check if openssl is available
    if ! command_exists openssl; then
        print_error "openssl is not installed. Please install openssl first."
        exit 1
    fi

    # Check if deployment already exists
    if [ -f "docker-compose.yml" ] && [ -f ".env" ]; then
        print_warning "Deployment files already exist in current directory."
        read -p "Overwrite existing files? (y/N): " -r
        echo
        if [[ ! $REPLY =~ ^[Yy]$ ]]; then
            print_info "Cancelled."
            exit 0
        fi
    fi

    if [ -n "${LOCAL_DEPLOY_DIR}" ]; then
        # 本地仓库模式：复制仓库内的 compose / env 模板
        print_info "Using deployment files from this repository (${LOCAL_DEPLOY_DIR})..."
        cp "${LOCAL_DEPLOY_DIR}/docker-compose.local.yml" docker-compose.yml
        cp "${LOCAL_DEPLOY_DIR}/.env.example" .env.example
        if [ -f "${LOCAL_DEPLOY_DIR}/docker-compose.build.yml" ]; then
            cp "${LOCAL_DEPLOY_DIR}/docker-compose.build.yml" docker-compose.build.yml
        fi
        print_success "Copied docker-compose.yml / .env.example"
    else
        # Download docker-compose.local.yml and save as docker-compose.yml
        print_info "Downloading docker-compose.yml from ${SUB2API_REPO}@${SUB2API_REF}..."
        download_file "docker-compose.local.yml" docker-compose.yml
        print_success "Downloaded docker-compose.yml"

        # Download .env.example
        print_info "Downloading .env.example..."
        download_file ".env.example" .env.example
        print_success "Downloaded .env.example"
    fi

    # Generate .env file with auto-generated secrets
    print_info "Generating secure secrets..."
    echo ""

    # Generate secrets
    JWT_SECRET=$(generate_secret)
    TOTP_ENCRYPTION_KEY=$(generate_secret)
    POSTGRES_PASSWORD=$(generate_secret)

    # Create .env from .env.example
    cp .env.example .env

    # Update .env with generated secrets (cross-platform compatible)
    if sed --version >/dev/null 2>&1; then
        # GNU sed (Linux)
        sed -i "s/^JWT_SECRET=.*/JWT_SECRET=${JWT_SECRET}/" .env
        sed -i "s/^TOTP_ENCRYPTION_KEY=.*/TOTP_ENCRYPTION_KEY=${TOTP_ENCRYPTION_KEY}/" .env
        sed -i "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=${POSTGRES_PASSWORD}/" .env
    else
        # BSD sed (macOS)
        sed -i '' "s/^JWT_SECRET=.*/JWT_SECRET=${JWT_SECRET}/" .env
        sed -i '' "s/^TOTP_ENCRYPTION_KEY=.*/TOTP_ENCRYPTION_KEY=${TOTP_ENCRYPTION_KEY}/" .env
        sed -i '' "s/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=${POSTGRES_PASSWORD}/" .env
    fi

    # 镜像来源：默认使用预构建镜像；--build 时改为本地构建（无需外部镜像仓库）
    if [ "${BUILD_FROM_SOURCE}" = "1" ]; then
        TARGET_IMAGE="sub2api:local"
    else
        TARGET_IMAGE="${SUB2API_IMAGE:-ghcr.io/cxb168/sub2api:latest}"
    fi
    if grep -q '^SUB2API_IMAGE=' .env; then
        if sed --version >/dev/null 2>&1; then
            sed -i "s|^SUB2API_IMAGE=.*|SUB2API_IMAGE=${TARGET_IMAGE}|" .env
        else
            sed -i '' "s|^SUB2API_IMAGE=.*|SUB2API_IMAGE=${TARGET_IMAGE}|" .env
        fi
    else
        printf '\nSUB2API_IMAGE=%s\n' "${TARGET_IMAGE}" >> .env
    fi

    # Create data directories
    print_info "Creating data directories..."
    mkdir -p data postgres_data redis_data
    print_success "Created data directories"

    # Set secure permissions for .env file (readable/writable only by owner)
    chmod 600 .env
    echo ""

    # Display completion message
    echo "=========================================="
    echo "  Preparation Complete!"
    echo "=========================================="
    echo ""
    echo "Generated secure credentials:"
    echo "  POSTGRES_PASSWORD:     ${POSTGRES_PASSWORD}"
    echo "  JWT_SECRET:            ${JWT_SECRET}"
    echo "  TOTP_ENCRYPTION_KEY:   ${TOTP_ENCRYPTION_KEY}"
    echo ""
    print_warning "These credentials have been saved to .env file."
    print_warning "Please keep them secure and do not share publicly!"
    echo ""
    echo "Directory structure:"
    echo "  docker-compose.yml        - Docker Compose configuration"
    echo "  .env                      - Environment variables (generated secrets)"
    echo "  .env.example              - Example template (for reference)"
    echo "  data/                     - Application data (will be created on first run)"
    echo "  postgres_data/            - PostgreSQL data"
    echo "  redis_data/               - Redis data"
    echo ""
    echo "Next steps:"
    echo "  1. (Optional) Edit .env to customize configuration"
    echo "  2. Start services:"
    if [ "${BUILD_FROM_SOURCE}" = "1" ]; then
        echo "     docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build"
        echo "     (镜像在本地构建：首次会拉取 golang/node 基础镜像并编译前后端)"
    else
        echo "     docker compose up -d"
        echo "     # 想改成在本地构建镜像（不依赖外部镜像仓库）："
        echo "     # ./docker-deploy.sh --build"
        echo "     # docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build"
    fi
    echo ""
    echo "  3. View logs:"
    echo "     docker compose logs -f sub2api"
    echo ""
    echo "  4. Access Web UI:"
    echo "     http://localhost:8080"
    echo ""
    print_info "If admin password is not set in .env, it will be auto-generated."
    print_info "Check logs for the generated admin password on first startup."
    echo ""
}

# Run main function
main "$@"
