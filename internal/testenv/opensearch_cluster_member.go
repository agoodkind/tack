package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const (
	// openSearchService is the stack file service that pins the member image.
	openSearchService = "opensearch"
	// openSearchConfigDir is the image's configuration directory.
	openSearchConfigDir = "/usr/share/opensearch/config"
	// openSearchFileLimit is the open file limit the stack file sets.
	openSearchFileLimit = 65536
)

// clusterMemberConfig enables REST and transport TLS with certificates that
// the cluster CA signs, and it restricts the model to ML members, as each
// environment does. The member reads its roles, heap, and discovery settings
// from environment variables, as the stack file's opensearch service sets
// them. A member outside single-node discovery enforces the
// bootstrap checks. The memory-map count check reads the host's
// vm.max_map_count, which the test does not control, and the check does not
// run while node.store.allow_mmap is false. action.auto_create_index refuses
// automatic creation of node-pages-* indexes, as each environment does.
const clusterMemberConfig = `network.host: 0.0.0.0
action.auto_create_index: "-node-pages-*,+*"
node.store.allow_mmap: false
plugins.security.ssl.http.enabled: true
plugins.security.ssl.http.pemcert_filepath: certs/node.pem
plugins.security.ssl.http.pemkey_filepath: certs/node-key.pem
plugins.security.ssl.http.pemtrustedcas_filepath: certs/ca.pem
plugins.security.ssl.transport.pemcert_filepath: certs/node.pem
plugins.security.ssl.transport.pemkey_filepath: certs/node-key.pem
plugins.security.ssl.transport.pemtrustedcas_filepath: certs/ca.pem
plugins.security.ssl.transport.enforce_hostname_verification: false
plugins.security.nodes_dn:
  - "CN=` + clusterNodeCommonName + `"
plugins.security.allow_default_init_securityindex: true
plugins.ml_commons.only_run_on_ml_node: true
plugins.ml_commons.model_auto_redeploy.enable: true
`

// clusterContainer is one container the cluster creates under a name it
// chose in advance. The certificates include that name.
type clusterContainer struct {
	name  string
	image string
	cmd   []string
	env   []string
	files map[string][]byte
	// owner owns the written files and their directories.
	owner     fileOwner
	resources container.Resources
}

// startClusterContainer pulls the image when absent, creates the container
// on the engines' network, records it for Release, writes its files, and
// starts it.
func startClusterContainer(ctx context.Context, cli *client.Client, spec clusterContainer) error {
	if err := ensureImage(ctx, cli, engineSpec{kind: spec.name, image: spec.image, platform: nil, cmd: nil, env: nil}); err != nil {
		return err
	}
	_, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: spec.image, Cmd: spec.cmd, Env: spec.env, Labels: map[string]string{managedLabel: "true"}},
		HostConfig: &container.HostConfig{Resources: spec.resources},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{
			networkName: {Aliases: []string{spec.name}},
		}},
		Name: spec.name,
	})
	if err != nil {
		slog.ErrorContext(ctx, "testenv.cluster.create_failed", slog.String("err", err.Error()), slog.String("container", spec.name))
		return fmt.Errorf("create container %s from %s: %w", spec.name, spec.image, err)
	}
	own(spec.name)
	for path, contents := range spec.files {
		if err := writeContainerFileAs(ctx, cli, spec.name, path, contents, spec.owner); err != nil {
			return err
		}
	}
	if _, err := cli.ContainerStart(ctx, spec.name, client.ContainerStartOptions{}); err != nil {
		slog.ErrorContext(ctx, "testenv.cluster.start_failed", slog.String("err", err.Error()), slog.String("container", spec.name))
		return fmt.Errorf("start container %s: %w", spec.name, err)
	}
	return nil
}

// startMember starts one planned member with the stack file's image, heap,
// roles, and file limit. Every member's seed list includes every planned
// member, and a restarted member probes each of them. Only the first planned
// member sets cluster.initial_cluster_manager_nodes; a later member
// discovers the running cluster.
func (c *OpenSearchCluster) startMember(ctx context.Context, cli *client.Client, member string) error {
	image, err := serviceImage(ctx, openSearchService)
	if err != nil {
		return err
	}
	certificate, err := c.authority.issue(ctx, clusterNodeCommonName, []string{member, c.name})
	if err != nil {
		return err
	}
	env := []string{
		"OPENSEARCH_JAVA_OPTS=-Xms2g -Xmx2g", "DISABLE_INSTALL_DEMO_CONFIG=true",
		"node.roles=cluster_manager,data,ingest,ml", "cluster.name=" + c.name, "node.name=" + member,
		"discovery.seed_hosts=" + strings.Join(c.planned, ","),
	}
	if member == c.planned[0] {
		env = append(env, "cluster.initial_cluster_manager_nodes="+member)
	}
	files := map[string][]byte{openSearchConfigDir + "/opensearch.yml": []byte(clusterMemberConfig)}
	files[openSearchConfigDir+"/opensearch-security/internal_users.yml"] = c.users
	files[openSearchConfigDir+"/certs/ca.pem"] = c.authority.pem
	files[openSearchConfigDir+"/certs/node.pem"] = certificate.certificate
	files[openSearchConfigDir+"/certs/node-key.pem"] = certificate.key
	limit := &container.Ulimit{Name: "nofile", Soft: openSearchFileLimit, Hard: openSearchFileLimit}
	resources := container.Resources{Memory: openSearchMemoryBytes, Ulimits: []*container.Ulimit{limit}}
	return startClusterContainer(ctx, cli, clusterContainer{name: member, image: image, cmd: nil, env: env, files: files, owner: openSearchOwner, resources: resources})
}

// memberFixture returns the cluster credentials with member's own endpoint,
// for a health request that bypasses the proxy.
func (c *OpenSearchCluster) memberFixture(member string) OpenSearchFixture {
	pass := c.Fixture.Password
	return OpenSearchFixture{Endpoint: c.MemberEndpoint(member), CA: c.Fixture.CA, Username: c.Fixture.Username, Password: pass, Container: member}
}
