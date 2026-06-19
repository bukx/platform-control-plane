APP_NAME := platform-control-plane

.PHONY: fmt test build run cli-classes cli-request-create cli-request-list dev-stack-up dev-stack-down

fmt:
	go fmt ./...

test:
	go test ./...

build:
	mkdir -p bin
	go build -o bin/platformd ./cmd/platformd
	go build -o bin/platformctl ./cmd/platformctl

run:
	PLATFORM_APPROVAL_HMAC_SECRET=$${PLATFORM_APPROVAL_HMAC_SECRET:-dev-approval-secret} \
	PLATFORM_JWT_HS256_SECRET=$${PLATFORM_JWT_HS256_SECRET:-dev-jwt-secret} \
	go run ./cmd/platformd

cli-classes:
	TOKEN=$$(go run ./cmd/platformctl token mint --subject viewer --actor viewer@example.com --role viewer --secret $${PLATFORM_JWT_HS256_SECRET:-dev-jwt-secret}); \
	go run ./cmd/platformctl classes --token "$$TOKEN"

cli-request-create:
	TOKEN=$$(go run ./cmd/platformctl token mint --subject requester --actor requester@example.com --role requester --secret $${PLATFORM_JWT_HS256_SECRET:-dev-jwt-secret}); \
	go run ./cmd/platformctl request create \
		--app payments-api \
		--team platform \
		--class preview \
		--region us-east-1 \
		--ttl 24 \
		--owner mcmoney \
		--repo https://github.com/mcmoney/payments-api \
		--token "$$TOKEN"

cli-request-list:
	TOKEN=$$(go run ./cmd/platformctl token mint --subject viewer --actor viewer@example.com --role viewer --secret $${PLATFORM_JWT_HS256_SECRET:-dev-jwt-secret}); \
	go run ./cmd/platformctl request list --token "$$TOKEN"

dev-stack-up:
	docker compose up -d postgres jaeger otel-collector

dev-stack-down:
	docker compose down
