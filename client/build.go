package client

// BuildMode controls how build-configured images are treated during Ensure.
type BuildMode int

const (
	// BuildAuto builds the image only when it is missing (default).
	BuildAuto BuildMode = iota
	// BuildForce rebuilds the image even if an existing one is present.
	BuildForce
	// BuildNever never builds; returns an error if the image is missing.
	BuildNever
)

// BuildInfo carries the rebuild mode and optional builder selection for ActionEnsure.
type BuildInfo struct {
	// Mode controls rebuild behaviour.
	Mode BuildMode

	// PreferredBuilder is the container builder binary name or absolute path.
	// Empty means auto-detect (tries podman, then docker).
	PreferredBuilder string
}

// BuildConfig holds the parameters read from a compose service's build: block.
type BuildConfig struct {
	// Context is the build context directory (absolute path).
	Context string

	// Dockerfile is an optional path to the Containerfile/Dockerfile.
	// Empty means the builder uses its default (Containerfile or Dockerfile in Context).
	Dockerfile string

	// DockerfileInline is inline Dockerfile content from compose build.dockerfile_inline.
	DockerfileInline string

	// Target is the Dockerfile stage to build.
	Target string

	// Platform is the OCI platform to build for, for example linux/amd64.
	Platform string

	// Args are build-time variables (--build-arg).
	Args map[string]string

	// NoCache disables layer caching during the build.
	NoCache bool

	// Pull always attempts to pull a newer version of the base image.
	Pull bool
}

// incusArchToPlatform maps an Incus architecture name to an OCI platform.
func incusArchToPlatform(arch string) (string, bool) {
	switch arch {
	case "x86_64":
		return "linux/amd64", true
	case "i686":
		return "linux/386", true
	case "aarch64":
		return "linux/arm64", true
	case "armv7", "armv7l":
		return "linux/arm/v7", true
	case "armv6", "armv6l":
		return "linux/arm/v6", true
	case "ppc64le":
		return "linux/ppc64le", true
	case "s390x":
		return "linux/s390x", true
	case "riscv64":
		return "linux/riscv64", true
	}
	return "", false
}

func platformToIncusArch(platform string, arches []string) (string, bool) {
	for _, arch := range arches {
		candidate, ok := incusArchToPlatform(arch)
		if ok && candidate == platform {
			return arch, true
		}
	}
	return "", false
}
