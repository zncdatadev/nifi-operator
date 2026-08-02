package util

import (
	"fmt"

	"github.com/zncdatadev/operator-go/pkg/constant"
)

// TODO(operator-go): vendored from operator-go v0.12.6 pkg/util/bash.go —
// removed upstream (#441) with no replacement, while the Vector sidecar still
// enforces the shutdown-file contract these commands implement. The rendered
// main-container args are part of the Gen 2 parity contract, so the byte
// content must not change. Restore tracked in docs/gen3-migration-design.md §7.

const (
	// VectorLogDir is the subdirectory of the log directory containing files to
	// control the Vector instance.
	VectorLogDir = "_vector/"

	// ShutdownFile signals that Vector should be gracefully shut down.
	ShutdownFile = "shutdown"
)

const CommonBashTrapFunctions = `prepare_signal_handlers()
{
    unset term_child_pid
    unset term_kill_needed
    trap 'handle_term_signal' TERM
}

handle_term_signal()
{
    if [ "${term_child_pid}" ]; then
        kill -TERM "${term_child_pid}" 2>/dev/null
    else
        term_kill_needed="yes"
    fi
}

wait_for_termination()
{
    set +e
    term_child_pid=$1
    if [[ -v term_kill_needed ]]; then
        kill -TERM "${term_child_pid}" 2>/dev/null
    fi
    wait ${term_child_pid} 2>/dev/null
    trap - TERM
    wait ${term_child_pid} 2>/dev/null
    set -e
}`

// RemoveVectorShutdownFileCommand removes the shutdown file (if it exists)
// created by CreateVectorShutdownFileCommand. Execute before starting the
// application.
func RemoveVectorShutdownFileCommand() string {
	return fmt.Sprintf("rm -f %s%s%s", constant.KubedoopLogDir, VectorLogDir, ShutdownFile)
}

// CreateVectorShutdownFileCommand creates the shutdown file for the vector
// container. Execute after the application terminates.
func CreateVectorShutdownFileCommand() string {
	return fmt.Sprintf("mkdir -p %s%s && touch %s%s%s", constant.KubedoopLogDir, VectorLogDir, constant.KubedoopLogDir, VectorLogDir, ShutdownFile)
}
