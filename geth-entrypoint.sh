#!/bin/sh
set -e

# Initialize genesis if not already done
if [ ! -d "/root/.ethereum/geth" ]; then
    echo "Initializing genesis..."
    geth init --datadir /root/.ethereum /genesis/genesis.json
fi

# Run geth
exec geth \
    --datadir /root/.ethereum \
    --http \
    --http.addr 0.0.0.0 \
    --http.port 8545 \
    --http.api eth,net,web3,txpool,debug \
    --http.corsdomain '*' \
    --http.vhosts '*' \
    --authrpc.addr 0.0.0.0 \
    --authrpc.port 8551 \
    --authrpc.vhosts '*' \
    --authrpc.jwtsecret /jwt/jwt.hex \
    --nodiscover \
    --maxpeers 0 \
    --networkid 1337 \
    --allow-insecure-unlock \
    --syncmode full \
    "$@"
