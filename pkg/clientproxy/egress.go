package clientproxy

import (
	"cornus/pkg/api"
	"cornus/pkg/egresspolicy"
)

// ApplyEgressEnv resolves the proxy environment variables for an "env"-mode egress
// spec and merges them into spec.Env WITHOUT overwriting variables the caller set
// explicitly. It is a no-op when there is no egress spec, the mode is "proxy" or
// "transparent" (those are realised by the backend/relay, not by env injection), or
// no proxy is configured. Explicit spec.Egress.Proxies win over OS resolution; a
// routing rule to "cluster" contributes its pattern to NO_PROXY (bypass the proxy).
func ApplyEgressEnv(spec *api.DeploySpec) error {
	if spec == nil || spec.Egress == nil {
		return nil
	}
	e := spec.Egress
	if e.Mode != "" && e.Mode != "env" {
		return nil
	}
	extraNoProxy := egresspolicy.NoProxyPatterns(*e)
	var (
		env map[string]string
		err error
	)
	if len(e.Proxies) > 0 {
		env = fromExplicitProxies(e.Proxies, extraNoProxy)
	} else if env, err = EnvVars(extraNoProxy); err != nil {
		return err
	}
	if len(env) == 0 {
		return nil
	}
	if spec.Env == nil {
		spec.Env = map[string]string{}
	}
	for k, v := range env {
		if _, exists := spec.Env[k]; !exists {
			spec.Env[k] = v
		}
	}
	return nil
}

// fromExplicitProxies copies caller-provided proxy vars and folds the cluster-route
// patterns into NO_PROXY.
func fromExplicitProxies(proxies map[string]string, extraNoProxy []string) map[string]string {
	out := make(map[string]string, len(proxies)+2)
	for k, v := range proxies {
		out[k] = v
	}
	if len(extraNoProxy) > 0 {
		base := out["NO_PROXY"]
		if base == "" {
			base = out["no_proxy"]
		}
		merged := mergeNoProxy(base, extraNoProxy)
		out["NO_PROXY"] = merged
		out["no_proxy"] = merged
	}
	return out
}
