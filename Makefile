evm:
	go run ./cmd/evm-node-check evm --config=evm.yaml

ton:
	go run ./cmd/evm-node-check ton --config=ton.yaml

NODECORE_COMPOSE = docker compose -f nodecore/docker-compose.yml $(if $(wildcard nodecore/docker-compose.override.yml),-f nodecore/docker-compose.override.yml)

nodecore-up:
	@test -f nodecore/nodecore.yml || { echo "nodecore/nodecore.yml not found: cp nodecore/nodecore.example.yml nodecore/nodecore.yml"; exit 1; }
	$(NODECORE_COMPOSE) up -d
	@echo "nodecore: http://localhost:9090/queries/<chain>, metrics :9093/metrics, status :9096/status"

nodecore-down:
	$(NODECORE_COMPOSE) down

nodecore-logs:
	$(NODECORE_COMPOSE) logs -f nodecore

nodecore-check:
	go run ./cmd/evm-node-check nodecore --config=nodecore/nodecore.yml

.PHONY: start ton nodecore-up nodecore-down nodecore-logs nodecore-check
