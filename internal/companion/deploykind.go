package companion

import (
	"bytes"
	"context"
	"fmt"
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
	// "forgejo.winar.to/winarto/update-detector:latest") the container
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
// an ambiguous state is never hidden from the caller.
func Detect(ctx context.Context, name string) (Detection, error) {
	native := nativeUnitPresent(name)
	dockerID, dockerImage, err := dockerContainerFor(ctx, name)
	if err != nil {
		return Detection{}, err
	}
	return Detection{Native: native, DockerContainerID: dockerID, DockerImage: dockerImage}, nil
}

// AggregatorColocated reports whether the aggregator itself is running
// (natively or as a Docker container) on this same host -- used to tell
// the companion whether to skip or include the aggregator component in
// a self-update sweep. Best-effort: errors are silently treated as
// "not present" rather than propagated, since this is informational only
// and failing to self-update an absent aggregator is harmless.
func AggregatorColocated(ctx context.Context) bool {
	detection, err := Detect(ctx, "update-aggregator")
	if err != nil {
		return false
	}
	return detection.Kind() != DeployNone
}

// dockerContainerFor mirrors install.sh's own docker_container_for:
// finds the first container (running or stopped, via "docker ps -a")
// whose image matches name, anchored the same way install.sh's own awk
// pattern is (so "update-detector" can't match an
// "update-detector-companion" image either direction), returning both its
// ID and its exact image reference. Returns "", "", nil if docker isn't
// on PATH at all, or if nothing matches.
//
// Deliberately not "docker ps --format {{.Image}}": that field silently
// falls back to printing a bare image ID once the tag a container was
// created from has since been reassigned to a different image (e.g. a
// later `docker pull` of the same :latest this repo's own compose files
// pin, which release.yml moves on every tag push including -rcN builds)
// -- confirmed live against a real long-running container whose `docker
// ps` showed a raw hex ID while `docker inspect .Config.Image` still
// correctly reported "forgejo.winar.to/winarto/update-detector:latest".
// docker inspect's Config.Image is the reference the container was
// actually created with and never silently changes, so it's used
// instead -- one extra call, but a single "docker inspect id1 id2 ..."
// across every candidate rather than one call per container.
func dockerContainerFor(ctx context.Context, name string) (id, image string, err error) {
	if _, lookErr := exec.LookPath("docker"); lookErr != nil {
		return "", "", nil
	}

	psCmd := exec.CommandContext(ctx, "docker", "ps", "-a", "--format", "{{.ID}}")
	var psOut bytes.Buffer
	psCmd.Stdout = &psOut
	if runErr := psCmd.Run(); runErr != nil {
		return "", "", fmt.Errorf("deploykind: docker ps: %w", runErr)
	}
	ids := strings.Fields(psOut.String())
	if len(ids) == 0 {
		return "", "", nil
	}

	inspectCmd := exec.CommandContext(ctx, "docker", append([]string{"inspect", "--format", "{{.Config.Image}}"}, ids...)...)
	var inspectOut bytes.Buffer
	inspectCmd.Stdout = &inspectOut
	if runErr := inspectCmd.Run(); runErr != nil {
		return "", "", fmt.Errorf("deploykind: docker inspect: %w", runErr)
	}
	images := strings.Split(strings.TrimRight(inspectOut.String(), "\n"), "\n")

	pattern := regexp.MustCompile(`(^|/)` + regexp.QuoteMeta(name) + `(:|$)`)
	for i, containerImage := range images {
		if i >= len(ids) {
			break
		}
		if containerImage != "" && pattern.MatchString(containerImage) {
			return ids[i], containerImage, nil
		}
	}
	return "", "", nil
}
