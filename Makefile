.PHONY: setup build test local-up local-down simulate load-test logs k8s-deploy k8s-test k8s-down monitoring-up monitoring-down

setup:
	cd controller && go mod download
	cd device-agent && go mod download
	cd simulator && go mod download
	cd provisioning && go mod download
	@echo "Setup complete. Run 'make test' then 'make local-up'."

build:
	mkdir -p bin
	cd controller && go build -o ../bin/controller ./cmd/controller
	cd device-agent && go build -o ../bin/device-agent ./cmd/device-agent
	cd simulator && go build -o ../bin/simulator ./cmd/simulator && go build -o ../bin/load-test ./cmd/load-test
	cd provisioning && go build -o ../bin/bulk-provision ./cmd/bulk-provision

test:
	cd controller && go vet ./... && go test ./...
	cd device-agent && go vet ./... && go test ./...
	cd simulator && go vet ./... && go test ./...
	cd provisioning && go vet ./... && go test ./...

local-up:
	docker compose up --build -d
	@echo "Controller:       http://localhost:8000/ui/"
	@echo "EMQX dashboard:   http://localhost:18083  (admin/public)"
	@echo "Waiting for controller to become ready..."
	@for i in $$(seq 1 30); do \
		if curl -sf http://localhost:8000/readyz > /dev/null 2>&1; then echo "Controller is ready."; exit 0; fi; \
		sleep 2; \
	done; \
	echo "Controller did not become ready in time — check 'make logs'"; exit 1

local-down:
	docker compose down -v

logs:
	docker compose logs -f

simulate:
	cd simulator && go run ./cmd/simulator --count 10 --duration 60s \
		--api-endpoint http://localhost:8000 --mqtt-host localhost --admin-api-key dev-admin-key

load-test:
	cd simulator && go run ./cmd/load-test --count $${COUNT:-100} \
		--api-endpoint http://localhost:8000 --mqtt-host localhost --admin-api-key dev-admin-key \
		--report-dir ../docs/load-test-results

monitoring-up:
	docker compose -f docker-compose.yml -f docker-compose.monitoring.yml up -d prometheus grafana
	@echo "Prometheus: http://localhost:9090"
	@echo "Grafana:    http://localhost:3000  (anonymous viewer access enabled; admin/admin for login)"

monitoring-down:
	docker compose -f docker-compose.yml -f docker-compose.monitoring.yml stop prometheus grafana

KIND_CLUSTER := device-controller
HELM_NAMESPACE := device-controller
HELM_CHART := kubernetes/helm/device-controller

k8s-deploy:
	kind get clusters 2>/dev/null | grep -q "^$(KIND_CLUSTER)$$" || kind create cluster --name $(KIND_CLUSTER)
	docker build -t cloud-device-controller-controller:latest ./controller
	kind load docker-image cloud-device-controller-controller:latest --name $(KIND_CLUSTER)
	helm upgrade --install device-controller $(HELM_CHART) -f $(HELM_CHART)/values-dev.yaml \
		--create-namespace --namespace $(HELM_NAMESPACE) --timeout 3m
	kubectl -n $(HELM_NAMESPACE) rollout status deployment/device-controller --timeout=90s

k8s-test:
	kubectl -n $(HELM_NAMESPACE) get pods
	kubectl -n $(HELM_NAMESPACE) run smoke-curl --rm -i --restart=Never --image=curlimages/curl -- \
		curl -sf http://device-controller:8000/readyz
	@echo "See docs/deployment.md for the full manual verification steps (register/enroll/command)."

k8s-down:
	helm uninstall device-controller --namespace $(HELM_NAMESPACE) || true
	kind delete cluster --name $(KIND_CLUSTER)
