#!/usr/bin/env bash
# Regenerate Go gRPC code from proto/*.proto into the rpc/ package.
# Requires: protoc, protoc-gen-go, protoc-gen-go-grpc on PATH.
# The generated *.pb.go files are committed, so this only needs to run when
# the .proto files change.
set -euo pipefail

cd "$(dirname "$0")/.."

# Ensure locally-installed tooling is reachable.
export PATH="$HOME/.local/bin:$(go env GOPATH)/bin:$PATH"

protoc \
  --go_out=. --go_opt=module=github.com/jbofill10/looper \
  --go-grpc_out=. --go-grpc_opt=module=github.com/jbofill10/looper \
  proto/looper.proto

echo "generated rpc/*.pb.go"
