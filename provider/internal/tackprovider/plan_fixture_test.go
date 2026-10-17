package tackprovider_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// statusDocumentPath specifies the path to the migrate --output json document
// for a database at migration 19 with the migrations embedded in Tack commit
// 986faf12.
const statusDocumentPath = "testdata/migrate-status.json"

type statusMigration struct {
	Version int64  `json:"version"`
	Name    string `json:"name"`
	SHA256  string `json:"sha256"`
}

type migrationStatus struct {
	MigrationTablePresent bool              `json:"migration_table_present"`
	AppliedVersion        int64             `json:"applied_version"`
	Available             []statusMigration `json:"available"`
	ImageCommit           string            `json:"image_commit"`
}

type statusEnvelope struct {
	Result migrationStatus `json:"result"`
}

type migrationValue struct {
	Version types.Int64  `tfsdk:"version"`
	Name    types.String `tfsdk:"name"`
	SHA256  types.String `tfsdk:"sha256"`
}

var migrationObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"version": types.Int64Type,
	"name":    types.StringType,
	"sha256":  types.StringType,
}}

// statusDocument reads the document at statusDocumentPath and decodes the
// result member. It checks that the document records a migration table and
// lists available migrations, then sets the applied version to applied.
func statusDocument(t *testing.T, applied int64) migrationStatus {
	t.Helper()
	body, err := os.ReadFile(statusDocumentPath)
	if err != nil {
		t.Fatalf("read the status document %s: %v", statusDocumentPath, err)
	}
	var document statusEnvelope
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the status document %s: %v", statusDocumentPath, err)
	}
	status := document.Result
	if !status.MigrationTablePresent || len(status.Available) == 0 {
		t.Fatalf("document at %s does not record a migration table or does not list available migrations", statusDocumentPath)
	}
	status.AppliedVersion = applied
	return status
}

func (status migrationStatus) highest() int64 {
	var highest int64
	for _, available := range status.Available {
		highest = max(highest, available.Version)
	}
	return highest
}

func migrationList(t *testing.T, migrations []statusMigration) types.List {
	t.Helper()
	values := make([]migrationValue, 0, len(migrations))
	for _, item := range migrations {
		values = append(values, migrationValue{
			Version: types.Int64Value(item.Version),
			Name:    types.StringValue(item.Name),
			SHA256:  types.StringValue(item.SHA256),
		})
	}
	list, diags := types.ListValueFrom(t.Context(), migrationObjectType, values)
	if diags.HasError() {
		t.Fatalf("build the migration list: %s", diagnosticsText(diags))
	}
	return list
}
