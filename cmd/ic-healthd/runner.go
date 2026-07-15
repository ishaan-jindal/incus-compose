package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	incus "github.com/lxc/incus/v7/client"
	incusApi "github.com/lxc/incus/v7/shared/api"

	"github.com/lxc/incus-compose/shared"
)

// Runner manages all health checkers for one project via event-driven
// discovery: it reacts to the Incus lifecycle event stream instead of
// re-listing instances on a timer or SIGHUP. tracked is the single source of
// truth for what's running - every code path (initial discover, reconnect
// reseed, event handling) reconciles against it: start/kill exactly the
// delta, leave everything else running untouched.
type Runner struct {
	config *Config

	conn incus.InstanceServer

	mu      sync.Mutex
	tracked map[string]*trackedInstance
}

// NewRunner creates a new runner with the given configuration.
func NewRunner(cfg *Config) (*Runner, error) {
	if cfg.Project == "" {
		return nil, errors.New("--project is required")
	}

	return &Runner{
		config:  cfg,
		tracked: map[string]*trackedInstance{},
	}, nil
}

func (r *Runner) trackedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tracked)
}

// Run connects to Incus, discovers instances, and reacts to lifecycle events
// until ctx is canceled. This is the main entry point, it should never exit
// except when the context is done. reload triggers a manual full resync (see
// resync) without tearing down the current connection/listener - the
// replacement for the old SIGHUP path; normal operation no longer needs it.
func (r *Runner) Run(ctx context.Context, reload <-chan struct{}) error {
	for {
		conn, err := r.connect()
		if err != nil {
			slog.Error("connecting to incus", "error", err)

			select {
			case <-ctx.Done():
				return fmt.Errorf("failed to connect to incus: %w", err)
			case <-time.After(time.Second):
				continue
			}
		}
		r.conn = conn.UseProject(r.config.Project)

		err = r.writeStatus(shared.HealthStatusHealthy)
		if err != nil {
			slog.Warn("Failed to update my own status", "error", err)
		}

		err = r.resync(ctx)
		if err != nil {
			slog.Warn("discovery had errors", "error", err)
		}

		listener, err := r.conn.GetEventsByType([]string{incusApi.EventTypeLifecycle})
		if err != nil {
			slog.Error("opening event listener", "error", err)

			select {
			case <-ctx.Done():
				return r.writeStatus(shared.HealthStatusUnhealthy)
			case <-time.After(time.Second):
				continue
			}
		}

		_, err = listener.AddHandler([]string{incusApi.EventTypeLifecycle}, func(event incusApi.Event) {
			r.handleEvent(ctx, event)
		})
		if err != nil {
			listener.Disconnect()
			slog.Error("registering event handler", "error", err)
			continue
		}

		slog.Info("Health daemon running", "tracked", r.trackedCount())

		disconnected := make(chan struct{})
		go func() {
			err := listener.Wait()
			if err != nil {
				slog.Error("While waiting for events", "error", err)
			}
			close(disconnected)
		}()

		stop := false
		for !stop {
			select {
			case <-ctx.Done():
				listener.Disconnect()
				return r.writeStatus(shared.HealthStatusUnhealthy)
			case <-reload:
				slog.Info("manual resync requested")
				if err := r.resync(ctx); err != nil {
					slog.Warn("resync had errors", "error", err)
				}
			case <-disconnected:
				slog.Warn("event listener disconnected, reconnecting")
				stop = true
			}
		}
	}
}

// writeStatus persists status into the daemon's own instance, if configured
// to know its own project/name (see Config.OwnProject/OwnName).
func (r *Runner) writeStatus(status string) error {
	if r.config.OwnName == "" || r.config.OwnProject == "" {
		return nil
	}

	myConn := r.conn.UseProject(r.config.OwnProject)

	slog.Debug("Writing status", "own-project", r.config.OwnProject, "own-name", r.config.OwnName, "status", status)

	inst, etag, err := myConn.GetInstance(r.config.OwnName)
	if err != nil {
		return err
	}

	wInst := inst.Writable()
	wInst.Config[shared.HealthStatusKey] = status

	op, err := myConn.UpdateInstance(r.config.OwnName, wInst, etag)
	if err != nil {
		return err
	}

	return op.Wait()
}

// connect returns an authenticated Incus client.
//
// On first run, the persisted cert is missing: we generate one, register it
// with the one-time TrustToken, and persist it for subsequent runs.
// On restart, the persisted cert is reused and the token (already consumed) is ignored.
func (r *Runner) connect() (incus.InstanceServer, error) {
	// Token to register (generates KEY/CERT)
	tokenPath := filepath.Join(r.config.SecretsDir, tokenFile)

	// Paths after r.register(...)
	certDataPath := filepath.Join(r.config.DataDir, certFile)
	keyDataPath := filepath.Join(r.config.DataDir, keyFile)

	if !fileExists(certDataPath) && (r.config.Token != "" || fileExists(tokenPath)) {
		slog.Debug("fresh token performing first-run registration")

		token := r.config.Token
		if token == "" {
			slog.Debug("Reading the token from a file")
			tokenBytes, err := os.ReadFile(tokenPath)
			if err != nil {
				return nil, fmt.Errorf("reading token: %w", err)
			}
			token = strings.TrimSpace(string(tokenBytes))
			if token == "" {
				return nil, errors.New("token file is empty")
			}
		}

		conn, err := r.register(token)
		if err != nil {
			return nil, fmt.Errorf("first-run registration: %w", err)
		}

		return conn, nil
	} else if !fileExists(keyDataPath) || !fileExists(certDataPath) {
		return nil, fmt.Errorf("no token and no registration happened before")
	}

	slog.Debug("reusing persisted cert from data dir")

	certPEM, err := os.ReadFile(certDataPath)
	if err != nil {
		return nil, fmt.Errorf("reading cert: %w", err)
	}
	keyPEM, err := os.ReadFile(keyDataPath)
	if err != nil {
		return nil, fmt.Errorf("reading key: %w", err)
	}

	return incus.ConnectIncus(r.config.IncusURL, &incus.ConnectionArgs{
		TLSClientCert:      string(certPEM),
		TLSClientKey:       string(keyPEM),
		InsecureSkipVerify: true,
	})
}

// register generates a self-signed ECDSA cert, presents it to Incus over TLS,
// and asks the server to add it to the trust store using the one-time token.
// The server reads the cert from the TLS handshake (see incusd certificates.go),
// applies the restrictions stored in the token metadata, and returns trusted=true.
// The cert/key are persisted to the data dir only after successful registration,
// so a failed attempt is retried on the next run.
func (r *Runner) register(token string) (incus.InstanceServer, error) {
	certPEM, keyPEM, err := generateClientCert()
	if err != nil {
		return nil, fmt.Errorf("generating cert: %w", err)
	}

	conn, err := incus.ConnectIncus(r.config.IncusURL, &incus.ConnectionArgs{
		TLSClientCert:      string(certPEM),
		TLSClientKey:       string(keyPEM),
		InsecureSkipVerify: true,
	})
	if err != nil {
		return nil, fmt.Errorf("connecting to register cert: %w", err)
	}

	if err := conn.CreateCertificate(
		incusApi.CertificatesPost{
			CertificatePut: incusApi.CertificatePut{
				Name:       r.config.Project + "-ic-healthd",
				Restricted: true,
				Projects:   []string{r.config.Project},
			}, TrustToken: token,
		}); err != nil {
		return nil, fmt.Errorf("registering cert with token: %w", err)
	}

	if err := os.MkdirAll(r.config.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating data-dir %v: %w", r.config.DataDir, err)
	}

	keyDataPath := filepath.Join(r.config.DataDir, keyFile)
	if err := os.WriteFile(keyDataPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("saving key %v: %w", keyDataPath, err)
	}

	certDataPath := filepath.Join(r.config.DataDir, certFile)
	if err := os.WriteFile(certDataPath, certPEM, 0o600); err != nil {
		return nil, fmt.Errorf("saving cert %v: %w", certDataPath, err)
	}

	slog.Debug("certificate registered and persisted")
	return conn, nil
}

// generateClientCert returns a fresh ECDSA P-384 key pair and self-signed
// X.509 client certificate, both PEM-encoded.
func generateClientCert() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("ecdsa key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "ic-healthd"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("creating cert: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling key: %w", err)
	}

	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
