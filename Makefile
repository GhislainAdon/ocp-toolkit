# ocp-toolkit — cibles de build. `make help` pour la liste.
VERSION ?= 0.1.0
IMAGE   ?= adon1996/ocp-toolkit
LDFLAGS  = -s -w -X github.com/GhislainAdon/ocp-toolkit/internal/cli.Version=$(VERSION)

.PHONY: help build test release docker docker-push clean

help: ## affiche cette aide
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F ':.*## ' '{printf "  %-14s %s\n", $$1, $$2}'

build: ## binaire local (OS courant) dans bin/
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ocptk ./cmd/ocptk

test: ## tests unitaires + vet
	go vet ./...
	go test ./...

release: ## binaires linux/amd64, linux/arm64, windows, darwin dans bin/
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ocptk-linux-amd64 ./cmd/ocptk
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ocptk-linux-arm64 ./cmd/ocptk
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ocptk-windows-amd64.exe ./cmd/ocptk
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/ocptk-darwin-arm64 ./cmd/ocptk

docker: ## image de distribution (ocptk + oc)
	docker build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

docker-push: docker ## push vers Docker Hub
	docker push $(IMAGE):$(VERSION)
	docker push $(IMAGE):latest

clean:
	rm -rf bin/
