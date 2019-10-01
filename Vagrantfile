# -*- mode: ruby -*-
# vi: set ft=ruby :

Vagrant.configure('2') do |config|
  config.vm.define 'systemd' do |systemd|
    systemd.vm.box = 'ubuntu/xenial64'
    systemd.vm.provision :shell, path: 'bootstrap/systemd.sh'
  end
  config.vm.define 'runit' do |runit|
    runit.vm.box = 'ubuntu/trusty64'
    runit.vm.provision :shell, path: 'bootstrap/runit.sh'
  end
end
