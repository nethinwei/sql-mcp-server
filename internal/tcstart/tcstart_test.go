package tcstart

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
)

// fake is published when it has a host port.
type fake struct {
	testcontainers.Container
	published  bool
	terminated *int
}

func (c *fake) MappedPort(context.Context, string) (network.Port, error) {
	if !c.published {
		return network.Port{}, errdefs.ErrNotFound.WithMessage(`port "3306/tcp" not found`)
	}
	return network.MustParsePort("55073/tcp"), nil
}

func (c *fake) Terminate(context.Context, ...testcontainers.TerminateOption) error {
	*c.terminated++
	return nil
}

// A container without its host port is terminated and started again, until
// one has it; another error ends the start at once.
func TestRunStartsAgainWithoutAHostPort(t *testing.T) {
	terminated, started := 0, 0
	c, err := Run(context.Background(), func(testcontainers.CustomizeRequestOption) (*fake, error) {
		started++
		return &fake{published: started == 3, terminated: &terminated}, nil
	}, "3306/tcp")
	if err != nil || !c.published || started != 3 || terminated != 2 {
		t.Fatalf("container %+v, started %d, terminated %d, err %v", c, started, terminated, err)
	}
	if _, err := Run(context.Background(), func(testcontainers.CustomizeRequestOption) (*fake, error) {
		return &fake{terminated: &terminated}, nil
	}, "3306/tcp"); !errors.Is(err, errdefs.ErrNotFound) {
		t.Fatalf("after every attempt: %v", err)
	}
	refused := errors.New("no such image")
	started = 0
	if _, err := Run(context.Background(), func(testcontainers.CustomizeRequestOption) (*fake, error) {
		started++
		return nil, refused
	}, "3306/tcp"); !errors.Is(err, refused) || started != 1 {
		t.Fatalf("another error: %v after %d starts", err, started)
	}
}

// publish binds every port to a host port the host has free, after the
// request's own host configuration.
func TestPublishBindsFreeHostPorts(t *testing.T) {
	publish, err := freeHostPorts([]string{"3306/tcp", "33060/tcp"})
	if err != nil {
		t.Fatal(err)
	}
	req := testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		HostConfigModifier: func(hc *container.HostConfig) { hc.ShmSize = 1 },
	}}
	if err := Apply(&req, publish); err != nil {
		t.Fatal(err)
	}
	var hc container.HostConfig
	req.HostConfigModifier(&hc)
	if hc.ShmSize != 1 || len(hc.PortBindings) != 2 {
		t.Fatalf("host config = %+v", hc)
	}
	for port, bindings := range hc.PortBindings {
		host, _ := strconv.Atoi(bindings[0].HostPort)
		l, err := net.Listen("tcp", ":"+bindings[0].HostPort)
		if host == 0 || err != nil {
			t.Fatalf("%s: host port %q is not free: %v", port, bindings[0].HostPort, err)
		}
		_ = l.Close()
	}
}
