package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/buildkite/agent/bootstrap/shell"
	"github.com/pkg/errors"
)

var dockerEnv = []string{
	`BUILDKITE_DOCKER_COMPOSE_CONTAINER`,
	`BUILDKITE_DOCKER_COMPOSE_FILE`,
	`BUILDKITE_DOCKER`,
	`BUILDKITE_DOCKER_FILE`,
	`BUILDKITE_DOCKER_COMPOSE_BUILD_ALL`,
	`BUILDKITE_DOCKER_COMPOSE_LEAVE_VOLUMES`,
}

func hasDeprecatedDockerIntegration(sh *shell.Shell) bool {
	for _, k := range dockerEnv {
		if sh.Env.Exists(k) {
			return true
		}
	}
	return false
}

func runDeprecatedDockerIntegration(sh *shell.Shell, scriptPath string) error {
	var warnNotSet = func(k1, k2 string) {
		sh.Warningf("%s is set, but without %s, which it requires. You should be able to safely remove this from your pipeline.", k1, k2)
	}

	// scriptPath needs to be relative to wd
	relativePath, err := filepath.Rel(sh.Getwd(), scriptPath)
	if err != nil {
		return err
	}

	// this gives us ./scriptPath, which is needed for executing from wd
	relativePathToDot := "." + string(os.PathSeparator) + relativePath

	switch {
	case sh.Env.Exists(`BUILDKITE_DOCKER_COMPOSE_CONTAINER`):
		sh.Warningf("BUILDKITE_DOCKER_COMPOSE_CONTAINER is set, which is deprecated in Agent v3 and will be removed in v4. Consider using the :docker: docker-compose plugin instead at https://github.com/buildkite-plugins/docker-compose-buildkite-plugin.")
		return runDockerComposeCommand(sh, relativePathToDot)

	case sh.Env.Exists(`BUILDKITE_DOCKER`):
		sh.Warningf("BUILDKITE_DOCKER is set, which is deprecated in Agent v3 and will be removed in v4. Consider using the docker plugin instead at https://github.com/buildkite-plugins/docker-buildkite-plugin.")
		return runDockerCommand(sh, relativePathToDot)

	case sh.Env.Exists(`BUILDKITE_DOCKER_COMPOSE_FILE`):
		warnNotSet(`BUILDKITE_DOCKER_COMPOSE_FILE`, `BUILDKITE_DOCKER_COMPOSE_CONTAINER`)

	case sh.Env.Exists(`BUILDKITE_DOCKER_COMPOSE_BUILD_ALL`):
		warnNotSet(`BUILDKITE_DOCKER_COMPOSE_BUILD_ALL`, `BUILDKITE_DOCKER_COMPOSE_CONTAINER`)

	case sh.Env.Exists(`BUILDKITE_DOCKER_COMPOSE_LEAVE_VOLUMES`):
		warnNotSet(`BUILDKITE_DOCKER_COMPOSE_LEAVE_VOLUMES`, `BUILDKITE_DOCKER_COMPOSE_CONTAINER`)

	case sh.Env.Exists(`BUILDKITE_DOCKER_COMPOSE_LEAVE_VOLUMES`):
		warnNotSet(`BUILDKITE_DOCKER_COMPOSE_LEAVE_VOLUMES`, `BUILDKITE_DOCKER_COMPOSE_CONTAINER`)
	}

	return errors.New("Failed to find any docker env")
}

func tearDownDeprecatedDockerIntegration(sh *shell.Shell) error {
	if container, ok := sh.Env.Get(`DOCKER_CONTAINER`); ok {
		sh.Printf("~~~ Cleaning up Docker containers")

		if err := sh.Run("docker", "rm", "-f", "-v", container); err != nil {
			return err
		}
	} else if projectName, ok := sh.Env.Get(`COMPOSE_PROJ_NAME`); ok {
		sh.Printf("~~~ Cleaning up Docker containers")

		// Friendly kill
		_ = runDockerCompose(sh, projectName, "kill")

		if sh.Env.GetBool(`BUILDKITE_DOCKER_COMPOSE_LEAVE_VOLUMES`, false) {
			_ = runDockerCompose(sh, projectName, "rm", "--force", "--all")
		} else {
			_ = runDockerCompose(sh, projectName, "rm", "--force", "--all", "-v")
		}

		return runDockerCompose(sh, projectName, "down")
	}

	return nil
}

// runDockerCommand executes a script inside a docker container that is built as needed
// Ported from https://github.com/buildkite/agent/blob/2b8f1d569b659e07de346c0e3ae7090cb98e49ba/templates/bootstrap.sh#L439
func runDockerCommand(sh *shell.Shell, scriptPath string) error {
	jobId, _ := sh.Env.Get(`BUILDKITE_JOB_ID`)
	dockerContainer := fmt.Sprintf("buildkite_%s_container", jobId)
	dockerImage := fmt.Sprintf("buildkite_%s_image", jobId)

	dockerFile, _ := sh.Env.Get(`BUILDKITE_DOCKER_FILE`)
	if dockerFile == "" {
		dockerFile = "Dockerfile"
	}

	sh.Env.Set(`DOCKER_CONTAINER`, dockerContainer)
	sh.Env.Set(`DOCKER_IMAGE`, dockerImage)

	sh.Printf("~~~ :docker: Building Docker image %s", dockerImage)
	if err := sh.Run("docker", "build", "-f", dockerFile, "-t", dockerImage, "."); err != nil {
		return err
	}

	sh.Headerf(":docker: Running command (in Docker container)")
	if err := sh.Run("docker", "run", "--name", dockerContainer, dockerImage, scriptPath); err != nil {
		return err
	}

	return nil
}

// runDockerComposeCommand executes a script with docker-compose
// Ported from https://github.com/buildkite/agent/blob/2b8f1d569b659e07de346c0e3ae7090cb98e49ba/templates/bootstrap.sh#L462
func runDockerComposeCommand(sh *shell.Shell, scriptPath string) error {
	composeContainer, _ := sh.Env.Get(`BUILDKITE_DOCKER_COMPOSE_CONTAINER`)
	jobId, _ := sh.Env.Get(`BUILDKITE_JOB_ID`)

	// Compose strips dashes and underscores, so we'll remove them
	// to match the docker container names
	projectName := strings.Replace(fmt.Sprintf("buildkite%s", jobId), "-", "", -1)

	sh.Env.Set(`COMPOSE_PROJ_NAME`, projectName)
	sh.Headerf(":docker: Building Docker images")

	if sh.Env.GetBool(`BUILDKITE_DOCKER_COMPOSE_BUILD_ALL`, false) {
		if err := runDockerCompose(sh, projectName, "build", "--pull"); err != nil {
			return err
		}
	} else {
		if err := runDockerCompose(sh, projectName, "build", "--pull", composeContainer); err != nil {
			return err
		}
	}

	sh.Headerf(":docker: Running command (in Docker Compose container)")
	return runDockerCompose(sh, projectName, "run", composeContainer, scriptPath)
}

func runDockerCompose(sh *shell.Shell, projectName string, commandArgs ...string) error {
	args := []string{}

	composeFile, _ := sh.Env.Get(`BUILDKITE_DOCKER_COMPOSE_FILE`)
	if composeFile == "" {
		composeFile = "docker-compose.yml"
	}

	// composeFile might be multiple files, spaces or colons
	for _, chunk := range strings.Fields(composeFile) {
		for _, file := range strings.Split(chunk, ":") {
			args = append(args, "-f", file)
		}
	}

	args = append(args, "-p", projectName)

	if sh.Env.GetBool(`BUILDKITE_AGENT_DEBUG`, false) {
		args = append(args, "--verbose")
	}

	args = append(args, commandArgs...)
	return sh.Run("docker-compose", args...)
}
