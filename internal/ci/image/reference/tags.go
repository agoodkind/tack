package reference

import (
	"os"
	"strings"

	"goodkind.io/tack/internal/ci/image/environment"
)

// tagInputs stores the workflow values used to form image references.
type tagInputs struct {
	registry, owner, image, shortCommit, commit, refType string
}

// WriteTags writes one image reference per line to IMAGE_TAG_FILE. Every build
// includes the short and full commit tags. A tag build also includes its
// reference name and latest. A branch build also includes its branch name;
// among branch builds, only main includes latest.
func WriteTags() error {
	var input tagInputs
	for _, item := range []struct {
		key   string
		value *string
	}{
		{"REGISTRY", &input.registry},
		{"IMAGE_OWNER", &input.owner},
		{"IMAGE_NAME", &input.image},
		{"COMMIT_SHORT", &input.shortCommit},
		{"GITHUB_SHA", &input.commit},
		{"GITHUB_REF_TYPE", &input.refType},
	} {
		value, err := environment.Required(item.key)
		if err != nil {
			return &os.PathError{Op: "read image tag input", Path: item.key, Err: err}
		}
		*item.value = value
	}
	base := input.registry + "/" + input.owner + "/" + input.image
	references := []string{base + ":" + input.shortCommit, base + ":" + input.commit}
	if input.refType == "tag" {
		name, err := environment.Required("GITHUB_REF_NAME")
		if err != nil {
			return &os.PathError{Op: "read image tag ref name", Path: "GITHUB_REF_NAME", Err: err}
		}
		references = append(references, base+":"+strings.ReplaceAll(name, "/", "-"), base+":latest")
	} else {
		name, err := environment.Required("BRANCH_NAME")
		if err != nil {
			return &os.PathError{Op: "read image tag branch name", Path: "BRANCH_NAME", Err: err}
		}
		references = append(references, base+":"+strings.ReplaceAll(name, "/", "-"))
		if name == "main" {
			references = append(references, base+":latest")
		}
	}
	path, err := environment.Required("IMAGE_TAG_FILE")
	if err != nil {
		return &os.PathError{Op: "read image tag file", Path: "IMAGE_TAG_FILE", Err: err}
	}
	return writeFile(path, strings.Join(references, "\n")+"\n")
}
