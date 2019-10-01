#!/usr/bin/env bash

# golang 1.6 is no longer supported
# https://github.com/golang/go/issues/26576
sudo add-apt-repository -y ppa:gophers/archive

apt-get -qq update

# https://pkg-go.alioth.debian.org/packaging.html
apt-get install -y lintian \
                   build-essential \
                   equivs \
                   dpatch \
                   fakeroot \
                   devscripts \
                   quilt \
                   ruby \
                   ruby-dev \
                   rubygems \
                   tig \
                   golang-1.10-go

gem install --no-ri --no-rdoc fpm

export PATH=$PATH:/usr/lib/go-1.10/bin

home="/home/vagrant"
mkdir -p ${home}/.go/bin

export GOPATH=${home}/.go
export GOBIN=${home}/.go/bin
export PATH=$PATH:$GOBIN

touch ${home}/.bashrc
{
echo "export GOPATH=${home}/.go"
echo "export GOBIN=${home}/.go/bin"
echo "export PATH=$PATH:$GOBIN"
echo "export PATH=$PATH:/usr/local/go/bin"

echo "cd /vagrant"
} >> "${home}/.bashrc"

chown -R vagrant:vagrant /home/vagrant/.go

# add raingutter user/group
adduser --system --no-create-home --group raingutter

echo -e "\\nInstalling raingutter dependencies...\\n"
go get -v github.com/DataDog/datadog-go/statsd
go get -v github.com/sirupsen/logrus
go get -v github.com/prometheus/client_golang/prometheus
go get -v github.com/prometheus/client_golang/prometheus/promauto
go get -v github.com/prometheus/client_golang/prometheus/promhttp

go version

echo -e "\\nSystem is ready.\\n1. $ vagrant ssh systemd\\n2. $ make systemd_pkg"
