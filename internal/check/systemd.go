package check

import (
	"context"
	"errors"
	"time"

	sd_dbus "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

const (
	SystemctlTimeout   = 5 * time.Second
	UnexpectedExitCode = 255
)

type SystemdClient struct {
	conn *sd_dbus.Conn
}

func NewSystemdClient(ctx context.Context) (*SystemdClient, error) {
	conn, err := sd_dbus.NewWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return &SystemdClient{conn: conn}, nil
}

func (c *SystemdClient) Close() error {
	if c != nil && c.conn != nil {
		c.conn.Close()
	}
	return nil
}

func (c *SystemdClient) SystemdStatus(ctx context.Context, unit string) int {
	if c == nil || c.conn == nil {
		return UnexpectedExitCode
	}
	ctx, cancel := context.WithTimeout(ctx, SystemctlTimeout)
	defer cancel()

	props, err := c.conn.GetUnitPropertiesContext(ctx, unit)
	if err != nil {
		var dbusErr *dbus.Error
		if errors.As(err, &dbusErr) {
			return 1
		}
		return UnexpectedExitCode
	}
	if st, ok := props["ActiveState"].(string); ok && st == "active" {
		return 0
	}
	return 1
}

func (c *SystemdClient) ExpandUnitsByGlob(ctx context.Context, glob string) []string {
	if c == nil || c.conn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, SystemctlTimeout)
	defer cancel()

	units, err := c.conn.ListUnitsByPatternsContext(ctx, []string{"service"}, []string{glob})
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(units))
	for _, u := range units {
		out = append(out, u.Name)
	}
	return out
}
