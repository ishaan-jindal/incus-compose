package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	incusApi "github.com/lxc/incus/v7/shared/api"
	"github.com/urfave/cli/v3"

	"github.com/lxc/incus-compose/client"
	"github.com/lxc/incus-compose/project"
)

// ANSI color codes for log output.
var logColors = []string{
	"36",   // cyan
	"33",   // yellow
	"32",   // green
	"35",   // magenta
	"34",   // blue
	"36;1", // intense cyan
	"33;1", // intense yellow
	"32;1", // intense green
	"35;1", // intense magenta
	"34;1", // intense blue
}

// logFormatter handles formatting and output of log lines from multiple services.
type logFormatter struct {
	mu         sync.Mutex
	out        io.Writer
	colors     map[string]string // resource name -> color code
	colorIndex int
	maxWidth   int
	noColor    bool
	buffers    map[string]*bytes.Buffer // resource name -> line buffer
}

// newLogFormatter creates a new log formatter.
func newLogFormatter(out io.Writer, noColor bool) *logFormatter {
	return &logFormatter{
		out:      out,
		colors:   make(map[string]string),
		buffers:  make(map[string]*bytes.Buffer),
		noColor:  noColor,
		maxWidth: 0,
	}
}

// registerService registers a service and assigns it a color.
func (f *logFormatter) registerService(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.colors[name]; ok {
		return
	}

	f.colors[name] = logColors[f.colorIndex%len(logColors)]
	f.colorIndex++
	f.buffers[name] = &bytes.Buffer{}

	if len(name) > f.maxWidth {
		f.maxWidth = len(name)
	}
}

// write handles incoming log data from a resource.
func (f *logFormatter) write(action client.Action, r client.Resource, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()

	name := r.Name()

	// Ensure service is registered
	if _, ok := f.colors[name]; !ok {
		f.colors[name] = logColors[f.colorIndex%len(logColors)]
		f.colorIndex++
		f.buffers[name] = &bytes.Buffer{}
		if len(name) > f.maxWidth {
			f.maxWidth = len(name)
		}
	}

	buf := f.buffers[name]
	buf.Write(data)

	// Process complete lines
	for {
		line, err := buf.ReadBytes('\n')
		if err != nil {
			// No complete line yet, put back unprocessed data
			buf.Write(line)
			break
		}

		// Output the line with prefix
		f.writeLine(name, line)
	}
}

// writeLine outputs a single line with prefix and color.
func (f *logFormatter) writeLine(name string, line []byte) {
	prefix := fmt.Sprintf("%-*s | ", f.maxWidth, name)

	if f.noColor {
		_, _ = fmt.Fprintf(f.out, "%s%s", prefix, line)
	} else {
		color := f.colors[name]
		// Color the prefix, not the log content
		_, _ = fmt.Fprintf(f.out, "\033[%sm%s\033[0m%s", color, prefix, line)
	}
}

// flush outputs any remaining buffered data.
func (f *logFormatter) flush() {
	f.mu.Lock()
	defer f.mu.Unlock()

	for name, buf := range f.buffers {
		if buf.Len() > 0 {
			// Output remaining data even if no newline
			line := buf.Bytes()
			f.writeLine(name, append(line, '\n'))
			buf.Reset()
		}
	}
}

// logTracker manages per-instance log goroutines for follow mode.
type logTracker struct {
	mu        sync.Mutex
	cancels   map[string]context.CancelFunc
	formatter *logFormatter
}

func newLogTracker(formatter *logFormatter) *logTracker {
	return &logTracker{
		cancels:   make(map[string]context.CancelFunc),
		formatter: formatter,
	}
}

func (lt *logTracker) start(ctx context.Context, inst *client.Instance) {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	name := inst.IncusName()
	if _, running := lt.cancels[name]; running {
		return
	}

	lt.formatter.registerService(inst.Name())

	logCtx, cancel := context.WithCancel(ctx)
	lt.cancels[name] = cancel

	go func() {
		_ = client.RunAction(logCtx, inst, client.ActionLog, client.OptionFollow())

		lt.mu.Lock()
		delete(lt.cancels, name)
		lt.mu.Unlock()
	}()
}

func (lt *logTracker) stop(incusName string) {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	cancel, ok := lt.cancels[incusName]
	if !ok {
		return
	}

	cancel()
	delete(lt.cancels, incusName)
}

func (lt *logTracker) stopAll() {
	lt.mu.Lock()
	defer lt.mu.Unlock()

	for name, cancel := range lt.cancels {
		cancel()
		delete(lt.cancels, name)
	}
}

func newLogsCommand() *cli.Command {
	return &cli.Command{
		Name:      "logs",
		Usage:     "View output from containers",
		Category:  "compose",
		ArgsUsage: "[SERVICE...]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "follow",
				Aliases: []string{"f"},
				Usage:   "Follow log output",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			noColor := noColor(ctx)

			globalClient, err := clientFromContext(ctx)
			if err != nil {
				return err
			}
			if err := globalClient.Connect(); err != nil {
				return err
			}

			p, err := project.New().Load(ctx, buildLoadOptions(cmd)...)
			if err != nil {
				globalClient.LogError("Configuring the project", "error", err)
				return errLogged.Wrap(err)
			}

			c, err := globalClient.EnsureProject(p.Name, client.EnsureProjectWithCreate())
			if err != nil {
				globalClient.LogError("Getting the incus project", "error", err)
				return errLogged.Wrap(err)
			}
			if err := c.Open(); err != nil {
				globalClient.LogError("Opening the project client", "error", err)
				return errLogged.Wrap(err)
			}
			defer func() { _ = c.Done() }()

			formatter := newLogFormatter(cmd.Root().Writer, noColor)
			globalClient.SetOutputHandler(formatter.write)

			stackOpts := []project.ToStackOption{project.ToStackOnlyServices(cmd.Args().Slice())}

			stack := client.NewStack(c, client.StackWorkers(cmd.Root().Int("workers")))
			if err := p.ToStack(c, stack, stackOpts...); err != nil {
				c.LogError(err.Error())
				return errLogged.Wrap(err)
			}

			isInstance := func(r client.Resource) bool {
				return r.Kind() == client.KindInstance
			}

			instanceStack := stack.ForActionF(client.ActionLog, isInstance)

			instances, err := client.ByKind[*client.Instance](instanceStack.All(), client.KindInstance)
			if err != nil {
				c.LogError("Filtering instances", "error", err)
				return errLogged.Wrap(err)
			}

			if !cmd.Bool("follow") {
				for _, inst := range instances {
					formatter.registerService(inst.Name())
				}

				if err := instanceStack.Run(ctx, client.ActionLog, cmd.Root().Writer, cmd.Root().ErrWriter); err != nil {
					c.LogError("Getting logs", "error", err)
					return errLogged.Wrap(err)
				}

				formatter.flush()
				return nil
			}

			// Follow mode: watch events, stream dynamically.
			knownInstances := make(map[string]*client.Instance, len(instances))
			for _, inst := range instances {
				knownInstances[inst.IncusName()] = inst
			}

			conn, err := c.Connection()
			if err != nil {
				c.LogError("Getting connection for events", "error", err)
				return errLogged.Wrap(err)
			}

			listener, err := conn.GetEventsByType([]string{incusApi.EventTypeLifecycle})
			if err != nil {
				c.LogError("Subscribing to events", "error", err)
				return errLogged.Wrap(err)
			}
			defer listener.Disconnect()

			tracker := newLogTracker(formatter)
			defer tracker.stopAll()

			_, err = listener.AddHandler([]string{incusApi.EventTypeLifecycle}, func(event incusApi.Event) {
				var lifecycle incusApi.EventLifecycle
				if err := json.Unmarshal(event.Metadata, &lifecycle); err != nil {
					return
				}

				inst, known := knownInstances[lifecycle.Name]
				if !known {
					return
				}

				switch lifecycle.Action {
				case incusApi.EventLifecycleInstanceStarted:
					tracker.start(ctx, inst)
				case incusApi.EventLifecycleInstanceStopped, incusApi.EventLifecycleInstanceDeleted, incusApi.EventLifecycleInstanceShutdown:
					tracker.stop(lifecycle.Name)
				}
			})
			if err != nil {
				c.LogError("Adding event handler", "error", err)
				return errLogged.Wrap(err)
			}

			for _, inst := range knownInstances {
				if inst.Running() {
					tracker.start(ctx, inst)
				}
			}

			<-ctx.Done()
			formatter.flush()
			return nil
		},
	}
}
