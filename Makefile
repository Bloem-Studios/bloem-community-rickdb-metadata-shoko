.PHONY: build rebuild clean test vet verify docker-copy

BINARY=shoko_plugin
VERSION ?= 0.2.5
LDFLAGS=-s -w -X main.version=$(VERSION)

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

rebuild: clean
	go clean -cache
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

test:
	go test ./...

vet:
	go vet ./...

verify: test vet build

clean:
	rm -f $(BINARY)

docker-copy: rebuild
	docker cp $(BINARY) silo-atlantis:/app/plugins/shoko/anime.shoko_plugin
	docker exec silo-atlantis chmod +x /app/plugins/shoko/anime.shoko_plugin
	docker restart silo-atlantis
	@echo "✅ Deployed to silo-atlantis"
