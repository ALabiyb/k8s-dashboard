#!/bin/sh
set -e
if [ "$MOCK_MODE" = "true" ]; then
    exec /app/k8s-dashboard -mock -config /app/config/config.yaml
else
    exec /app/k8s-dashboard -config /app/config/config.yaml
fi
