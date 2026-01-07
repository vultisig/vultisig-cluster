.PHONY: help init plan apply destroy cluster-setup deploy-all deploy-infra deploy-services test clean
.PHONY: deploy-k8s deploy-k8s-prod
.PHONY: local-build local-start local-stop local-status local-logs

TERRAFORM_DIR := infrastructure/terraform
KUBECONFIG := $(shell pwd)/.kube/config
VCLI := ./local/vcli.sh

help:
	@echo "Vultisig Cluster Management"
	@echo ""
	@echo "Local Development:"
	@echo "  local-build       Build devctl CLI"
	@echo "  local-start       Start all local services"
	@echo "  local-stop        Stop all local services"
	@echo "  local-status      Show local service status"
	@echo "  local-logs        Tail all local logs"
	@echo ""
	@echo "Infrastructure (Cloud):"
	@echo "  init              Initialize Terraform"
	@echo "  plan              Plan infrastructure changes"
	@echo "  apply             Provision Hetzner VMs"
	@echo "  destroy           Destroy all infrastructure"
	@echo ""
	@echo "Cluster Setup:"
	@echo "  cluster-setup     Install k3s on all nodes"
	@echo ""
	@echo "K8s Deployment:"
	@echo "  deploy-k8s        Deploy K8s with custom Relay + VultiServer"
	@echo "  deploy-k8s-prod   Deploy K8s using api.vultisig.com endpoints"
	@echo "  deploy-all        Deploy everything (legacy)"
	@echo "  deploy-infra      Deploy infrastructure services only"
	@echo "  deploy-services   Deploy application services only"
	@echo "  deploy-monitoring Deploy Prometheus and Grafana"
	@echo ""
	@echo "Testing:"
	@echo "  test-smoke        Run smoke tests"
	@echo "  test-partition    Show partition test options"
	@echo ""
	@echo "Utilities:"
	@echo "  logs-verifier     Tail verifier logs"
	@echo "  logs-worker       Tail worker logs"
	@echo "  logs-relay        Tail relay logs"
	@echo "  port-forward      Port forward services for local access"
	@echo "  clean             Remove generated files"

# ============== Infrastructure ==============

init:
	cd $(TERRAFORM_DIR) && terraform init

plan:
	cd $(TERRAFORM_DIR) && terraform plan

apply:
	cd $(TERRAFORM_DIR) && terraform apply

destroy:
	cd $(TERRAFORM_DIR) && terraform destroy

# ============== Cluster Setup ==============

cluster-setup:
	./infrastructure/scripts/setup-cluster.sh

# ============== Kubernetes Deployment ==============

deploy-namespaces:
	kubectl apply -f k8s/base/namespaces.yaml

deploy-secrets:
	@if [ -f k8s/secrets.yaml ]; then \
		kubectl apply -f k8s/secrets.yaml; \
	else \
		echo "ERROR: k8s/secrets.yaml not found"; \
		echo "Copy secrets-template.yaml and fill in values:"; \
		echo "  cp k8s/secrets-template.yaml k8s/secrets.yaml"; \
		exit 1; \
	fi

deploy-infra: deploy-namespaces deploy-secrets
	kubectl apply -f k8s/base/infra/postgres/
	kubectl apply -f k8s/base/infra/redis/
	kubectl apply -f k8s/base/infra/minio/
	@echo "Waiting for infrastructure..."
	kubectl -n infra wait --for=condition=ready pod -l app=postgres --timeout=300s
	kubectl -n infra wait --for=condition=ready pod -l app=redis --timeout=120s
	kubectl -n infra wait --for=condition=ready pod -l app=minio --timeout=120s
	@echo "Infrastructure ready"

deploy-relay:
	kubectl apply -f k8s/base/relay/
	kubectl -n relay wait --for=condition=ready pod -l app=relay --timeout=120s

deploy-verifier:
	kubectl apply -f k8s/base/verifier/
	kubectl -n verifier wait --for=condition=ready pod -l app=verifier --timeout=300s

deploy-dca:
	kubectl apply -f k8s/base/dca/
	kubectl -n plugin-dca wait --for=condition=ready pod -l app=dca --timeout=300s

deploy-vultiserver:
	kubectl apply -f k8s/base/vultiserver/
	kubectl -n vultiserver wait --for=condition=ready pod -l app=vultiserver --timeout=120s

deploy-monitoring:
	kubectl apply -f k8s/base/monitoring/prometheus/
	kubectl apply -f k8s/base/monitoring/grafana/
	kubectl -n monitoring wait --for=condition=ready pod -l app=prometheus --timeout=120s
	kubectl -n monitoring wait --for=condition=ready pod -l app=grafana --timeout=120s

deploy-services: deploy-relay deploy-verifier deploy-dca deploy-vultiserver deploy-monitoring

deploy-all: deploy-infra deploy-services

# Kustomize-based K8s deployment
deploy-k8s: deploy-secrets
	@echo "Deploying K8s with custom Relay + VultiServer..."
	kubectl apply -k k8s/overlays/local
	@echo ""
	@echo "Waiting for pods..."
	kubectl -n infra wait --for=condition=ready pod -l app=postgres --timeout=300s
	kubectl -n infra wait --for=condition=ready pod -l app=redis --timeout=120s
	kubectl -n infra wait --for=condition=ready pod -l app=minio --timeout=120s
	kubectl -n relay wait --for=condition=ready pod -l app=relay --timeout=120s
	kubectl -n vultiserver wait --for=condition=ready pod -l app=vultiserver --timeout=120s
	kubectl -n verifier wait --for=condition=ready pod -l app=verifier --timeout=300s
	kubectl -n plugin-dca wait --for=condition=ready pod -l app=dca --timeout=300s
	@echo ""
	@echo "========================================="
	@echo "  K8s Deployment Complete!"
	@echo "  Relay:       relay.relay.svc.cluster.local"
	@echo "  VultiServer: vultiserver.vultiserver.svc.cluster.local"
	@echo "========================================="
	kubectl get pods --all-namespaces

deploy-k8s-prod: deploy-secrets
	@echo "Deploying K8s with production endpoints (api.vultisig.com)..."
	kubectl apply -k k8s/overlays/production
	@echo ""
	@echo "Waiting for pods..."
	kubectl -n infra wait --for=condition=ready pod -l app=postgres --timeout=300s
	kubectl -n infra wait --for=condition=ready pod -l app=redis --timeout=120s
	kubectl -n infra wait --for=condition=ready pod -l app=minio --timeout=120s
	kubectl -n verifier wait --for=condition=ready pod -l app=verifier --timeout=300s
	kubectl -n plugin-dca wait --for=condition=ready pod -l app=dca --timeout=300s
	@echo ""
	@echo "========================================="
	@echo "  K8s Production Deployment Complete!"
	@echo "  Relay:       https://api.vultisig.com/router"
	@echo "  VultiServer: https://api.vultisig.com"
	@echo "========================================="
	kubectl get pods --all-namespaces

# ============== Testing ==============

test-smoke:
	./tests/smoke-test.sh

test-partition:
	./tests/network-partition-test.sh help

partition-isolate-relay:
	./tests/network-partition-test.sh isolate-service relay

partition-isolate-worker:
	./tests/network-partition-test.sh isolate-service worker

partition-restore:
	./tests/network-partition-test.sh restore

# ============== Utilities ==============

logs-verifier:
	kubectl -n verifier logs -l app=verifier,component=api -f

logs-worker:
	kubectl -n verifier logs -l app=verifier,component=worker -f

logs-relay:
	kubectl -n relay logs -l app=relay -f

logs-dca-worker:
	kubectl -n plugin-dca logs -l app=dca,component=worker -f

port-forward:
	@echo "Starting port forwards..."
	@echo "  Verifier:   http://localhost:8080"
	@echo "  Grafana:    http://localhost:3000"
	@echo "  Prometheus: http://localhost:9090"
	@echo "  MinIO:      http://localhost:9000"
	@echo ""
	kubectl -n verifier port-forward svc/verifier 8080:8080 &
	kubectl -n monitoring port-forward svc/grafana 3000:3000 &
	kubectl -n monitoring port-forward svc/prometheus 9090:9090 &
	kubectl -n infra port-forward svc/minio 9000:9000 &
	@echo "Press Ctrl+C to stop all port forwards"
	@wait

status:
	@echo "=== Cluster Status ==="
	@echo ""
	@echo "Nodes:"
	@kubectl get nodes -o wide
	@echo ""
	@echo "Pods:"
	@kubectl get pods --all-namespaces
	@echo ""
	@echo "Services:"
	@kubectl get svc --all-namespaces

clean:
	rm -rf .kube/
	rm -f setup-env.sh
	rm -rf infrastructure/terraform/.terraform
	rm -f infrastructure/terraform/terraform.tfstate*

# ============== Local Development ==============
# Note: vcli.sh wrapper auto-sets DYLD_LIBRARY_PATH from cluster.yaml

local-build:
	@echo "Building vcli..."
	cd local && go build -o vcli ./cmd/devctl
	@echo "Built: local/vcli"
	@echo "Use ./local/vcli.sh (wrapper) or make local-* commands"

local-start: local-build
	@if [ ! -f local/cluster.yaml ]; then \
		echo "ERROR: local/cluster.yaml not found"; \
		echo "Copy cluster.yaml.example and configure your paths:"; \
		echo "  cp local/cluster.yaml.example local/cluster.yaml"; \
		exit 1; \
	fi
	$(VCLI) start

local-stop:
	@if [ -f ./local/vcli ]; then \
		$(VCLI) stop; \
	else \
		echo "vcli not built. Run: make local-build"; \
	fi

local-status:
	@if [ -f ./local/vcli ]; then \
		$(VCLI) status; \
	else \
		echo "vcli not built. Run: make local-build"; \
	fi

local-logs:
	@echo "=== Verifier ===" && tail -20 /tmp/verifier.log 2>/dev/null || echo "(not running)"
	@echo ""
	@echo "=== Worker ===" && tail -20 /tmp/worker.log 2>/dev/null || echo "(not running)"
	@echo ""
	@echo "=== DCA ===" && tail -20 /tmp/dca.log 2>/dev/null || echo "(not running)"
	@echo ""
	@echo "=== DCA Worker ===" && tail -20 /tmp/dca-worker.log 2>/dev/null || echo "(not running)"

local-clean:
	rm -f local/vcli
	rm -f local/cluster.yaml
	docker compose -f local/configs/docker-compose.yaml down -v 2>/dev/null || true
