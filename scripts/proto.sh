#!/bin/bash

# Get the absolute path of the script's directory
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" &> /dev/null && pwd)
PROJECT_ROOT="$SCRIPT_DIR/.."
PROTO_DIR="$PROJECT_ROOT/src/internal/grpc"

# Generate Python gRPC code
python3 -m grpc_tools.protoc -I"$PROJECT_ROOT" \
    --python_out="$PROJECT_ROOT/src/services/dispatcher" \
    --grpc_python_out="$PROJECT_ROOT/src/services/dispatcher" \
    "$PROTO_DIR/text_embedding/text_embedding.proto" \
    "$PROTO_DIR/unstructured_parsing/unstructured_parsing.proto"

python3 -m grpc_tools.protoc -I"$PROJECT_ROOT" \
    --python_out="$PROJECT_ROOT/src/services/text-embedding" \
    --grpc_python_out="$PROJECT_ROOT/src/services/text-embedding" \
    "$PROTO_DIR/text_embedding/text_embedding.proto"

python3 -m grpc_tools.protoc -I"$PROJECT_ROOT" \
    --python_out="$PROJECT_ROOT/src/services/unstructured-parsing-service" \
    --grpc_python_out="$PROJECT_ROOT/src/services/unstructured-parsing-service" \
    "$PROTO_DIR/unstructured_parsing/unstructured_parsing.proto"

