#!/bin/bash

# Comprehensive cleanup script for Genesis and Join nodes
# This removes all state, Docker containers, volumes, and configuration files

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Default base directory (can be overridden with TESTNET_BASE_DIR)
BASE_DIR="${TESTNET_BASE_DIR:-$HOME}"
GONKA_DIR="${BASE_DIR}/gonka"
DEPLOY_DIR="${GONKA_DIR}/deploy/join"
INFERENCED_STATE_DIR="${BASE_DIR}/.inference"
INFERENCED_BINARY="${BASE_DIR}/inferenced"
INFERENCED_ZIP="${BASE_DIR}/inferenced-linux-amd64.zip"

echo -e "${YELLOW}=== Full Node Cleanup Script ===${NC}"
echo "Base directory: ${BASE_DIR}"
echo ""

# Function to confirm before destructive operations
confirm() {
    read -p "$(echo -e ${YELLOW}Are you sure you want to continue? This will delete all node data! [y/N]: ${NC})" -n 1 -r
    echo
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Cleanup cancelled."
        exit 1
    fi
}

# Function to safely remove directory
safe_remove() {
    local path="$1"
    local description="$2"
    
    if [ -e "$path" ] || [ -d "$path" ] 2>/dev/null; then
        echo -e "${YELLOW}Removing ${description}: ${path}${NC}"
        if sudo rm -rf "$path" 2>/dev/null; then
            echo -e "${GREEN}✓ Removed ${description}${NC}"
        else
            echo -e "${RED}✗ Failed to remove ${description} (may need manual cleanup)${NC}"
        fi
    else
        echo -e "  ${description} doesn't exist, skipping"
    fi
}

# Function to build docker compose command with only existing files
# Returns space-separated list of -f flags and filenames
build_compose_cmd() {
    local base_dir="$1"
    local compose_args=""
    
    # Base files (check if they exist)
    if [ -f "${base_dir}/docker-compose.yml" ]; then
        compose_args="-f docker-compose.yml"
    else
        return 1  # Base file is required
    fi
    
    if [ -f "${base_dir}/docker-compose.mlnode.yml" ]; then
        compose_args="${compose_args} -f docker-compose.mlnode.yml"
    fi
    
    if [ -f "${base_dir}/docker-compose.postgres.yml" ]; then
        compose_args="${compose_args} -f docker-compose.postgres.yml"
    fi
    
    # Override files (check if they exist)
    if [ -f "${base_dir}/docker-compose.env-override.yml" ]; then
        compose_args="${compose_args} -f docker-compose.env-override.yml"
    fi
    
    if [ -f "${base_dir}/docker-compose.genesis-override.yml" ]; then
        compose_args="${compose_args} -f docker-compose.genesis-override.yml"
    fi
    
    if [ -f "${base_dir}/docker-compose.runtime-override.yml" ]; then
        compose_args="${compose_args} -f docker-compose.runtime-override.yml"
    fi
    
    echo "$compose_args"
}

# Step 1: Stop Docker containers
echo -e "\n${YELLOW}=== Step 1: Stopping Docker containers ===${NC}"
if [ -d "$DEPLOY_DIR" ]; then
    cd "$DEPLOY_DIR" || exit 1
    
    if [ -f "docker-compose.yml" ]; then
        # Build compose command with only existing files
        compose_args=$(build_compose_cmd "$DEPLOY_DIR")
        
        if [ $? -eq 0 ] && [ -n "$compose_args" ]; then
            echo "Stopping Docker Compose services with files:"
            echo "$compose_args" | grep -oE "docker-compose[^ ]*" | sed 's/^/  - /'
            
            # Try graceful shutdown
            docker compose $compose_args down --timeout 10 2>/dev/null || true
            
            # Force stop if needed (just with base file)
            docker compose -f docker-compose.yml down --timeout 5 2>/dev/null || true
        else
            echo "  docker-compose.yml not found or invalid, using fallback"
            docker compose -f docker-compose.yml down --timeout 5 2>/dev/null || true
        fi
    fi
    
    # Stop all containers that might be running (fallback)
    docker ps -q --filter "name=node" --filter "name=api" --filter "name=tmkms" \
        --filter "name=mlnode" --filter "name=inference" --filter "name=proxy" \
        --filter "name=bridge" --filter "name=explorer" | xargs -r docker stop 2>/dev/null || true
    
    echo -e "${GREEN}✓ Docker containers stopped${NC}"
else
    echo "  Deploy directory doesn't exist, skipping Docker cleanup"
fi

# Step 2: Remove Docker containers and volumes
echo -e "\n${YELLOW}=== Step 2: Removing Docker containers and volumes ===${NC}"
if [ -d "$DEPLOY_DIR" ]; then
    cd "$DEPLOY_DIR" || exit 1
    
    if [ -f "docker-compose.yml" ]; then
        # Build compose command with only existing files
        compose_args=$(build_compose_cmd "$DEPLOY_DIR")
        
        if [ $? -eq 0 ] && [ -n "$compose_args" ]; then
            echo "Removing containers and volumes with files:"
            echo "$compose_args" | grep -oE "docker-compose[^ ]*" | sed 's/^/  - /'
            
            # Remove containers and volumes
            docker compose $compose_args down -v 2>/dev/null || true
        else
            echo "  docker-compose.yml not found or invalid, using fallback"
            docker compose -f docker-compose.yml down -v 2>/dev/null || true
        fi
    fi
    
    echo -e "${GREEN}✓ Docker containers and volumes removed${NC}"
fi

# Step 3: Remove node state directories
echo -e "\n${YELLOW}=== Step 3: Removing node state directories ===${NC}"
safe_remove "$INFERENCED_STATE_DIR" "Node state directory (~/.inference)"
safe_remove "${DEPLOY_DIR}/.inference" "Docker container state (deploy/join/.inference)"
safe_remove "${DEPLOY_DIR}/.tmkms" "TMKMS secrets directory"

# Step 4: Remove binary files
echo -e "\n${YELLOW}=== Step 4: Removing binary files ===${NC}"
safe_remove "$INFERENCED_BINARY" "inferenced binary"
safe_remove "$INFERENCED_ZIP" "inferenced zip file"

# Step 5: Remove generated config files and override files
echo -e "\n${YELLOW}=== Step 5: Removing generated config files and override files ===${NC}"
safe_remove "${DEPLOY_DIR}/config.env" "config.env file"
safe_remove "${DEPLOY_DIR}/docker-compose.env-override.yml" "docker-compose.env-override.yml"
safe_remove "${DEPLOY_DIR}/docker-compose.genesis-override.yml" "docker-compose.genesis-override.yml"
safe_remove "${DEPLOY_DIR}/docker-compose.runtime-override.yml" "docker-compose.runtime-override.yml"

# Step 6: Clean up validator directories (if genesis was run)
echo -e "\n${YELLOW}=== Step 6: Cleaning validator directories ===${NC}"
if [ -d "${GONKA_DIR}/genesis/validators" ]; then
    VALIDATORS_DIR="${GONKA_DIR}/genesis/validators"
    echo "Cleaning validators directory (keeping template only)..."
    
    for item in "$VALIDATORS_DIR"/*; do
        if [ -d "$item" ]; then
            if [ "$(basename "$item")" != "template" ] && [ "$(basename "$item")" != "testnet-genesis" ]; then
                safe_remove "$item" "Validator directory: $(basename "$item")"
            fi
        fi
    done
else
    echo "  Validators directory doesn't exist, skipping"
fi

# Step 7: Remove gonka repository
echo -e "\n${YELLOW}=== Step 4: Removing gonka repository ===${NC}"
safe_remove "$GONKA_DIR" "Gonka repository directory"


# Step 8: Clean Docker system (optional but recommended)
echo -e "\n${YELLOW}=== Step 7: Cleaning Docker system (optional) ===${NC}"
read -p "$(echo -e ${YELLOW}Clean Docker images and volumes? This frees disk space. [y/N]: ${NC})" -n 1 -r
echo
if [[ $REPLY =~ ^[Yy]$ ]]; then
    echo "Removing unused Docker images..."
    docker image prune -a -f 2>/dev/null || true
    
    echo "Removing unused Docker volumes..."
    docker volume prune -f 2>/dev/null || true
    
    echo "Removing unused Docker networks..."
    docker network prune -f 2>/dev/null || true
    
    echo -e "${GREEN}✓ Docker system cleaned${NC}"
else
    echo "  Skipping Docker system cleanup"
fi

# Summary
echo -e "\n${GREEN}=== Cleanup Complete ===${NC}"
echo ""
echo "The following have been removed:"
echo "  ✓ Docker containers and volumes"
echo "  ✓ Node state directories (~/.inference, deploy/join/.inference)"
echo "  ✓ Binary files (inferenced, inferenced-linux-amd64.zip)"
echo "  ✓ Generated config files (config.env, docker-compose.env-override.yml, docker-compose.genesis-override.yml, docker-compose.runtime-override.yml)"
echo "  ✓ Validator directories (genesis/validators)"
echo "  ✓ Gonka repository"
echo ""
echo -e "${YELLOW}Note: Docker images may still be cached. Run 'docker image prune -a' to remove them.${NC}"
echo -e "${YELLOW}Note: If you had permission issues, some files may need manual cleanup with sudo.${NC}"
echo ""
