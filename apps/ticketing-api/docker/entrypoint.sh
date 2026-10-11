#!/bin/sh
# Container bootstrap: resolve the VM identity and secrets, cache config, migrate once, start services.
set -eu

METADATA="http://169.254.169.254/computeMetadata/v1"
metadata() {
    curl -fsS --max-time 3 --retry 3 -H 'Metadata-Flavor: Google' "${METADATA}/$1"
}

if metadata instance/name >/dev/null 2>&1; then
    INSTANCE_NAME="$(metadata instance/name)"
    INSTANCE_ZONE="$(metadata instance/zone | awk -F/ '{print $NF}')"
    export INSTANCE_NAME INSTANCE_ZONE
    echo "entrypoint: running on ${INSTANCE_NAME} (${INSTANCE_ZONE})"
    ACCESS_TOKEN="$(metadata instance/service-accounts/default/token | jq -r .access_token)"
    [ -n "$ACCESS_TOKEN" ] && [ "$ACCESS_TOKEN" != null ] || { echo "entrypoint: no service-account token" >&2; exit 1; }

    # Secrets come from Secret Manager with the VM's own service account; nothing is baked into
    # the image or the instance metadata. Fails loudly (without printing the value) on any error.
    read_secret() {
        body="$(curl -sS --retry 5 --retry-delay 2 -w '\n%{http_code}' -H "Authorization: Bearer ${ACCESS_TOKEN}" \
            "https://secretmanager.googleapis.com/v1/$1:access")" || { echo "entrypoint: cannot reach Secret Manager for $1" >&2; return 1; }
        code="$(printf '%s' "$body" | tail -n1)"
        if [ "$code" != 200 ]; then
            echo "entrypoint: Secret Manager returned HTTP ${code} for $1: $(printf '%s' "$body" | sed '$d' | jq -c .error 2>/dev/null)" >&2
            return 1
        fi
        value="$(printf '%s' "$body" | sed '$d' | jq -r .payload.data | base64 -d)"
        [ -n "$value" ] || { echo "entrypoint: secret $1 is empty" >&2; return 1; }
        printf '%s' "$value"
    }
    if [ -n "${DB_PASSWORD_SECRET:-}" ]; then DB_PASSWORD="$(read_secret "${DB_PASSWORD_SECRET}/versions/latest")" || exit 1; export DB_PASSWORD; fi
    if [ -n "${LAB_ADMIN_TOKEN_SECRET:-}" ]; then LAB_ADMIN_TOKEN="$(read_secret "${LAB_ADMIN_TOKEN_SECRET}/versions/latest")" || exit 1; export LAB_ADMIN_TOKEN; fi
    if [ -n "${APP_KEY_SECRET:-}" ]; then APP_KEY="$(read_secret "${APP_KEY_SECRET}/versions/latest")" || exit 1; export APP_KEY; fi
    echo "entrypoint: secrets loaded"
    unset ACCESS_TOKEN
else
    echo "entrypoint: not on GCE (metadata server unreachable); using environment as given"
fi

CONTAINER_STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
export CONTAINER_STARTED_AT

# /api/admin/* is only reachable from these networks (the load balancer's proxies are not in them).
ADMIN_ALLOW_RULES=""
for cidr in $(echo "${ADMIN_ALLOW_CIDRS:-127.0.0.1/32}" | tr ',' ' '); do
    ADMIN_ALLOW_RULES="${ADMIN_ALLOW_RULES}allow ${cidr}; "
done
export ADMIN_ALLOW_RULES
# /lab/load and /lab/cpu (load signals for the waiting room) are only reachable from these networks.
LOAD_ALLOW_RULES=""
for cidr in $(echo "${LOAD_ALLOW_CIDRS:-127.0.0.1/32}" | tr ',' ' '); do
    LOAD_ALLOW_RULES="${LOAD_ALLOW_RULES}allow ${cidr}; "
done
export LOAD_ALLOW_RULES
export LAB_GUEST_CPU_FILE="${LAB_GUEST_CPU_FILE:-/tmp/guest-cpu.json}"
export HTTP_PORT="${HTTP_PORT:-8080}"
export FPM_MAX_CHILDREN="${FPM_MAX_CHILDREN:-16}"
envsubst '${ADMIN_ALLOW_RULES} ${LOAD_ALLOW_RULES} ${LAB_GUEST_CPU_FILE} ${HTTP_PORT} ${FPM_MAX_CHILDREN}' < /etc/nginx/templates/default.conf.template > /etc/nginx/http.d/default.conf
envsubst '${FPM_MAX_CHILDREN}' < /usr/local/etc/php-fpm.d/www.conf.template > /usr/local/etc/php-fpm.d/www.conf

php artisan config:cache --no-interaction >/dev/null

# Migrations run as a supervised task next to nginx/PHP-FPM, so the liveness check answers while the
# database is still unreachable and autohealing does not recreate the VM; /health stays 503 until
# the schema exists.
if [ "${RUN_MIGRATIONS:-true}" != "true" ]; then
    sed -i 's/^autostart = true ; migrations$/autostart = false/' /etc/supervisord.conf
fi

exec /usr/bin/supervisord -c /etc/supervisord.conf
