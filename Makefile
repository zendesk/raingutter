.PHONY: build clean

VERSION := $(shell git describe --abbrev=0 --tags | sed 's/v//')
NAMESPACES := "unicorn-raindrops" "unicorn-socket-stats" "puma-socket-stats"

ensure_deps:
	go mod vendor
	go mod tidy

clean:
	rm -f *.dsc *.tar.gz *.changes bin/raingutter

build: clean
	go test ./raingutter -v
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -mod=vendor -ldflags "-X main.version=${version}" -o bin/raingutter raingutter/raingutter.go raingutter/socket_stats.go raingutter/prometheus.go

setup-skaffold:
	$(foreach var,$(NAMESPACES),kubectl create namespace $(var);)
