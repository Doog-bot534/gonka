#!/bin/sh

# Set default values for environment variables if not provided

export GONKA_API_PORT=${GONKA_API_PORT:-9000}
export CHAIN_RPC_PORT=${CHAIN_RPC_PORT:-26657}
export CHAIN_API_PORT=${CHAIN_API_PORT:-1317}
export CHAIN_GRPC_PORT=${CHAIN_GRPC_PORT:-9090}

# Service names - configurable for Docker vs Kubernetes
export API_SERVICE_NAME=${API_SERVICE_NAME:-api}
export NODE_SERVICE_NAME=${NODE_SERVICE_NAME:-node}
export EXPLORER_SERVICE_NAME=${EXPLORER_SERVICE_NAME:-explorer}
export PROXY_SSL_SERVICE_NAME=${PROXY_SSL_SERVICE_NAME:-proxy-ssl}
export PROXY_SSL_PORT=${PROXY_SSL_PORT:-8080}

if [ -n "${KEY_NAME}" ] && [ "${KEY_NAME}" != "" ]; then
    export KEY_NAME_PREFIX="${KEY_NAME}-"
else
    export KEY_NAME_PREFIX=""
fi

# Set final service names
export FINAL_API_SERVICE="${KEY_NAME_PREFIX}${API_SERVICE_NAME}"
export FINAL_NODE_SERVICE="${KEY_NAME_PREFIX}${NODE_SERVICE_NAME}"
export FINAL_EXPLORER_SERVICE="${KEY_NAME_PREFIX}${EXPLORER_SERVICE_NAME}"
export FINAL_PROXY_SSL_SERVICE="${KEY_NAME_PREFIX}${PROXY_SSL_SERVICE_NAME}"

# Check if dashboard is enabled
DASHBOARD_ENABLED="false"
if [ -n "${DASHBOARD_PORT}" ] && [ "${DASHBOARD_PORT}" != "" ]; then
    DASHBOARD_ENABLED="true"
    export DASHBOARD_PORT=${DASHBOARD_PORT}
fi

# Resolve which ports/mode to enable
# NGINX_MODE supports: http | https | both
NGINX_MODE=${NGINX_MODE:-}

ENABLE_HTTP="false"
ENABLE_HTTPS="false"
case "$NGINX_MODE" in
  http) ENABLE_HTTP="true" ;;
  https) ENABLE_HTTPS="true" ;;
  both) ENABLE_HTTP="true"; ENABLE_HTTPS="true" ;;
  *)
    echo "WARNING: Unknown NGINX_MODE='$NGINX_MODE', defaulting to 'http'"
    ENABLE_HTTP="true"
    NGINX_MODE="http"
    ;;
esac

# SSL is considered enabled if HTTPS is enabled
SSL_ENABLED="false"
if [ "$ENABLE_HTTPS" = "true" ]; then
    SSL_ENABLED="true"
fi

# Determine server_name
if [ -z "${SERVER_NAME:-}" ]; then
    if [ "$SSL_ENABLED" = "true" ] && [ -n "${CERT_ISSUER_DOMAIN:-}" ]; then
        export SERVER_NAME="$CERT_ISSUER_DOMAIN"
    else
        export SERVER_NAME="localhost"
    fi
fi

# For logging
if [ "$SSL_ENABLED" = "true" ]; then
    export DOMAIN_NAME=${CERT_ISSUER_DOMAIN}
fi

# Log the configuration being used
echo "🔧 Nginx Proxy Configuration:"
echo "   KEY_NAME: $KEY_NAME"
echo "   PROXY_ADD_NODE_PREFIX: $PROXY_ADD_NODE_PREFIX"
echo "   API Service: $FINAL_API_SERVICE:$GONKA_API_PORT"
echo "   Node Service: $FINAL_NODE_SERVICE (API:$CHAIN_API_PORT, RPC:$CHAIN_RPC_PORT, gRPC:$CHAIN_GRPC_PORT)"
echo "   Explorer Service: $FINAL_EXPLORER_SERVICE:$DASHBOARD_PORT"
echo "   Proxy-SSL Service: $FINAL_PROXY_SSL_SERVICE:$PROXY_SSL_PORT"
if [ "$ENABLE_HTTP" = "true" ] && [ "$ENABLE_HTTPS" = "true" ]; then
    echo "   Mode: both (HTTP:80, HTTPS:443)"
elif [ "$ENABLE_HTTP" = "true" ]; then
    echo "   Mode: http-only (80)"
else
    echo "   Mode: https-only (443)"
fi
if [ "$SSL_ENABLED" = "true" ]; then
    echo "   SSL: Enabled for domain $DOMAIN_NAME"
else
    echo "   SSL: Disabled"
fi

if [ "$DASHBOARD_ENABLED" = "true" ]; then
    echo "   DASHBOARD_PORT: $DASHBOARD_PORT (enabled)"
    echo "Dashboard: Enabled - root path will proxy to explorer"
    
    # Set up dashboard upstream and root location for enabled dashboard
    export DASHBOARD_UPSTREAM="upstream dashboard_backend {
        zone dashboard_backend 64k;
        server ${FINAL_EXPLORER_SERVICE}:${DASHBOARD_PORT} resolve;
    }"
    
    export ROOT_LOCATION="location / {
            proxy_pass http://dashboard_backend/;
            proxy_set_header Host \$\$host;
            proxy_set_header X-Real-IP \$\$remote_addr;
            proxy_set_header X-Forwarded-For \$\$proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto \$\$scheme;

            # WebSocket support for hot reloading
            proxy_http_version 1.1;
            proxy_set_header Upgrade \$\$http_upgrade;
            proxy_set_header Connection \"upgrade\";
        }"
else
    echo "   DASHBOARD_PORT: not set (disabled)"
    echo "Dashboard: Disabled - root path will show 'not available' page"
    
    # No dashboard upstream needed
    export DASHBOARD_UPSTREAM="# Dashboard not configured"
    
    # Set up root location for disabled dashboard
    export ROOT_LOCATION="location / {
            return 200 '<!DOCTYPE html>
<html>
<head>
    <title>Dashboard Not Configured</title>
    <style>
        body { font-family: Arial, sans-serif; text-align: center; padding: 50px; background: #f5f5f5; }
        .container { max-width: 600px; margin: 0 auto; background: white; padding: 40px; border-radius: 8px; box-shadow: 0 2px 10px rgba(0,0,0,0.1); }
        h1 { color: #e74c3c; margin-bottom: 20px; }
        p { color: #666; line-height: 1.6; margin-bottom: 15px; }
        .code { background: #f8f9fa; padding: 2px 6px; border-radius: 3px; font-family: monospace; }
        .endpoint-list { text-align: left; display: inline-block; margin: 20px 0; }
        .endpoint-list li { margin: 8px 0; }
    </style>
</head>
<body>
    <div class=\"container\">
        <h1>Dashboard Not Configured</h1>
        <p>The blockchain explorer dashboard is not enabled for this deployment.</p>
        <p>You can access the following endpoints:</p>
        <ul class=\"endpoint-list\">
            <li>API endpoints: <span class=\"code\">/api/*</span></li>
            <li>Chain RPC: <span class=\"code\">/chain-rpc/*</span></li>
            <li>Chain REST API: <span class=\"code\">/chain-api/*</span></li>
            <li>Chain gRPC: <span class=\"code\">/chain-grpc/*</span></li>
            <li>Health check: <span class=\"code\">/health</span></li>
        </ul>
        <p>To enable the dashboard, set the <span class=\"code\">DASHBOARD_PORT</span> environment variable and include the explorer service in your deployment.</p>
    </div>
</body>
</html>';
            add_header Content-Type text/html;
        }"
fi

# If SSL is intended, ensure certificates are present (attempt issuance if missing)
if [ "$SSL_ENABLED" = "true" ]; then
    if [ ! -f "/etc/nginx/ssl/cert.pem" ] || [ ! -f "/etc/nginx/ssl/private.key" ]; then
        echo "SSL enabled but certificates not found; requesting via proxy-ssl"
        /setup-ssl.sh || echo "WARNING: SSL setup failed; will attempt to continue"
    fi

    # Start background renewal loop if order.id exists (indicates auto issuance)
    if [ -f "/etc/nginx/ssl/order.id" ]; then
        RENEW_INTERVAL_HOURS=${RENEW_INTERVAL_HOURS:-24}
        echo "Starting background renewal loop (every ${RENEW_INTERVAL_HOURS}h)"
        (
            while true; do
                if /setup-ssl.sh renew-if-needed; then
                    echo "No renewal needed"
                else
                    if [ "$?" -eq 10 ]; then
                        echo "Certificate renewed; reloading nginx"
                        nginx -s reload || true
                    else
                        echo "WARNING: Renewal attempt failed"
                    fi
                fi
                sleep $(( RENEW_INTERVAL_HOURS * 3600 ))
            done
        ) &
    fi
fi

# Prepare template vars for unified config
if [ "$ENABLE_HTTP" = "true" ]; then
    export LISTEN_HTTP="listen 80;"
else
    export LISTEN_HTTP="# HTTP disabled"
fi

if [ "$ENABLE_HTTPS" = "true" ]; then
    export LISTEN_HTTPS="listen 443 ssl;
        http2 on;"
    export SSL_CONFIG="ssl_certificate /etc/nginx/ssl/cert.pem;
        ssl_certificate_key /etc/nginx/ssl/private.key;
        
        # SSL Security Settings
        ssl_protocols TLSv1.2 TLSv1.3;
        ssl_ciphers ECDHE-RSA-AES256-GCM-SHA512:DHE-RSA-AES256-GCM-SHA512:ECDHE-RSA-AES256-GCM-SHA384:DHE-RSA-AES256-GCM-SHA384;
        ssl_prefer_server_ciphers off;
        ssl_session_cache shared:SSL:10m;
        ssl_session_timeout 10m;"
else
    export LISTEN_HTTPS="# HTTPS disabled"
    export SSL_CONFIG="# SSL disabled"
fi

# Route Disabling Logic
# If DISABLE_* env vars are set to true, inject a "return 404" into the location block

if [ "${DISABLE_GONKA_API}" = "true" ]; then
    export API_STATUS="return 404 'App API Disabled';"
    echo "   🚫 App API: Disabled"
else
    export API_STATUS=""
fi

if [ "${DISABLE_CHAIN_RPC}" = "true" ]; then
    export CHAIN_RPC_STATUS="return 404 'Chain RPC Disabled';"
    echo "   🚫 Chain RPC: Disabled"
else
    export CHAIN_RPC_STATUS=""
fi

if [ "${DISABLE_CHAIN_API}" = "true" ]; then
    export CHAIN_API_STATUS="return 404 'Chain API Disabled';"
    echo "   🚫 Chain API: Disabled"
else
    export CHAIN_API_STATUS=""
fi

if [ "${DISABLE_CHAIN_GRPC}" = "true" ]; then
    export CHAIN_GRPC_STATUS="return 404 'Chain gRPC Disabled';"
    echo "   🚫 Chain gRPC: Disabled"
else
    export CHAIN_GRPC_STATUS=""
fi

# CORS Configuration - Single source of truth for all location blocks
export CORS_CONFIG="
            # CORS setup
            if (\$\$request_method = 'OPTIONS') {
                add_header 'Access-Control-Allow-Origin' '*';
                add_header 'Access-Control-Allow-Methods' 'GET, POST, OPTIONS, PUT, DELETE';
                add_header 'Access-Control-Allow-Headers' 'DNT,User-Agent,X-Requested-With,If-Modified-Since,Cache-Control,Content-Type,Range,Authorization';
                add_header 'Access-Control-Max-Age' 1728000;
                add_header 'Content-Type' 'text/plain; charset=utf-8';
                add_header 'Content-Length' 0;
                return 204;
            }
            add_header 'Access-Control-Allow-Origin' '*' always;
            add_header 'Access-Control-Allow-Methods' 'GET, POST, OPTIONS, PUT, DELETE' always;
            add_header 'Access-Control-Allow-Headers' 'DNT,User-Agent,X-Requested-With,If-Modified-Since,Cache-Control,Content-Type,Range,Authorization' always;
            add_header 'Access-Control-Expose-Headers' 'Content-Length,Content-Range' always;"

# Configure DNS resolver for dynamic upstream re-resolution
if [ -n "${RESOLVER:-}" ]; then
    export RESOLVER_DIRECTIVE="resolver ${RESOLVER} valid=10s ipv6=off;"
else
    # Default Docker DNS, override with RESOLVER to your infra DNS if needed
    export RESOLVER_DIRECTIVE="resolver 127.0.0.11 valid=10s ipv6=off;"
fi

# Rate Limiting Logic (Granular)
# Default values

# 1. Global (Safety Net - Ceiling for everything)
# Default: 1000r/s to ensure it doesn't throttle Exempt routes (500r/s)
GLOBAL_RATE_LIMIT_VAL=${GLOBAL_RATE_LIMIT_RPS:-1000}
GLOBAL_RATE_UNIT=${GLOBAL_RATE_UNIT:-s}
GLOBAL_BURST=${GLOBAL_BURST:-5000}

# 2. Gonka API (Standard/Punisher)
# Default: 10r/m + 600 burst = 1 hr recovery
GONKA_API_RATE_LIMIT_VAL=${GONKA_API_RATE_LIMIT_RPS:-10}
GONKA_API_RATE_UNIT=${GONKA_API_RATE_UNIT:-m}
GONKA_API_BURST=${GONKA_API_BURST:-600}

# 3. Gonka API Exemptions (High Performance)
# Routes: chat, inference, training (partial matching)
GONKA_API_EXEMPT_ROUTES=${GONKA_API_EXEMPT_ROUTES:-"chat inference training"}
EXEMPT_RATE_LIMIT_VAL=${EXEMPT_RATE_LIMIT_RPS:-500}
EXEMPT_RATE_UNIT=${EXEMPT_RATE_UNIT:-s}
EXEMPT_BURST=${EXEMPT_BURST:-2000}

# 4. Chain API
CHAIN_API_RATE_LIMIT_VAL=${CHAIN_API_RATE_LIMIT_RPS:-20}
CHAIN_API_RATE_UNIT=${CHAIN_API_RATE_UNIT:-m}
CHAIN_API_BURST=${CHAIN_API_BURST:-200}

# 5. Chain RPC (Strict)
CHAIN_RPC_RATE_LIMIT_VAL=${CHAIN_RPC_RATE_LIMIT_RPS:-20}
CHAIN_RPC_RATE_UNIT=${CHAIN_RPC_RATE_UNIT:-m}
CHAIN_RPC_BURST=${CHAIN_RPC_BURST:-200}

# 6. Chain gRPC
CHAIN_GRPC_RATE_LIMIT_VAL=${CHAIN_GRPC_RATE_LIMIT_RPS:-20}
CHAIN_GRPC_RATE_UNIT=${CHAIN_GRPC_RATE_UNIT:-m}
CHAIN_GRPC_BURST=${CHAIN_GRPC_BURST:-200}

echo "   🛡️ Rate Limits:"
echo "      Global: ${GLOBAL_RATE_LIMIT_VAL}r/${GLOBAL_RATE_UNIT} (burst=${GLOBAL_BURST})"
echo "      App API (Standard): ${GONKA_API_RATE_LIMIT_VAL}r/${GONKA_API_RATE_UNIT} (burst=${GONKA_API_BURST})"
echo "      App API (Exempt): ${EXEMPT_RATE_LIMIT_VAL}r/${EXEMPT_RATE_UNIT} (burst=${EXEMPT_BURST}) -> [${GONKA_API_EXEMPT_ROUTES}]"
echo "      Chain API: ${CHAIN_API_RATE_LIMIT_VAL}r/${CHAIN_API_RATE_UNIT} (burst=${CHAIN_API_BURST})"
echo "      Chain RPC: ${CHAIN_RPC_RATE_LIMIT_VAL}r/${CHAIN_RPC_RATE_UNIT} (burst=${CHAIN_RPC_BURST})"
echo "      Chain gRPC: ${CHAIN_GRPC_RATE_LIMIT_VAL}r/${CHAIN_GRPC_RATE_UNIT} (burst=${CHAIN_GRPC_BURST})"

# Define Zones
# Use $$binary_remote_addr so it persists after first envsubst
export LIMIT_REQ_ZONE_GLOBAL="limit_req_zone \$\$binary_remote_addr zone=global_zone:10m rate=${GLOBAL_RATE_LIMIT_VAL}r/${GLOBAL_RATE_UNIT};"
export LIMIT_REQ_ZONE_GONKA_API="limit_req_zone \$\$binary_remote_addr zone=api_zone:10m rate=${GONKA_API_RATE_LIMIT_VAL}r/${GONKA_API_RATE_UNIT};"
export LIMIT_REQ_ZONE_EXEMPT="limit_req_zone \$\$binary_remote_addr zone=exempt_zone:10m rate=${EXEMPT_RATE_LIMIT_VAL}r/${EXEMPT_RATE_UNIT};"
export LIMIT_REQ_ZONE_CHAIN_API="limit_req_zone \$\$binary_remote_addr zone=chain_api_zone:10m rate=${CHAIN_API_RATE_LIMIT_VAL}r/${CHAIN_API_RATE_UNIT};"
export LIMIT_REQ_ZONE_CHAIN_RPC="limit_req_zone \$\$binary_remote_addr zone=rpc_zone:10m rate=${CHAIN_RPC_RATE_LIMIT_VAL}r/${CHAIN_RPC_RATE_UNIT};"
export LIMIT_REQ_ZONE_CHAIN_GRPC="limit_req_zone \$\$binary_remote_addr zone=grpc_zone:10m rate=${CHAIN_GRPC_RATE_LIMIT_VAL}r/${CHAIN_GRPC_RATE_UNIT};"

# Define Rules
export LIMIT_REQ_RULE_GLOBAL="limit_req zone=global_zone burst=${GLOBAL_BURST} nodelay;"
export LIMIT_REQ_RULE_GONKA_API="limit_req zone=api_zone burst=${GONKA_API_BURST} nodelay;"
export LIMIT_REQ_RULE_CHAIN_API="limit_req zone=chain_api_zone burst=${CHAIN_API_BURST} nodelay;"
export LIMIT_REQ_RULE_CHAIN_RPC="limit_req zone=rpc_zone burst=${CHAIN_RPC_BURST} nodelay;"
export LIMIT_REQ_RULE_CHAIN_GRPC="limit_req zone=grpc_zone burst=${CHAIN_GRPC_BURST} nodelay;"

# Generate Exempt Routes Configuration
EXEMPT_ROUTES_CONFIG=""
for route in $GONKA_API_EXEMPT_ROUTES; do
    # Ensure route doesn't start with / to avoid double slashes
    clean_route=$(echo "$route" | sed 's|^/||')
    
    # Generate block for /api/v1/ prefix
    EXEMPT_ROUTES_CONFIG="${EXEMPT_ROUTES_CONFIG}
    location /api/v1/${clean_route} {
        limit_req zone=exempt_zone burst=${EXEMPT_BURST} nodelay;
        ${API_STATUS}
        proxy_pass http://api_backend/v1/${clean_route};
        proxy_set_header Host \$\$host;
        proxy_set_header X-Real-IP \$\$remote_addr;
        proxy_set_header X-Forwarded-For \$\$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$\$scheme;
        proxy_set_header Authorization \$\$http_authorization;
        ${CORS_CONFIG}
    }
    "

    # Generate block for /v1/ prefix
    EXEMPT_ROUTES_CONFIG="${EXEMPT_ROUTES_CONFIG}
    location /v1/${clean_route} {
        limit_req zone=exempt_zone burst=${EXEMPT_BURST} nodelay;
        ${API_STATUS}
        proxy_pass http://api_backend/v1/${clean_route};
        proxy_set_header Host \$\$host;
        proxy_set_header X-Real-IP \$\$remote_addr;
        proxy_set_header X-Forwarded-For \$\$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$\$scheme;
        proxy_set_header Authorization \$\$http_authorization;
        ${CORS_CONFIG}
    }
    "
done
export EXEMPT_ROUTES_CONFIG

# Construct envsubst variable list for readability
# Group 1: Core Configuration & Naming
ENVSUBST_VARS='$KEY_NAME,$KEY_NAME_PREFIX,$SERVER_NAME,$DOMAIN_NAME,$RESOLVER_DIRECTIVE,$CORS_CONFIG'

# Group 2: Ports & Services
ENVSUBST_VARS="${ENVSUBST_VARS},\$GONKA_API_PORT,\$CHAIN_RPC_PORT,\$CHAIN_API_PORT,\$CHAIN_GRPC_PORT"
ENVSUBST_VARS="${ENVSUBST_VARS},\$FINAL_API_SERVICE,\$FINAL_NODE_SERVICE,\$FINAL_EXPLORER_SERVICE"

# Group 3: HTTP/SSL & Status
ENVSUBST_VARS="${ENVSUBST_VARS},\$LISTEN_HTTP,\$LISTEN_HTTPS,\$SSL_CONFIG"
ENVSUBST_VARS="${ENVSUBST_VARS},\$API_STATUS,\$CHAIN_RPC_STATUS,\$CHAIN_API_STATUS,\$CHAIN_GRPC_STATUS"

# Group 4: Dashboard
ENVSUBST_VARS="${ENVSUBST_VARS},\$DASHBOARD_PORT,\$DASHBOARD_UPSTREAM,\$ROOT_LOCATION"

# Group 5: Rate Limiting Zones
ENVSUBST_VARS="${ENVSUBST_VARS},\$LIMIT_REQ_ZONE_GLOBAL,\$LIMIT_REQ_ZONE_GONKA_API,\$LIMIT_REQ_ZONE_EXEMPT"
ENVSUBST_VARS="${ENVSUBST_VARS},\$LIMIT_REQ_ZONE_CHAIN_RPC,\$LIMIT_REQ_ZONE_CHAIN_API,\$LIMIT_REQ_ZONE_CHAIN_GRPC"

# Group 6: Rate Limiting Rules
ENVSUBST_VARS="${ENVSUBST_VARS},\$LIMIT_REQ_RULE_GLOBAL,\$LIMIT_REQ_RULE_GONKA_API"
ENVSUBST_VARS="${ENVSUBST_VARS},\$LIMIT_REQ_RULE_CHAIN_RPC,\$LIMIT_REQ_RULE_CHAIN_API,\$LIMIT_REQ_RULE_CHAIN_GRPC"
ENVSUBST_VARS="${ENVSUBST_VARS},\$EXEMPT_ROUTES_CONFIG"

echo "Rendering unified nginx configuration (mode: $NGINX_MODE, server_name: $SERVER_NAME)"
envsubst "$ENVSUBST_VARS" < /etc/nginx/nginx.unified.conf.template | sed 's/\$\$/$/g' > /etc/nginx/nginx.conf

# Validate nginx configuration (with fallback if SSL config fails)
if nginx -t; then
    echo "Nginx configuration is valid"
else
    echo "WARNING: Nginx configuration invalid"
    if [ "$ENABLE_HTTPS" = "true" ] && [ "$ENABLE_HTTP" = "true" ]; then
        echo "FALLBACK: Falling back to HTTP-only configuration"
        ENABLE_HTTPS="false"
        export LISTEN_HTTPS="# HTTPS disabled"
        export SSL_CONFIG="# SSL disabled"
        
        # Retry rendering with HTTP-only settings
        envsubst "$ENVSUBST_VARS" < /etc/nginx/nginx.unified.conf.template | sed 's/\$\$/$/g' > /etc/nginx/nginx.conf
        
        if nginx -t; then
            echo "SUCCESS: Nginx configuration is valid (HTTP-only fallback)"
        else
            echo "ERROR: Nginx configuration is invalid after HTTP-only fallback"
            exit 1
        fi
    else
        echo "ERROR: Nginx configuration is invalid and no fallback available"
        exit 1
    fi
fi

echo "🌐 Available endpoints:"
if [ "$DASHBOARD_ENABLED" = "true" ]; then
    echo "   / (root)       -> Explorer dashboard"
else
    echo "   / (root)       -> Dashboard not configured page"
fi
echo "   /api/*         -> API backend"
echo "   /chain-rpc/*   -> Chain RPC"
echo "   /chain-api/*   -> Chain REST API"
echo "   /chain-grpc/*  -> Chain gRPC"
echo "   /health        -> Health check"

# Execute the command passed to the container
exec "$@" 