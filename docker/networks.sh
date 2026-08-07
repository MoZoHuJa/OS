#!/usr/bin/env bash
set -e
echo "Creating Docker networks..."
docker network create scarlix_net --driver bridge --subnet 172.20.0.0/16 2>/dev/null || echo "scarlix_net already exists"
docker network create scarlix_ai --driver bridge --subnet 172.21.0.0/16 2>/dev/null || echo "scarlix_ai already exists"
echo "Done. Networks: scarlix_net, scarlix_ai"
