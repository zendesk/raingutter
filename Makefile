.PHONY: build clean

VERSION := $(shell git describe --abbrev=0 --tags | sed 's/v//')
DEBIAN_DESCRIPTION := "Raingutter performs high frequency polling of Unicorn utilization stats"
DEBIAN_MAINTAINER := "GUIDEOPS <guideops@zendesk.com>"
DEBIAN_URL := "https://github.com/zendesk/raingutter"
DEBIAN_NAME := "raingutter"
DEBIAN_ARCH := "amd64"
DEBIAN_VENDOR := "Zendesk"
NAMESPACES := "unicorn-raindrops" "unicorn-socket-stats" "puma-socket-stats"

ensure_deps:
	go mod vendor
	go mod tidy

clean:
	rm -f *.deb *.dsc *.tar.gz *.changes bin/raingutter

build: clean
	go test ./raingutter -v
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -mod=vendor -ldflags "-X main.version=${version}" -o bin/raingutter raingutter/raingutter.go raingutter/socket_stats.go raingutter/prometheus.go

systemd_pkg: build
	fpm --input-type dir \
		--output-type deb \
		--architecture $(DEBIAN_ARCH) \
		--version $(VERSION) \
		--depends systemd \
		--vendor $(DEBIAN_VENDOR) \
		--maintainer $(DEBIAN_MAINTAINER) \
		--description $(DEBIAN_DESCRIPTION) \
		--url $(DEBIAN_URL) \
		--name $(DEBIAN_NAME) \
		--verbose \
		./bin=/usr/
	dpkg -I raingutter*.deb

runit_pkg: build
	fpm --input-type dir \
		--output-type deb \
		--architecture $(DEBIAN_ARCH) \
		--version $(VERSION) \
		--depends runit \
		--vendor $(DEBIAN_VENDOR) \
		--maintainer $(DEBIAN_MAINTAINER) \
		--description $(DEBIAN_DESCRIPTION) \
		--url $(DEBIAN_URL) \
		--name $(DEBIAN_NAME) \
		--verbose \
		./bin=/usr/
	dpkg -I raingutter*.deb

setup-skaffold:
	$(foreach var,$(NAMESPACES),kubectl create namespace $(var);)
