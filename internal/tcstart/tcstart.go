// Package tcstart starts test containers on host ports the host has free.
//
// Docker Desktop on macOS publishes a container's port by listening on the
// host port the VM allocated. The VM allocates host ports in order, and so
// does macOS its ephemeral ports (the outbound connections to other
// containers): when they meet, the host port is in use, publishing fails
// ("bind: address already in use" in the Docker Desktop log), and the
// container has no host port ("port "3306/tcp" not found"). A port the host
// itself just handed out and let go is free, and its next ephemeral ports
// are past it.
package tcstart

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
)

// Publish is the option publishing a container's ports (see Run).
type Publish = testcontainers.CustomizeRequestOption

// attempts is how many containers Run starts at most.
const attempts = 3

// Run runs start, which starts a container with publish among its options,
// publish binding each of ports (every port the container exposes) to a
// host port the host has free. A container without a host port for one of
// them, or one whose host port the daemon could not bind (a race lost all the
// same), is terminated and started again, up to attempts times.
func Run[C testcontainers.Container](
	ctx context.Context, start func(publish Publish) (C, error), ports ...string,
) (C, error) {
	var c C
	var err error
	for range attempts {
		var publish Publish
		if publish, err = freeHostPorts(ports); err != nil {
			return c, err
		}
		c, err = start(publish)
		if err == nil {
			err = published(ctx, c, ports)
		}
		if err == nil || !retryable(err) {
			return c, err
		}
		_ = testcontainers.TerminateContainer(c) // nil when it was not created
	}
	return c, fmt.Errorf("tcstart: no host port after %d containers: %w", attempts, err)
}

// retryable reports whether err is the macOS port race: the container has no
// host port for one of the ports, or the daemon could not bind the host port
// it was given (the other form of the same collision).
func retryable(err error) bool {
	if errors.Is(err, errdefs.ErrNotFound) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "ports are not available") || strings.Contains(s, "address already in use")
}

// Apply applies publish to req, a request built by hand.
func Apply(req *testcontainers.GenericContainerRequest, publish Publish) error {
	return publish(req)
}

// freeHostPorts binds each of ports to a host port the host has free now.
// It sets the request's host configuration, after any other modifier the
// request has: a request with none takes Docker's defaults otherwise.
func freeHostPorts(ports []string) (Publish, error) {
	bindings := network.PortMap{}
	for _, p := range ports {
		port, err := network.ParsePort(p)
		if err != nil {
			return nil, err
		}
		host, err := freePort()
		if err != nil {
			return nil, err
		}
		bindings[port] = []network.PortBinding{{HostPort: host}}
	}
	return func(req *testcontainers.GenericContainerRequest) error {
		before := req.HostConfigModifier
		req.HostConfigModifier = func(hc *container.HostConfig) {
			if before != nil {
				before(hc)
			}
			hc.PortBindings = bindings
		}
		return nil
	}, nil
}

// freePort is a host port the host has free: one it hands out, let go.
func freePort() (string, error) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		return "", err
	}
	defer func() { _ = l.Close() }()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port), nil
}

// published checks c has a host port for each of ports.
func published(ctx context.Context, c testcontainers.Container, ports []string) error {
	for _, port := range ports {
		if _, err := c.MappedPort(ctx, port); err != nil {
			return err
		}
	}
	return nil
}
