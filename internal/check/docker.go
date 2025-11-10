package check

import (
	"context"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

type DockerChecker struct{ cli *client.Client }

func NewDockerChecker() (*DockerChecker, error) {
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, err
	}
	return &DockerChecker{cli: cli}, nil
}

func (d *DockerChecker) AllRunningNames(
	ctx context.Context,
	names []string,
) int {
	for _, n := range names {
		inspect, err := d.cli.ContainerInspect(ctx, n)
		if err != nil || inspect.State == nil || !inspect.State.Running {
			return 1
		}
	}
	return 0
}

func (d *DockerChecker) AllRunningByLabels(
	ctx context.Context,
	labels []string,
) int {
	if len(labels) == 0 {
		return 1
	}
	f := filters.NewArgs()
	for _, kv := range labels {
		if kv == "" {
			continue
		}
		f.Add("label", kv)
	}
	list, err := d.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: f,
	})
	if err != nil || len(list) == 0 {
		return 1
	}
	for _, c := range list {
		if c.State != "running" {
			return 1
		}
	}
	return 0
}
