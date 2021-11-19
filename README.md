![repo-checks](https://github.com/zendesk/raingutter/workflows/repo-checks/badge.svg)
## Overview

For pre-forking HTTP servers like [Unicorn](https://bogomips.org/unicorn/), workers utilization is an important capacity indicator, which can help to avoid requests queuing.

In Zendesk we have developed `Raingutter` to perform high frequency polling of `Unicorn` utilization stats in order to get a better visibility of our capacity utilization and unveil spikes.

A good understanding of our application capacity utilization, has been particularly helpful to properly scale our application during the migration to K8s.
The metrics generated by Raingutter are also helpful to implement the K8s Horizontal Pod Autoscaler, based on an indicator that directly correlates to capacity exaustions.

Other solutions exists, but they all have their caveats:
* It is not trivial to have these working seamlessly on both VMs and K8s
* They don't offer enough granularity (eg: `Datadog Agent v5` can only execute custom checks every 15s)
* There is no guarantee that metrics are always collected (eg: `Datadog Agent v6` collector works on a best effort model)

## Design

Raingutter currently supports three methods of collecting TCP connections metrics:

* Built in socket monitoring based on `NETLINK_SOCK_DIAG`: information about active TCP connections are retrieved from the OS, no external dependencies required (recommended).
* Built in socket monitoring based on `/proc/net/tcp`: can generate the same information as netlink, but slower due to the added string processing required.
* [Raindrops gem](https://bogomips.org/raindrops/) a real-time stats toolkit to show unicorn statistics

Multi-threaded web server like `Puma` can be also monitored by `Raingutter` with the built in socket monitoring.

The stats polled by Raingutter can be streamed as [histograms](https://docs.datadoghq.com/developers/dogstatsd/#histograms) to [dogstatsd](https://docs.datadoghq.com/developers/dogstatsd/), exposed as Prometheus endpoint and/or printed to STDOUT as JSON.

![datadog-example.png](https://i.postimg.cc/rpC9f9XB/datadog-example.png)

## Usage

Raingutter is a single binary that can either run as a [sidecar container](https://kubernetes.io/docs/concepts/workloads/pods/pod-overview/) if your application is deployed on Kubernetes or as daemon (both `systemd` and `runit` are supported).
More information about how to build a `deb` package can be found in the build section

The following environment variables can be used to configure Raingutter:

* `RG_SOCKET_STATS_MODE`: How socket stats will be collected. Set to one of:
    * `proc_net` - parse the stats from the string data in `/proc/net/tcp` (default)
    * `netlink` - reads stats from a `NETLINK_SOCK_DIAG` netlink socket.
    * `raindrops` - parse stats from the raindrops URL (must set `RG_RAINDROPS_URL`)
* `RG_USE_SOCKET_STATS`: Deprecated - use `RG_SOCKET_STATS_MODE` instead. If set to `true`, will behave like `RG_SOCKET_STATS_MODE == "proc_net"`, and if set to `false` will behave like `RG_SOCKET_STATS_MODE == "raindrops"`.
* `RG_FREQUENCY`: Polling frequency in milliseconds (default: `500`)
* `RG_SERVER_PORT`: Where the web server listens to
* `RG_MEMORY_STATS_ENABLED`: If enabled, attempt to collect memory usage statistics for processers listening on `RG_SERVER_PORT`. This is most useful for preforking webservers like unicorn, where it will measure how much memory is copy-on-write shared between processes. If using this feature, you should NOT use `RG_SOCKET_STATS_MODE=netlink` - Raingutter relies on lining up the listener socket inode numbers with `/proc/$pid/fd/` to find out which processes are listening on a socket. The inode is stored in the kernel as a 64-bit integer, however the INET_DIAG netlink API only exposes it as a 32-bit integer, doing silent wrap around! This means that if you use `RG_SOCKET_STATS_MODE=netlink`, `RG_MEMORY_STATS_ENABLED` might simply fail to generate any metrics at all if your system has had a lot of sockets.
* `RG_PROC_DIRECTORY`: Path to `/proc` directory to use. Useful when running in a container to point to a path where the host's `/proc` directory is mounted.

##### Pre-fork web servers (Unicorn)
* `UNICORN_WORKERS`: Total number of unicorn workers (required if running on K8s)
* `RG_RAINDROPS_URL`: Raindrops endpoint URL (eg: `http://127.0.0.1:3000/_raindrops`). Only required if Raindrops is used as collection method.

##### Multi-threaded web servers (Puma)
* `RG_THREADS`: Enabled support for multi-threaded web servers
* `MAX_THREADS`: Total number of allowed threads

##### METRIC TAGS
* `POD_NAME`: Name of the k8s pod (required)
* `POD_NAMESPACE`: K8s pod namespace (required)
* `PROJECT`: Project tag (required)

##### STATSD
* `RG_STATSD_ENABLED`: If set to `true` metrics are streamed to the dogstatsd histogram interface (default: `true`)
* `RG_STATSD_HOST`: IP address of the local dogstatsd instance (required if `RG_STATSD_ENABLED` is `true`)
* `RG_STATSD_PORT`: Port number of the local dogstatsd instance (required if `RG_STATSD_ENABLED` is `true`)
* `RG_STATSD_NAMESPACE`: A string to prepend to all statsd calls (default: `unicorn.raingutter.agg.`)
* `RG_STATSD_EXTRA_TAGS`: A list of extra tags to be passed to dogstatsd, as comma-separated key:value pairs (ie. `tagname:tagvalue,anothertag:anothervalue`)

##### PROMETHEUS
* `RG_PROMETHEUS_ENABLED`: If set to `true` metrics are exposed to `<IP>:8000/metrics` (default: `false`)

##### LOGS
* `RG_LOG_METRICS_ENABLED`: If set to `true` metrics are logged to STDOUT in JSON format (default: `false`)


## Add raingutter as sidecar container

![k8s.png](https://i.postimg.cc/CKVNCQtC/k8s.png)

#### Example:
```
---
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: my-unicorn-app
  labels:
    project: my-unicorn-app
    role: app-server
spec:
  selector:
    matchLabels:
      project: my-unicorn-app
      role: app-server
  template:
   spec:
      containers:
      - name: unicorn
        image: unicorn:latest
        ports:
          - name: main-port
            containerPort: 3000
            protocol: TCP
        env:
        - name: UNICORN_WORKERS
          value: '16'
      - name: raingutter
        image: raingutter:latest
        env:
        - name: PROJECT
          value: 'my-unicorn-app'
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        - name: RG_USE_SOCKET_STATS
          value: 'true'
        - name: RG_SERVER_PORT
          value: '3000'
        - name: RG_STATSD_HOST
          value: '192.168.0.1'
        - name: RG_STATSD_PORT
          value: '8125'
        - name: RG_FREQUENCY
          value: '500'
        - name: UNICORN_WORKERS
          value: '16'
       securityContext:
          runAsNonRoot: true
          readOnlyRootFilesystem: true
```

## Build

Build a Debian package for Ubuntu 16.04 (systemd)

```
$ vagrant up systemd
$ vagrant ssh systemd
$ make systemd_pkg
```

## Development

Setup a local k8s testing environment with Skaffold:

1. Make sure to have Kubernetes available locally (e.g.: `minikube`)
2. Setup the correct namespaces
```
make setup-skaffold
```
3. Install [Skaffold](https://github.com/GoogleContainerTools/skaffold) and run it in development mode
```
$ skaffold dev
```
4. (OPTIONAL) Install Prometheus and Grafana on K8s
```
$ helm init
$ helm install \
    --name=prometheus \
    --version=7.0.0 \
    stable/prometheus
$ helm install \
    --name=grafana \
    --version=1.12.0 \
    --set=adminUser=somepassword \
    --set=adminPassword=somepassword \
    --set=service.type=NodePort \
    stable/grafana
```
5. Get the service port via: `kubectl get svc` and login on `http://localhost:<service-port>/login`
6. Use port-forward to hit the Prometheus endpoint
```
export pod=$(kubectl get pods -l app=rg -o go-template --template '{{range .items}}{{.metadata.name}}{{"\n"}}{{end}}'); kubectl port-forward $pod 8000
```

## Contributors

Sean Goedecke <sgoedecke@zendesk.com>: implementation of the built in socket monitoring (based on `/proc/net/tcp`)

## Contributing

Create a Pull Request with your changes, ping someone and we'll look at getting it merged.

## Copyright and license

Copyright 2019 Zendesk

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the License.

You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and limitations under the License.
