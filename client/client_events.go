package client

import (
	"encoding/json"
	"fmt"

	incusApi "github.com/lxc/incus/v7/shared/api"
)

func addEventHook(c *Client) {
	c.AddHookConnected(func(err error) error {
		if err != nil {
			return err
		}

		listener, err := c.incus.GetEventsByType([]string{incusApi.EventTypeLifecycle})
		if err != nil {
			return fmt.Errorf("opening an event listener: %w", err)
		}

		_, err = listener.AddHandler([]string{incusApi.EventTypeLifecycle}, func(event incusApi.Event) {
			var lc incusApi.EventLifecycle

			err := json.Unmarshal(event.Metadata, &lc)
			if err != nil {
				c.LogDebug("Decoding lifecycle event", "error", err)
				return
			}

			// Ignore all lifecycle events except started, stopped and updated.
			switch lc.Action {
			case incusApi.EventLifecycleInstanceStarted:
			case incusApi.EventLifecycleInstanceStopped:
			case incusApi.EventLifecycleInstanceUpdated:
			default:
				return
			}

			if lc.Name == "" {
				return
			}

			c.rangeResources(func(r Resource) {
				if r.Kind() != KindInstance || r.IncusName() != lc.Name {
					return
				}

				inst, ok := r.(*Instance)
				if !ok {
					return
				}

				// We ignore errors here as on stop/delete this would log an error.
				err = inst.fetch()
				if err == nil {
					c.LogDebug("New lifecycle event", "resource", inst, "action", lc.Action, "health_status", inst.IncusInstance.Config[HealthStatusKey])
				}
			})
		})
		if err != nil {
			listener.Disconnect()
			return fmt.Errorf("registering an event handler: %w", err)
		}

		c.AddHookDone(func(err error) error {
			listener.Disconnect()
			return err
		})

		return nil
	})
}
