package companion

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
)

// DeployKind identifies how a component is currently running on a host,
// for self-update purposes.
type DeployKind int

const (
	DeployNone DeployKind = iota
	DeployNative          // systemd unit (Linux)
	DeployDocker
	DeployWindowsService // Windows Service (via sc.exe / service manager)
)

// Detection is the result of checking whether name is running natively,
// as a Docker container, both (an ambiguous state -- mirrors
// install_companion's own existing "both a native update-detector.service
// and a containerized one" warning), or neither on this host.
type Detection struct {
	Native            bool
	DockerContainerID string // "" if no Docker container found
	// DockerImage is the exact image reference (e.g.
	// "ghcr.io/sinwe/update-detector:latest") the container
	// is currently running -- needed to pull and pin a *specific*
	// version later (see updateDockerCompose), since a plain
	// `docker compose pull` would just re-fetch whatever this same tag
	// currently points to on the registry, not necessarily the version
	// actually requested.
	DockerImage string
}

// Kind resolves Detection to a single answer, preferring native when
// both are present -- the same precedence install_companion's own
// discovery already uses (a native install this same host might have
// just performed takes priority over a pre-existing container).
// Ambiguous cases should still be surfaced to the operator (see
// Detection.Ambiguous), not silently resolved and forgotten.
func (d Detection) Kind() DeployKind {
	switch {
	case d.Native && runtime.GOOS == "windows":
		return DeployWindowsService
	case d.Native:
		return DeployNative
	case d.DockerContainerID != "":
		return DeployDocker
	default:
		return DeployNone
	}
}

// Ambiguous reports whether both a native unit and a Docker container
// were found for the same component -- a real, if unusual, state worth
// warning about rather than silently picking one.
func (d Detection) Ambiguous() bool {
	return d.Native && d.DockerContainerID != ""
}

// Detect checks whether name (e.g. "update-detector", "update-aggregator")
// is running natively (a systemd unit file on Linux, a Windows Service on
// Windows) and/or as a Docker container (running or stopped) on this host.
// Both checks always run, even if the first already answers the question, so
// an ambiguous state is never hidden from the caller. No error return: both
// dockerContainerFor and nativeUnitPresent already degrade gracefully to
// "not found" on any failure of their own (see dockerContainerFor's own
// doc comment) rather than ever failing this call outright.
func Detect(ctx context.Context, name string) Detection {
	native := nativeUnitPresent(name)
	dockerID, dockerImage := dockerContainerFor(ctx, name)
	return Detection{Native: native, DockerContainerID: dockerID, DockerImage: dockerImage}
}

// AggregatorColocated reports whether the aggregator itself is running
// (natively or as a Docker container) on this same host -- used to tell
// the companion whether to skip or include the aggregator component in
// a self-update sweep.
func AggregatorColocated(ctx context.Context) bool {
	return Detect(ctx, "update-aggregator").Kind() != DeployNone
}

// dockerContainerFor mirrors install.sh's own docker_container_for:
// finds the first container (running or stopped, via "docker ps -a")
// whose image matches name, anchored the same way install.sh's own awk
// pattern is (so "update-detector" can't match an
// "update-detector-companion" image either direction), returning both its
// ID and its exact image reference. Returns "", "" if docker isn't on
// PATH at all, or if running it fails for any reason (not just absence,
// and not returned as an error at all -- see below) -- confirmed live: a
// Windows host with Docker Desktop installed (for unrelated reasons) but
// not actually running produced "docker ps: exit status 1" here, which
// used to propagate all the way up as SelfUpdate's own hard failure
// ("detecting how agent is deployed: ...") even though the agent was
// plainly installed natively and nativeUnitPresent (checked alongside
// this, see Detect) would have given a perfectly good, sufficient answer
// on its own. Docker being unreliable or misconfigured must never block
// a native-only host's own self-update from working at all -- same
// "graceful degradation, not a hard failure" posture every other
// exec-based check in this codebase already takes (winget, apt-get,
// ...), which is also why this has no error return at all: every
// failure mode here already degrades to "no container found", so an
// error return would only ever be dead code a caller could never
// actually receive.
//
// Deliberately not "docker ps --format {{.Image}}": that field silently
// falls back to printing a bare image ID once the tag a container was
// created from has since been reassigned to a different image (e.g. a
// later `docker pull` of the same :latest this repo's own compose files
// pin, which release.yml moves on every tag push including -rcN builds)
// -- confirmed live against a real long-running container whose `docker
// ps` showed a raw hex ID while `docker inspect .Config.Image` still
// correctly reported "ghcr.io/sinwe/update-detector:latest".
// docker inspect's Config.Image is the reference the container was
// actually created with and never silently changes, so it's used
// instead -- one extra call, but a single "docker inspect id1 id2 ..."
// across every candidate rather than one call per container.
func dockerContainerFor(ctx context.Context, name string) (id, image string) {
	if _, lookErr := exec.LookPath("docker"); lookErr != nil {
		return "", ""
	}

	psCmd := exec.CommandContext(ctx, "docker", "ps", "-a", "--format", "{{.ID}}")
	var psOut bytes.Buffer
	psCmd.Stdout = &psOut
	if runErr := psCmd.Run(); runErr != nil {
		return "", ""
	}
	ids := strings.Fields(psOut.String())
	if len(ids) == 0 {
		return "", ""
	}

	inspectCmd := exec.CommandContext(ctx, "docker", append([]string{"inspect", "--format", "{{.Config.Image}}"}, ids...)...)
	var inspectOut bytes.Buffer
	inspectCmd.Stdout = &inspectOut
	if runErr := inspectCmd.Run(); runErr != nil {
		return "", ""
	}
	images := strings.Split(strings.TrimRight(inspectOut.String(), "\n"), "\n")

	pattern := regexp.MustCompile(`(^|/)` + regexp.QuoteMeta(name) + `(:|$)`)
	for i, containerImage := range images {
		if i >= len(ids) {
			break
		}
		if containerImage != "" && pattern.MatchString(containerImage) {
			return ids[i], containerImage
		}
	}
	return "", ""
}

// AgentColocated reports whether agent is running (natively or as a
// Docker container) on this same host -- used to tell aggregator about
// the host's deployment shape.
func AgentColocated(ctx context.Context) bool {
	return Detect(ctx, "update-detector").Kind() != DeployNone
}
