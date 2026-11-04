//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// idmap SRC DST UID makes the mount at SRC appear at DST with root's files
// owned by UID: an idmapped mount through a one-entry user namespace. The
// read-only repo overlay is built by root and the host share reports every
// file as root's; this is what lets the session user edit the merged tree
// without a copy-up per file.
func idmap(args []string) {
	if len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: llok-guest idmap SRC DST UID")
		os.Exit(2)
	}
	src, dst := args[0], args[1]
	uid, err := strconv.Atoi(args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "llok-guest idmap: bad uid")
		os.Exit(2)
	}
	// A user namespace exists only while a process lives in it: park a
	// child there for the duration of the mount setup.
	child := exec.Command("/proc/self/exe", "idmap-hold")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: uid, HostID: 0, Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: uid, HostID: 0, Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	if err := child.Start(); err != nil {
		fail("user namespace", err)
	}
	defer child.Process.Kill()
	userns, err := unix.Open(fmt.Sprintf("/proc/%d/ns/user", child.Process.Pid), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		fail("open userns", err)
	}
	defer unix.Close(userns)

	tree, err := unix.OpenTree(unix.AT_FDCWD, src, unix.OPEN_TREE_CLONE|unix.OPEN_TREE_CLOEXEC)
	if err != nil {
		fail("open_tree", err)
	}
	defer unix.Close(tree)
	attr := unix.MountAttr{Attr_set: unix.MOUNT_ATTR_IDMAP, Userns_fd: uint64(userns)}
	if err := unix.MountSetattr(tree, "", unix.AT_EMPTY_PATH, &attr); err != nil {
		fail("mount_setattr idmap", err)
	}
	if err := unix.MoveMount(tree, "", unix.AT_FDCWD, dst, unix.MOVE_MOUNT_F_EMPTY_PATH); err != nil {
		fail("move_mount", err)
	}
}

// idmapHold is the parked child: it only needs to exist.
func idmapHold() {
	time.Sleep(time.Hour)
}

func fail(what string, err error) {
	fmt.Fprintln(os.Stderr, "llok-guest idmap:", what+":", err)
	os.Exit(1)
}
