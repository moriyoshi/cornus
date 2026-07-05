//go:build otelcol

package otelcollector

import (
	"context"
	"fmt"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/connector"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/debugexporter"
	"go.opentelemetry.io/collector/exporter/otlpexporter"
	"go.opentelemetry.io/collector/exporter/otlphttpexporter"
	"go.opentelemetry.io/collector/extension"
	"go.opentelemetry.io/collector/otelcol"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/batchprocessor"
	"go.opentelemetry.io/collector/processor/memorylimiterprocessor"
	"go.opentelemetry.io/collector/receiver"
	"go.opentelemetry.io/collector/receiver/otlpreceiver"
	"go.opentelemetry.io/collector/service/telemetry/otelconftelemetry"
)

// inlineScheme is the confmap URI scheme of the in-memory config provider below.
// It only needs to be a valid (>=2 char, letter-led) scheme distinct from the
// real providers; the opaque part is ignored.
const inlineScheme = "cornus"

// Compiled reports that the Collector is linked into this build.
func Compiled() bool { return true }

// Run builds the curated Collector from cfg and runs it until ctx is cancelled,
// then shuts it down cleanly. It blocks, so the caretaker runs it as a supervised
// child; a config or startup error returns for the supervisor to retry.
func Run(ctx context.Context, cfg Config) error {
	settings := otelcol.CollectorSettings{
		Factories: curatedFactories,
		BuildInfo: component.BuildInfo{
			Command:     "cornus-otelcol",
			Description: "cornus embedded OpenTelemetry Collector",
			Version:     cfg.Version,
		},
		// The caretaker owns lifecycle via ctx; don't let the Collector install its
		// own SIGINT/SIGTERM handler (which would fight the caretaker's).
		DisableGracefulShutdown: true,
		ConfigProviderSettings: otelcol.ConfigProviderSettings{
			ResolverSettings: confmap.ResolverSettings{
				URIs:              []string{inlineScheme + ":config"},
				ProviderFactories: []confmap.ProviderFactory{inlineProviderFactory(buildConfigMap(cfg))},
			},
		},
	}
	col, err := otelcol.NewCollector(settings)
	if err != nil {
		return fmt.Errorf("otelcollector: new: %w", err)
	}
	// Belt-and-suspenders: with graceful shutdown disabled, Run returns on ctx
	// cancellation, but explicitly signalling Shutdown guarantees teardown even if
	// Run is mid-startup when ctx is cancelled. Shutdown is idempotent.
	go func() {
		<-ctx.Done()
		col.Shutdown()
	}()
	if err := col.Run(ctx); err != nil {
		return fmt.Errorf("otelcollector: run: %w", err)
	}
	return nil
}

// curatedFactories is the fixed, small component set the embedded Collector
// supports. Adding a component here is the only place the linked surface grows.
func curatedFactories() (otelcol.Factories, error) {
	var f otelcol.Factories

	f.Receivers = map[component.Type]receiver.Factory{}
	for _, r := range []receiver.Factory{otlpreceiver.NewFactory()} {
		f.Receivers[r.Type()] = r
	}

	f.Processors = map[component.Type]processor.Factory{}
	for _, p := range []processor.Factory{
		memorylimiterprocessor.NewFactory(),
		batchprocessor.NewFactory(),
	} {
		f.Processors[p.Type()] = p
	}

	f.Exporters = map[component.Type]exporter.Factory{}
	for _, e := range []exporter.Factory{
		otlpexporter.NewFactory(),
		otlphttpexporter.NewFactory(),
		debugexporter.NewFactory(),
	} {
		f.Exporters[e.Type()] = e
	}

	f.Extensions = map[component.Type]extension.Factory{}
	f.Connectors = map[component.Type]connector.Factory{}
	// Required: the service telemetry factory (drives the Collector's own
	// providers). otelconf is the stock implementation.
	f.Telemetry = otelconftelemetry.NewFactory()
	return f, nil
}

// inlineProviderFactory returns a confmap ProviderFactory that serves conf from
// memory, so the Collector needs no config file — the deploy backend's rendered
// config rides in-process.
func inlineProviderFactory(conf map[string]any) confmap.ProviderFactory {
	return confmap.NewProviderFactory(func(confmap.ProviderSettings) confmap.Provider {
		return &inlineProvider{conf: conf}
	})
}

type inlineProvider struct{ conf map[string]any }

func (p *inlineProvider) Retrieve(context.Context, string, confmap.WatcherFunc) (*confmap.Retrieved, error) {
	return confmap.NewRetrieved(p.conf)
}

func (p *inlineProvider) Scheme() string                 { return inlineScheme }
func (p *inlineProvider) Shutdown(context.Context) error { return nil }
