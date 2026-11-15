#!/usr/bin/env bash
# Run the DB-backed tests against a THROWAWAY Postgres (pgvector image, the one
# CI uses), never the live cluster: `docker run --rm` on a random loopback
# port, a single gojob_test database, torn down on exit. Migrations are applied
# the way .github/workflows/preflight.yml does: "-- soft" files may fail
# (extension absent), every other file must succeed.
#
# Usage: make test-db                         # go test ./internal/...
#        bash scripts/test-db.sh ./internal/hunt -run TestFoo
set -euo pipefail

cd "$(dirname "$0")/.."
NAME="gojob-test-db-$$"
IMAGE="pgvector/pgvector:pg16"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

echo ">> starting throwaway $IMAGE as $NAME (random loopback port)"
docker run -d --rm --name "$NAME" \
  -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=gojob_test \
  -p 127.0.0.1::5432 "$IMAGE" >/dev/null
PORT=$(docker inspect -f '{{ (index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort }}' "$NAME")

ready=0
for _ in $(seq 1 30); do
  if docker exec "$NAME" pg_isready -h 127.0.0.1 -U postgres -d gojob_test >/dev/null 2>&1; then ready=1; break; fi
  sleep 2
done
[ "$ready" = "1" ] || { echo "postgres never became ready" >&2; docker logs "$NAME" | tail -20 >&2; exit 1; }

psql_in() { docker exec -i "$NAME" psql -U postgres -d gojob_test -v ON_ERROR_STOP=1 -q; }
for f in $(ls internal/engine/jobs/schema/*.sql internal/hunt/schema/*.sql | sort); do
  if head -1 "$f" | grep -q "^-- soft"; then
    psql_in < "$f" >/dev/null 2>&1 || echo "soft migration skipped (extension absent): $f"
  else
    psql_in < "$f" >/dev/null || { echo "hard migration failed: $f" >&2; exit 1; }
  fi
done

export DATABASE_URL="postgres://postgres:postgres@127.0.0.1:${PORT}/gojob_test?sslmode=disable"
echo ">> DATABASE_URL points at the throwaway container on 127.0.0.1:${PORT}"
if [ "$#" -eq 0 ]; then set -- ./internal/...; fi
GOWORK=off go test -count=1 -p 1 "$@"
