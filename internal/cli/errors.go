package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/inovector/mixpost-cli/internal/output"
)

// Exit codes. HTTP failures get a code of their own so a script can branch on
// the reason without parsing any output.
const (
	ExitOK           = 0
	ExitError        = 1
	ExitUsage        = 2
	ExitUnauthorized = 3 // 401
	ExitForbidden    = 4 // 403
	ExitNotFound     = 5 // 404
	ExitValidation   = 6 // 422
	ExitRateLimited  = 7 // 429
	ExitServer       = 8 // 5xx
	ExitNetwork      = 9 // connection failed, timed out, or was interrupted
	ExitAborted      = 10
)

// usageErr marks an error caused by how the command was invoked. command, when
// set, names the command whose help would explain the right invocation.
type usageErr struct {
	err     error
	command string
}

func (e *usageErr) Error() string { return e.err.Error() }
func (e *usageErr) Unwrap() error { return e.err }

func usageError(err error) error { return &usageErr{err: err} }

// cobraUsageMessages are the prefixes cobra and pflag use for a malformed
// invocation. They arrive as plain errors, so they are recognised by text.
var cobraUsageMessages = []string{
	"unknown command",
	"unknown flag",
	"unknown shorthand flag",
	"flag needs an argument",
	"invalid argument",
	"bad flag syntax",
	"accepts ",
	"requires at least",
}

// asUsageError promotes cobra's own invocation errors so they exit with the
// usage code like the ones this package raises.
func asUsageError(err error, command string) error {
	if err == nil {
		return nil
	}
	var usage *usageErr
	if errors.As(err, &usage) {
		if usage.command == "" {
			usage.command = command
		}
		return err
	}
	message := err.Error()
	for _, prefix := range cobraUsageMessages {
		if strings.HasPrefix(message, prefix) {
			return &usageErr{err: err, command: command}
		}
	}
	return err
}

var errAborted = errors.New("aborted")

// explainedErr carries a message that already says what to do, so the generic
// hint for its HTTP status is suppressed rather than contradicting it.
type explainedErr struct{ err error }

func (e *explainedErr) Error() string { return e.err.Error() }
func (e *explainedErr) Unwrap() error { return e.err }

func explained(err error) error { return &explainedErr{err: err} }

// ExitCode maps an error to the process exit status.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if errors.Is(err, errAborted) {
		return ExitAborted
	}

	var usage *usageErr
	if errors.As(err, &usage) {
		return ExitUsage
	}
	if errors.Is(err, mixpost.ErrNoWorkspace) || errors.Is(err, mixpost.ErrNoKey) ||
		errors.Is(err, mixpost.ErrWorkspaceNotFound) {
		return ExitUsage
	}

	var apiErr *mixpost.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Status {
		case 401:
			return ExitUnauthorized
		case 403:
			return ExitForbidden
		case 404:
			return ExitNotFound
		case 422:
			return ExitValidation
		case 429:
			return ExitRateLimited
		}
		if apiErr.Status >= 500 {
			return ExitServer
		}
		return ExitError
	}

	var transportErr *mixpost.TransportError
	if errors.As(err, &transportErr) {
		return ExitNetwork
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ExitNetwork
	}
	return ExitError
}

// renderError writes a failure to stderr, structured when the requested output
// format is structured so that an agent can read it the same way it reads data.
func renderError(printer *output.Printer, err error) {
	if errors.Is(err, errAborted) {
		fmt.Fprintln(printer.Err, "Aborted.")
		return
	}

	switch printer.Format {
	case output.FormatJSON, output.FormatYAML, output.FormatTemplate:
		renderStructuredError(printer, err)
	default:
		fmt.Fprintln(printer.Err, "Error: "+err.Error())
		hint(printer, err)
	}
}

func renderStructuredError(printer *output.Printer, err error) {
	payload := map[string]any{
		"error": map[string]any{
			"message":   err.Error(),
			"exit_code": ExitCode(err),
		},
	}
	detail := payload["error"].(map[string]any)

	var apiErr *mixpost.APIError
	if errors.As(err, &apiErr) {
		detail["message"] = apiErr.Message
		detail["status"] = apiErr.Status
		if len(apiErr.Errors) > 0 {
			detail["errors"] = apiErr.Errors
		}
		if apiErr.URL != "" {
			detail["url"] = apiErr.URL
			detail["method"] = apiErr.Method
		}
	}

	encoded, marshalErr := json.MarshalIndent(payload, "", "  ")
	if marshalErr != nil {
		fmt.Fprintln(printer.Err, "Error: "+err.Error())
		return
	}
	fmt.Fprintln(printer.Err, string(encoded))
}

func hint(printer *output.Printer, err error) {
	var usage *usageErr
	if errors.As(err, &usage) {
		if usage.command != "" {
			fmt.Fprintf(printer.Err, "Run '%s --help' for usage.\n", usage.command)
		}
		return
	}

	var explanation *explainedErr
	if errors.As(err, &explanation) {
		return
	}

	var apiErr *mixpost.APIError
	if !errors.As(err, &apiErr) {
		return
	}
	switch apiErr.Status {
	case 401:
		fmt.Fprintln(printer.Err, "Hint: the personal access key is missing, expired or deleted. Run 'mixpost auth login' to use a new one.")
	case 403:
		fmt.Fprintln(printer.Err, "Hint: your role does not allow that action in this workspace. Check 'mixpost workspace list'.")
	case 404:
		fmt.Fprintln(printer.Err, "Hint: check the workspace UUID and the instance URL, including the Mixpost core path.")
	case 429:
		fmt.Fprintln(printer.Err, "Hint: the instance is rate limiting. Retry later or raise --retries.")
	}
}

// isBrokenPipe keeps `mixpost ... | head` from reporting a failure when the
// reader goes away mid-write.
func isBrokenPipe(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, os.ErrClosed)
}
