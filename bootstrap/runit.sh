#!/usr/bin/env bash

echo "deb http://apt.datadoghq.com/ stable main" > /etc/apt/sources.list.d/datadog.list
apt-key adv --keyserver keyserver.ubuntu.com --recv-keys 4B4593018387EEAF

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
                   tig \
                   runit \
                   wget \
                   git-buildpackage \
                   ruby \
                   ruby-dev \
                   rubygems-integration \
                   golang-1.10-go \
                   datadog-agent=1:5.1.1-546

gem install --no-ri --no-rdoc fpm

mv /etc/dd-agent/datadog.conf.example /etc/dd-agent/datadog.conf \
 && sed -i -e"s/^.*non_local_traffic:.*$/non_local_traffic: yes/" /etc/dd-agent/datadog.conf \
 && sed -i -e"s/^.*log_to_syslog:.*$/log_to_syslog: no/" /etc/dd-agent/datadog.conf \
 && sed -i -e"s/^.*api_key:.*$/api_key: testing/" /etc/dd-agent/datadog.conf

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

# add raingutter user/group
adduser --system --no-create-home --group raingutter

echo -e "\\nInstalling raingutter dependencies...\\n"
go get -v github.com/DataDog/datadog-go/statsd
go get -v github.com/sirupsen/logrus
go get -v github.com/prometheus/client_golang/prometheus
go get -v github.com/prometheus/client_golang/prometheus/promauto
go get -v github.com/prometheus/client_golang/prometheus/promhttp
chown -R vagrant:vagrant /home/vagrant/.go

go version

echo -e "\\nSystem is ready.\\n1. $ vagrant ssh runit\\n2. $ make runit_pkg"
