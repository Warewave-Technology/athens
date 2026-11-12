//go:build integration

package httpapi

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/Warewave-Technology/athens/internal/discovery"
	"github.com/Warewave-Technology/athens/internal/testutil/pgtest"
)

var instances []*pgtest.Instance

func TestMain(m *testing.M) {
	ctx := context.Background()
	for _, img := range pgtest.Versions {
		in, err := pgtest.Start(ctx, img)
		if err == nil {
			err = seed(ctx, in)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "start", img, err)
			os.Exit(1)
		}
		instances = append(instances, in)
	}
	code := m.Run()
	for _, in := range instances {
		in.Stop(ctx)
	}
	os.Exit(code)
}

func seed(ctx context.Context, in *pgtest.Instance) error {
	c, err := in.Connect(ctx, "app")
	if err != nil {
		return err
	}
	defer func() { _ = c.Close(ctx) }()
	_, err = c.Exec(ctx, pgtest.SeedSQL+`GRANT USAGE ON SCHEMA shop TO `+pgtest.ROUser+`; GRANT SELECT ON ALL TABLES IN SCHEMA shop, public TO `+pgtest.ROUser)
	return err
}

// dbHarness starts an API server whose only target is "itest/db" on in.
func dbHarness(t *testing.T, in *pgtest.Instance, mutate func(*discovery.Target)) (*harness, discovery.Target) {
	t.Helper()
	tg, client := in.Target("db")
	if mutate != nil {
		mutate(&tg)
	}
	return newHarness(t, harnessOpts{targets: []discovery.Target{tg}, client: client}), tg
}

func each(t *testing.T, fn func(t *testing.T, in *pgtest.Instance)) {
	for _, in := range instances {
		t.Run(in.Image, func(t *testing.T) { fn(t, in) })
	}
}

const dbPath = "/api/v1/targets/itest/db/db/app"
