package client

import (
	"errors"
	"maps"
	"strings"
	"time"

	incusApi "github.com/lxc/incus/v6/shared/api"
)

// dnsIPWaitTimeout bounds how long to wait for a freshly started instance to
// acquire its DHCP lease before recording its DNS address.
const dnsIPWaitTimeout = 15 * time.Second

// scaleState accumulates the per-service instances and managed networks needed to
// rebuild the dnsmasq service-name records when the scale changes.
type scaleState struct {
	baseline     map[string]int
	instances    map[string]*Instance // current-run instances (for WaitIPs)
	networks     map[string]*Network
	scaleChanged bool
}

// isScaledInstance reports whether name follows the compose {service}-{index}
// convention (ends with a hyphen followed by one or more digits). This excludes
// non-service containers such as ic-healthd.
func isScaledInstance(name string) bool {
	i := strings.LastIndex(name, "-")
	if i <= 0 || i == len(name)-1 {
		return false
	}
	for _, c := range name[i+1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// RegisterScaleWatcher wires service-name DNS records into the project's managed
// networks via the client lifecycle hooks. On Open it snapshots the existing
// per-service instance counts; as instances are ensured or deleted it tracks
// scale changes; on Close, if the scale changed, it rewrites each network's
// raw.dnsmasq so every service name resolves to its current instance IPs.
// Call Open() after RegisterScaleWatcher so the connected hook fires correctly.
func (c *Client) RegisterScaleWatcher() error {
	st := &scaleState{
		baseline:  map[string]int{},
		instances: map[string]*Instance{},
		networks:  map[string]*Network{},
	}

	c.AddHookConnected(func(err error) error {
		names, e := c.incus.GetInstanceNames(incusApi.InstanceTypeContainer)
		if e != nil {
			return e
		}
		for _, name := range names {
			if isScaledInstance(name) {
				st.baseline[serviceName(name)]++
			}
		}
		c.LogDebug("ScaleWatcher connected", "baseline", st.baseline)
		return err
	})

	c.AddHookAfter(func(action Action, r Resource, _ Options, err error) error {
		if err != nil {
			return err
		}

		switch r.Kind() {
		case KindInstance:
			inst, ok := r.(*Instance)
			if !ok || !isScaledInstance(inst.name) {
				return err
			}
			switch action {
			case ActionEnsure:
				st.instances[inst.IncusName()] = inst
				c.LogDebug("ScaleWatcher ensure", "instance", inst.IncusName(), "created", inst.Created())
				if inst.Created() {
					st.scaleChanged = true
				}
			case ActionDelete:
				delete(st.instances, inst.IncusName())
				st.scaleChanged = true
				c.LogDebug("ScaleWatcher delete", "instance", inst.IncusName())
			}
		case KindNetwork:
			if net, ok := r.(*Network); ok && action == ActionEnsure && !net.Config.External {
				st.networks[net.IncusName()] = net
				c.LogDebug("ScaleWatcher network", "network", net.IncusName())
			}
		}
		return err
	})

	c.AddHookDisconnecting(func(err error) error {
		live := map[string]int{}
		for _, inst := range st.instances {
			live[inst.ServiceName()]++
		}
		c.LogDebug("ScaleWatcher disconnecting", "baseline", st.baseline, "live", live, "scaleChanged", st.scaleChanged)

		if !maps.Equal(live, st.baseline) {
			c.LogDebug("ScaleWatcher scale mismatch — updating DNS")
			st.scaleChanged = true
		}
		if !st.scaleChanged {
			c.LogDebug("ScaleWatcher no change — skipping DNS update")
			return err
		}

		// Fetch the full live instance list from Incus — not just the current
		// run's stack — so partial runs don't drop other services' records.
		names, e := c.incus.GetInstanceNames(incusApi.InstanceTypeContainer)
		if e != nil {
			return errors.Join(err, e)
		}
		c.LogDebug("ScaleWatcher live instances", "names", names)

		serviceIPs := map[string][]string{}
		for _, name := range names {
			if !isScaledInstance(name) {
				continue
			}

			var ipv4, ipv6 []string
			if inst, inRun := st.instances[name]; inRun {
				// Freshly started: wait for DHCP lease.
				_, ipv4, ipv6, e = inst.WaitIPs(dnsIPWaitTimeout)
				c.LogDebug("ScaleWatcher WaitIPs", "instance", name, "ipv4", ipv4, "ipv6", ipv6, "err", e)
			} else {
				// Pre-existing instance not in this run: fetch state directly.
				_, ipv4, ipv6, e = c.InstanceIPs(name)
				c.LogDebug("ScaleWatcher stateIPs", "instance", name, "ipv4", ipv4, "ipv6", ipv6, "err", e)
			}
			if e != nil {
				continue
			}

			svc := serviceName(name)
			serviceIPs[svc] = append(serviceIPs[svc], ipv4...)
			serviceIPs[svc] = append(serviceIPs[svc], ipv6...)
		}

		c.LogDebug("ScaleWatcher serviceIPs", "serviceIPs", serviceIPs)

		var errs error
		for _, net := range st.networks {
			c.LogDebug("ScaleWatcher updating network", "network", net.IncusName())
			errs = errors.Join(errs, net.UpdateDNSAliases(serviceIPs))
		}
		return errors.Join(err, errs)
	})

	return nil
}

// serviceName strips the trailing "-{index}" from a scaled instance name.
func serviceName(name string) string {
	if i := strings.LastIndex(name, "-"); i > 0 {
		return name[:i]
	}
	return name
}
