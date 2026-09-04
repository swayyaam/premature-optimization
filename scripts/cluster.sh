#!/usr/bin/env bash
#
# Starts or stops three copies of the application with nginx in front of them.
#
#   scripts/cluster.sh start
#   scripts/cluster.sh stop
#
# nginx listens on 8080. The instances listen on 8081, 8082 and 8083.

set -euo pipefail
cd "$(dirname "$0")/.."

PORTS="8081 8082 8083"
RUN="$PWD/run"

start() {
    mkdir -p "$RUN/logs"
    go build -o "$RUN/shortener" .

    for port in $PORTS; do
        ADDR=":$port" "$RUN/shortener" > "$RUN/logs/app-$port.log" 2>&1 &
        echo $! > "$RUN/app-$port.pid"
        echo "instance on $port, pid $(cat "$RUN/app-$port.pid")"
    done

    for port in $PORTS; do
        for _ in $(seq 1 50); do
            curl -sf "http://127.0.0.1:$port/healthz" > /dev/null && break
            sleep 0.1
        done
    done

    nginx -p "$RUN" -c "$PWD/deploy/nginx.conf"
    echo "nginx on 8080"
}

stop() {
    if [ -f "$RUN/logs/nginx.pid" ]; then
        nginx -p "$RUN" -c "$PWD/deploy/nginx.conf" -s quit 2>/dev/null || true
    fi
    for port in $PORTS; do
        if [ -f "$RUN/app-$port.pid" ]; then
            kill "$(cat "$RUN/app-$port.pid")" 2>/dev/null || true
            rm -f "$RUN/app-$port.pid"
        fi
    done
    echo "stopped"
}

case "${1:-}" in
    start) start ;;
    stop)  stop ;;
    *) echo "usage: $0 start|stop" >&2; exit 1 ;;
esac
