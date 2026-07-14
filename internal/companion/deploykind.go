package companion

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// DeployKind identifies how a component is currently running on a host,
// for self-update purposes.
type DeployKind int

const (
	DeployNone DeployKind = iota
	DeployNative
	DeployDocker
)

// systemdUnitDir is where install.sh always writes a native unit -- a
// var, not a const, purely so tests can point it at a temp dir instead
// of the real /etc/systemd/system.
var systemdUnitDir = "/etc/systemd/system"

// Detection is the result of checking whether name is running natively,
// as a Docker container, both (an ambiguous state -- mirrors
// install_companion's own existing "both a native update-detector.service
// and a containerized one" warning), or neither on this host.
type Detection struct {
	Native            bool
	DockerContainerID string // "" if no Docker container found
}

// Kind resolves Detection to a single answer, preferring native when
// both are present -- the same precedence install_companion's own
// discovery already uses (a native install this same host might have
// just performed takes priority over a pre-existing container).
// Ambiguous cases should still be surfaced to the operator (see
// Detection.Ambiguous), not silently resolved and forgotten.
func (d Detection) Kind() DeployKind {
	switch {
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
// is running natively (a systemd unit file exists, regardless of
// enabled/active state) and/or as a Docker container (running or
// stopped) on this host. Both checks always run, even if the first
// already answers the question, so an ambiguous state is never hidden
// from the caller.
func Detect(ctx context.Context, name string) (Detection, error) {
	native := nativeUnitPresent(name)
	dockerID, err := dockerContainerFor(ctx, name)
	if err != nil {
		return Detection{}, err
	}
	return Detection{Native: native, DockerContainerID: dockerID}, nil
}

// nativeUnitPresent mirrors install.sh's own native_unit_present.
func nativeUnitPresent(name string) bool {
	_, err := os.Stat(fmt.Sprintf("%s/%s.service", systemdUnitDir, name))
	return err == nil
}

// dockerContainerFor mirrors install.sh's own docker_container_for:
// finds the first container (running or stopped, via "docker ps -a")
// whose image matches name, anchored the same way install.sh's own awk
// pattern is (so "update-detector" can't match an
// "update-detector-companion" image either direction). Returns "" with
// no error if docker isn't on PATH at all, or if nothing matches.
func dockerContainerFor(ctx context.Context, name string) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", nil
	}

	cmd := exec.CommandContext(ctx, "docker", "ps", "-a", "--format", "{{.ID}} {{.Image}}")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("deploykind: docker ps: %w", err)
	}

	pattern := regexp.MustCompile(`(^|/)` + regexp.QuoteMeta(name) + `(:|$)`)
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if id, image := fields[0], fields[1]; pattern.MatchString(image) {
			return id, nil
		}
	}
	return "", nil
}
