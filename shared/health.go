// Package shared holds constants and small helpers shared across packages
// that would otherwise need to import the full client package.
package shared

// Health check status constants written to HealthConfigKey by ic-healthd.
const (
	HealthStatusUnknown   = "unknown"
	HealthStatusHealthy   = "healthy"
	HealthStatusUnhealthy = "unhealthy"
	HealthStatusStopped   = "stopped"

	HealthKeyPrefix = "user.healthcheck."

	// HealthStatusKey is the instance config key used to store health status.
	HealthStatusKey = HealthKeyPrefix + "status"

	// HealthStoppedKey when "true" means healthchecking is stopped.
	HealthStoppedKey = HealthKeyPrefix + "stopped"
)
