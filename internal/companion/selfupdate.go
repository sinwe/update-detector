package companion

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"update-detector/internal/aggregator"
)

// componentUnitName maps a self-update Action's Component to the
// systemd unit / Windows Service / Docker image name that component
// actually runs under (install.sh's own INSTALL_COMPONENTS values --
// "agent", "aggregator", "companion" -- are not the same strings as the
// unit/service/image names).
func componentUnitName(component string) (string, error) {
	switch component {
	case "agent":
		return "update-detector", nil
	case "aggregator":
		return "update-aggregator", nil
	case "companion":
		return "update-detector-companion", nil
	default:
		return "", fmt.Errorf("selfupdate: unknown component %q", component)
	}
}

// SelfUpdate carries out action (Type == ActionSelfUpdate): detects
// whether action.Component is running natively (systemd unit on Linux,
// Windows Service on Windows) or as a Docker container on this host,
// and updates it to action.TargetVersion accordingly.
//
// Never restarts *this* process as part of this call, even when
// Component == "companion" -- the restart for that case is bundled into
// the install script's own install step (already true for the other two
// components too, which is fine there since restarting them doesn't kill
// the process running this code). The caller
// (cmd/update-detector-companion/main.go) must report this action's
// result *before* calling SelfUpdate for Component == "companion"
// specifically, since code after the install script restarts this very
// process may never run to report it.
func SelfUpdate(ctx context.Context, action aggregator.Action) aggregator.ActionResult {
	fail := func(format string, args ...any) aggregator.ActionResult {
		return aggregator.ActionResult{ActionID: action.ID, Message: fmt.Sprintf(format, args...), CompletedAt: time.Now()}
	}
	succeed := func(msg string) aggregator.ActionResult {
		return aggregator.ActionResult{ActionID: action.ID, Success: true, Message: msg, CompletedAt: time.Now()}
	}

	// Companion is always native, never containerized (it needs real
	// administrator/root privileges to apply updates) --
	// install_companion's own uninstall code already documents this same
	// fact, so there's no Docker case to even check here.
	if action.Component == "companion" {
		return companionSelfUpdate(ctx, action)
	}

	unitName, err := componentUnitName(action.Component)
	if err != nil {
		return fail("%v", err)
	}

	detection := Detect(ctx, unitName)

	switch detection.Kind() {
	case DeployNative, DeployWindowsService:
		// Both native deploy kinds go through installNative -- the
		// platform-specific file (selfupdate_unix.go / selfupdate_windows.go)
		// provides the right implementation for this build.
		if err := installNative(ctx, action.Component, action.TargetVersion); err != nil {
			return fail("%v", err)
		}
	case DeployDocker:
		if err := updateDockerCompose(ctx, detection.DockerContainerID, detection.DockerImage, action.TargetVersion); err != nil {
			return fail("%v", err)
		}
	default:
		return fail("%s is not running natively or as a Docker container on this host", action.Component)
	}

	msg := fmt.Sprintf("%s updated to %s", action.Component, action.TargetVersion)
	if detection.Ambiguous() {
		msg += " (both a native install and a Docker container were found -- updated the native one; remove the Docker one to avoid confusion)"
	}
	return succeed(msg)
}

// updateDockerCompose updates a Docker-based component to targetVersion,
// using the exact compose file(s)/working dir/service name Docker Compose
// itself recorded as labels on containerID when it created it -- so this
// respects the user's own compose file, env, and volumes exactly, without
// install.sh or the companion needing to track or duplicate that path
// anywhere.
//
// Deliberately NOT a plain `docker compose pull && up -d`: this repo's
// own compose files pin `image: .../update-detector:latest`, and that
// tag gets moved on every real-release push (see
// .github/workflows/release.yml) -- a plain pull fetches whatever
// :latest currently is on the registry, which is not necessarily
// targetVersion at all. Confirmed live (before :latest was excluded from
// pre-release pushes): requesting a downgrade to an older real release
// instead silently pulled a newer -rc build that had been pushed moments
// earlier for an unrelated reason. Instead, pull the
// *specific* targetVersion tag by name, then locally retag it as
// whatever tag the container's own image reference already uses (so the
// compose file's own `image:` line, unedited, resolves to the right
// content) -- then `up -d` alone, deliberately never `pull` again here,
// since that would immediately undo the retag by re-fetching the
// registry's current tag.
func updateDockerCompose(ctx context.Context, containerID, image, targetVersion string) error {
	configFiles, err := dockerInspectLabel(ctx, containerID, "com.docker.compose.project.config_files")
	if err != nil {
		return err
	}
	workingDir, err := dockerInspectLabel(ctx, containerID, "com.docker.compose.project.working_dir")
	if err != nil {
		return err
	}
	service, err := dockerInspectLabel(ctx, containerID, "com.docker.compose.service")
	if err != nil {
		return err
	}
	if configFiles == "" || workingDir == "" || service == "" {
		return fmt.Errorf("selfupdate: container %s is missing expected Docker Compose labels -- was it started with `docker compose up`?", containerID)
	}

	// Splitting on the *last* colon, not the first, since a registry
	// host can itself contain a colon for a non-default port (e.g.
	// "registry.example.com:5000/name:tag") -- this repo's own images
	// never do that (ghcr.io has no port in the hostname), so
	// this is a deliberate simplification, not general image-reference
	// parsing, matching how internal/version.Compare is also scoped to
	// this repo's own tag convention rather than general semver.
	sep := strings.LastIndex(image, ":")
	if sep < 0 {
		return fmt.Errorf("selfupdate: container %s's image %q has no tag to replace", containerID, image)
	}
	repo, currentTag := image[:sep], image[sep+1:]

	pullCmd := exec.CommandContext(ctx, "docker", "pull", repo+":"+targetVersion)
	if out, err := runCapped(ctx, pullCmd); err != nil {
		return fmt.Errorf("selfupdate: docker pull %s:%s: %w\n%s", repo, targetVersion, err, out)
	}
	tagCmd := exec.CommandContext(ctx, "docker", "tag", repo+":"+targetVersion, repo+":"+currentTag)
	if out, err := runCapped(ctx, tagCmd); err != nil {
		return fmt.Errorf("selfupdate: docker tag %s:%s %s:%s: %w\n%s", repo, targetVersion, repo, currentTag, err, out)
	}

	// config_files can be a comma-separated list if the original compose
	// invocation used more than one -f flag -- pass each as its own -f,
	// not a single comma-joined value (docker compose's -f takes one
	// path per flag, repeatable).
	var fileArgs []string
	for _, f := range strings.Split(configFiles, ",") {
		fileArgs = append(fileArgs, "-f", f)
	}
	args := append(append([]string{"compose"}, fileArgs...), "up", "-d", service)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = workingDir
	if out, err := runCapped(ctx, cmd); err != nil {
		return fmt.Errorf("selfupdate: docker %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

func dockerInspectLabel(ctx context.Context, containerID, label string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--format",
		fmt.Sprintf(`{{index .Config.Labels "%s"}}`, label), containerID)
	out, err := runCapped(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("selfupdate: docker inspect %s: %w\n%s", containerID, err, out)
	}
	return strings.TrimSpace(out), nil
}

// passThrough returns "KEY=value" env entries for each of keys present
// (and non-empty) in values, unchanged. Shared by both platforms'
// existingConfigEnv: selfupdate_unix.go reads values from install.sh's
// own env file, selfupdate_windows.go from install.bat's own Windows
// Service registry Environment value -- the translation logic itself
// doesn't care which.
func passThrough(values map[string]string, keys ...string) []string {
	var env []string
	for _, k := range keys {
		if v := values[k]; v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// translate returns "newName=value" env entries, renaming each key in
// rename (oldName -> newName) that's present and non-empty in values.
func translate(values map[string]string, rename map[string]string) []string {
	var env []string
	for oldName, newName := range rename {
		if v := values[oldName]; v != "" {
			env = append(env, newName+"="+v)
		}
	}
	return env
}
