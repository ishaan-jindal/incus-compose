package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2ENATProxyWithPort(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()

	dir := writeTempFiles(t, map[string]string{
		"compose.yaml": `services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"
`,
	})
	compose := filepath.Join(dir, "compose.yaml")

	t.Cleanup(func() {
		_, _ = runCommand(ctx, t, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(ctx, t, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	c := projectClient(ctx, t, pn)
	conn, err := c.Connection()
	require.NoError(t, err)

	inst, _, err := conn.GetInstance("web-1")
	require.NoError(t, err)

	proxy, ok := inst.Devices["proxy-8080"]
	require.True(t, ok, "proxy-8080 device should exist")
	assert.Contains(t, proxy["connect"], "0.0.0.0:80")
	assert.Equal(t, "true", proxy["nat"])
}

func TestE2ENATProxyWithPortAndStaticIP(t *testing.T) {
	t.Parallel()
	skipLocal(t)
	skipE2E(t)

	ctx := context.Background()
	pn := t.Name()

	dir := writeTempFiles(t, map[string]string{
		"compose.yaml": `services:
  web:
    image: docker.io/nginx:alpine
    ports:
      - "8080:80"
    networks:
      frontend:
        ipv4_address: 10.131.245.2

networks:
  frontend:
    x-incus:
      ipv4.address: 10.131.245.1/24
`,
	})
	compose := filepath.Join(dir, "compose.yaml")

	t.Cleanup(func() {
		_, _ = runCommand(ctx, t, pn, "-f", compose, "down", "--project")
	})

	_, err := runCommand(ctx, t, pn, "-f", compose, "up", "--detach")
	require.NoError(t, err)

	c := projectClient(ctx, t, pn)
	conn, err := c.Connection()
	require.NoError(t, err)

	inst, _, err := conn.GetInstance("web-1")
	require.NoError(t, err)

	proxy, ok := inst.Devices["proxy-8080"]
	require.True(t, ok, "proxy-8080 device should exist")
	assert.Contains(t, proxy["connect"], "10.131.245.2:80")
	assert.Equal(t, "true", proxy["nat"])
}
