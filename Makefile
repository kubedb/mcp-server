VERSION ?= v0.1.0
IMG     ?= ghcr.io/kubedb/mcp-server:$(VERSION)

.PHONY: build
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/kubedb-mcp ./cmd/kubedb-mcp

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: image
image:
	docker build -t $(IMG) --build-arg VERSION=$(VERSION) -f Dockerfile .

.PHONY: inspector
inspector: build
	npx @modelcontextprotocol/inspector ./bin/kubedb-mcp

.PHONY: deploy
deploy:
	kubectl create namespace kubedb-mcp --dry-run=client -o yaml | kubectl apply -f -
	kubectl apply -f deploy/openshift/rbac.yaml
	kubectl apply -f deploy/openshift/mcpserver.yaml
