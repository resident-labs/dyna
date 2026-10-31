# Container toolchain image

A repository can put `nested-containers` in `.dyna/images/<name>/box-needs`.
Dyna supplies the TUN device and unmasks `/proc/sys` only for that image.
A `kvm` need supplies `/dev/kvm` and keeps the host user’s supplementary groups.
This allows access when the host grants it through a group. It also gives the
box those groups for other mounted files. It requires Podman with crun.
List one need per line.
Dyna refuses any unknown need.

This example uses the Debian packages in the current `dyna-base`. Put it in
`.dyna/images/<name>/Containerfile`:

```Dockerfile
ARG DYNA_BASE
FROM ${DYNA_BASE}
USER root
RUN apt-get update && apt-get install -y --no-install-recommends \
    podman uidmap libcap2-bin passt fuse-overlayfs && \
    chmod u-s /usr/bin/newuidmap /usr/bin/newgidmap && \
    setcap cap_setuid=ep /usr/bin/newuidmap && \
    setcap cap_setgid=ep /usr/bin/newgidmap && \
    printf 'root:1:65535\nnode:1:999\nnode:1001:64535\n' > /etc/subuid && \
    cp /etc/subuid /etc/subgid && rm -rf /var/lib/apt/lists/*
COPY containers.conf /etc/containers/containers.conf
USER node
```

Put this `containers.conf` beside the Containerfile. The system path matters:
Dyna sets HOME to a box volume, not to the image user's home.

```toml
[containers]
volumes = ["/proc:/proc"]
default_sysctls = []
netns = "host"
userns = "host"
ipcns = "host"
utsns = "host"
cgroupns = "host"
cgroups = "disabled"
log_driver = "k8s-file"
[engine]
cgroup_manager = "cgroupfs"
events_logger = "file"
runtime = "crun"
```

These settings bind the box's `/proc` into each inner container. An inner
container can therefore see the box's processes. It does not gain access to
host processes outside the box. Unmasking `/proc/sys` and exposing TUN increase
the kernel attack surface; kernel permissions and namespaces still restrict
the writable subset. Use this image only for work that needs containers.

The rootless network cleanup of Podman 5.8.9 returned a permission error in
our test. Removing the network and starting another container still worked.
Debian's Podman 4.3.1 did not report that error in the same workload.
