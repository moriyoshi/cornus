package hostrun

// NetnsDir is where instance network namespaces are bind-mounted (tmpfs-backed;
// pins survive a cornus restart but not a host reboot — the backends' reboot
// recovery rebuilds them).
//
// It lives in its own untagged file, and is exported, because two very different
// consumers need it and one of them cannot be built with the linux-only network
// code: the CNI manager that CREATES the pins (network_linux.go), and the startup
// preflight that has to check whether the runtime can SEE them
// (pkg/hostcheck, reached via containerdhost.NetnsDir — pkg/hostcheck cannot
// import this internal package directly).
//
// Deliberately outside the data directory. That is what makes it a separate
// problem from every other path this package hands over: a containerized cornus
// binds its DATA dir from the host as a matter of course, but /run is a
// container-private tmpfs, so a pin created here is invisible to a daemon in
// another mount namespace unless the operator binds this directory too. Measured:
// a file created at this path inside a `ctr`-created cornus container is not
// visible from containerd's own mount namespace.
const NetnsDir = "/run/cornus/netns"
