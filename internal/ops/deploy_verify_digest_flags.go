package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
)

// indexDigestPattern accepts SHA-256 index digests with lowercase hexadecimal digits.
var indexDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// deployIndexDigests maps each service to the index digest its replicas must run.
// A nil map selects the local tag comparison.
type deployIndexDigests map[string]string

// parseDeployIndexDigests validates digest flags before the command reads the daemon.
// Empty flags select local tag comparison. Verification that includes audit-consumer
// requires both flags. App-only verification rejects the consumer digest flag.
func parseDeployIndexDigests(
	ctx context.Context,
	server string,
	consumer string,
	services []string,
) (deployIndexDigests, error) {
	if server == "" && consumer == "" {
		return nil, nil
	}

	appOnly := !slices.Contains(services, auditConsumerService)
	var problems []string
	switch {
	case appOnly && consumer != "":
		problems = append(
			problems,
			"--service app checks only the app service and refuses --tack-audit-consumer-digest",
		)
	case !appOnly && (server == "" || consumer == ""):
		problems = append(
			problems,
			"--tack-server-digest and --tack-audit-consumer-digest must be given together",
		)
	}

	for flag, value := range map[string]string{
		"--tack-server-digest":         server,
		"--tack-audit-consumer-digest": consumer,
	} {
		if value != "" && !indexDigestPattern.MatchString(value) {
			problems = append(
				problems,
				fmt.Sprintf(
					"%s %q is not sha256: followed by 64 lowercase hex digits",
					flag,
					value,
				),
			)
		}
	}

	if len(problems) > 0 {
		err := errors.New("ops deploy verify: " + strings.Join(problems, "; "))
		slog.ErrorContext(
			ctx,
			"ops.deploy.verify.digest_flags_invalid",
			slog.String("err", err.Error()),
		)

		return nil, err
	}

	if appOnly {
		return deployIndexDigests{appService: server}, nil
	}

	return deployIndexDigests{appService: server, auditConsumerService: consumer}, nil
}
