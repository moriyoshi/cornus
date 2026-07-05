package server

import (
	"context"
	"log/slog"
	"os"
	"time"

	"cornus/pkg/deploy"
	"cornus/pkg/logging"
	"cornus/pkg/supervisor"
)

const kubernetesRegistryRefreshInterval = 4 * time.Hour

// superviseRegistryCredentials keeps the Kubernetes pull Secret fresh for
// future kubelet pulls. Other backends mint per pull and need no background
// service; auth-off servers register nothing.
func (s *Server) superviseRegistryCredentials() {
	if s.auth == nil || s.auth.internal == nil {
		return
	}
	switch os.Getenv("CORNUS_DEPLOY_BACKEND") {
	case "kubernetes", "k8s":
	default:
		return
	}
	s.sup.Add("registry-credentials", supervisor.ServiceFunc(s.refreshRegistryCredentials), supervisor.Restart)
}

func (s *Server) refreshRegistryCredentials(ctx context.Context) error {
	ticker := time.NewTicker(kubernetesRegistryRefreshInterval)
	defer ticker.Stop()
	logctx := logging.WithAttrs(context.Background(), slog.String("component", "registry-credentials"))
	log := logging.FromContext(logctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			backend, err := s.getBackend()
			if err != nil {
				log.WarnContext(logctx, "registry credential refresh skipped", "error", err)
				continue
			}
			refresher, ok := backend.(deploy.RegistryCredentialRefresher)
			if !ok {
				continue
			}
			if err := refresher.RefreshRegistryCredentials(ctx); err != nil {
				log.WarnContext(logctx, "registry credential refresh failed", "error", err)
			}
		}
	}
}
