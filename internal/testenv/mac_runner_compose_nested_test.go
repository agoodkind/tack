//go:build darwin && integration

package testenv_test

import (
	"os"
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func TestMacRunnerRejectsUnsupportedNestedCompose(t *testing.T) {
	t.Run("aliased bind options", func(t *testing.T) {
		options, _ := macFixture(t)
		compose := macRead(t, options.Root, "docker-compose.test.yml")
		anchor := "services:\n  fixture:\n    image: unused\n    volumes: &fixture_mounts\n      - type: bind\n        source: /var/run/docker.sock\n        target: /var/run/docker.sock\n        bind:\n          create_host_path: true\n"
		compose = strings.Replace(compose, "services:\n", anchor, 1)
		compose = strings.Replace(compose, "- /var/run/docker.sock:/var/run/docker.sock", "- *fixture_mount", 1)
		compose = strings.Replace(compose, "      - type: bind", "      - &fixture_mount\n        type: bind", 1)
		macRefuseNestedCompose(t, options, compose, "bind.create_host_path")
	})
	for _, test := range []struct {
		field   string
		options string
	}{
		{field: "volume.subpath", options: "volume:\n          subpath: nested"},
		{field: "volume.nocopy", options: "volume:\n          nocopy: true"},
		{field: "volume.labels", options: "volume:\n          labels: {fixture: test}"},
		{field: "consistency", options: "consistency: cached"},
	} {
		t.Run(test.field, func(t *testing.T) {
			options, _ := macFixture(t)
			compose := macRead(t, options.Root, "docker-compose.test.yml")
			mount := "- type: volume\n        source: tack-test-go-cache\n        target: /root/.cache/go-build\n        " + test.options
			compose = strings.Replace(compose, "- tack-test-go-cache:/root/.cache/go-build", mount, 1)
			macRefuseNestedCompose(t, options, compose, test.field)
		})
	}
	for _, test := range []struct {
		field string
		value string
	}{
		{field: "selinux", value: "z"},
		{field: "propagation", value: "shared"},
		{field: "recursive", value: "disabled"},
		{field: "create_host_path", value: "true"},
	} {
		t.Run("bind."+test.field, func(t *testing.T) {
			options, _ := macFixture(t)
			compose := macRead(t, options.Root, "docker-compose.test.yml")
			mount := "- type: bind\n        source: /var/run/docker.sock\n        target: /var/run/docker.sock\n        bind:\n          " + test.field + ": " + test.value
			compose = strings.Replace(compose, "- /var/run/docker.sock:/var/run/docker.sock", mount, 1)
			macRefuseNestedCompose(t, options, compose, "bind."+test.field)
		})
	}
	for _, test := range []struct {
		field string
		value string
	}{
		{field: "aliases", value: "[fixture]"},
		{field: "driver_opts", value: "{fixture: test}"},
		{field: "gw_priority", value: "1"},
		{field: "interface_name", value: "fixture0"},
		{field: "ipv4_address", value: "172.24.0.10"},
		{field: "ipv6_address", value: "fd00::10"},
		{field: "link_local_ips", value: "[169.254.0.10]"},
		{field: "mac_address", value: "02:42:ac:18:00:0a"},
		{field: "priority", value: "1"},
		{field: "use_service_name_alias", value: "false"},
	} {
		t.Run("endpoint."+test.field, func(t *testing.T) {
			options, _ := macFixture(t)
			compose := macRead(t, options.Root, "docker-compose.test.yml")
			endpoint := "  tests:\n    networks:\n      default:\n        " + test.field + ": " + test.value + "\n"
			compose = strings.Replace(compose, "  tests:\n", endpoint, 1)
			macRefuseNestedCompose(t, options, compose, test.field)
		})
	}
}

func macRefuseNestedCompose(t *testing.T, options testenv.MacTestOptions, compose, field string) {
	t.Helper()
	macWrite(t, options.Root, "docker-compose.test.yml", compose)
	result, err := testenv.RunMacTests(t.Context(), options)
	if err == nil || !strings.Contains(err.Error(), "unsupported tests") || !strings.Contains(err.Error(), field) || result.ExitCode != 1 {
		t.Fatalf("unsupported nested Compose %s: result=%+v error=%v", field, result, err)
	}
	if _, err := os.Lstat(options.EvidenceDirectory); !os.IsNotExist(err) {
		t.Fatalf("unsupported nested Compose created evidence: %v", err)
	}
}
