GO ?= go
IMAGE ?= harbor-local.aiplatformdev.freeddns.org/ai-platform/pipeline/pipeline-core-service
TAG ?= dev

.PHONY: test vet build image
test:
	$(GO) test ./...
vet:
	$(GO) vet ./...
build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/pipeline-core-service .
image:
	docker build -t $(IMAGE):$(TAG) .
