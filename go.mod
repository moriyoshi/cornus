module cornus

go 1.26.0

require (
	github.com/alecthomas/kong v1.15.0
	github.com/alicebob/miniredis/v2 v2.38.0
	github.com/aws/aws-sdk-go-v2 v1.42.0
	github.com/aws/aws-sdk-go-v2/config v1.32.26
	github.com/aws/aws-sdk-go-v2/credentials v1.19.25
	github.com/aws/aws-sdk-go-v2/service/s3 v1.104.1
	github.com/aws/aws-sdk-go-v2/service/sts v1.43.4
	github.com/aws/smithy-go v1.27.1
	github.com/charmbracelet/bubbles v1.0.0
	github.com/charmbracelet/bubbletea v1.3.10
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/coder/websocket v1.8.15
	github.com/containerd/cgroups/v3 v3.0.3
	github.com/containerd/console v1.0.4
	github.com/containerd/containerd v1.7.24
	github.com/containerd/continuity v0.4.5
	github.com/containerd/errdefs v1.0.0
	github.com/containerd/go-cni v1.1.10
	github.com/containerd/go-runc v1.1.0
	github.com/containerd/typeurl/v2 v2.2.3
	github.com/creack/pty v1.1.24
	github.com/distribution/reference v0.6.0
	github.com/docker/cli v29.5.3+incompatible
	github.com/docker/docker v27.4.0-rc.2+incompatible
	github.com/docker/go-units v0.5.0
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/google/go-containerregistry v0.21.7
	github.com/google/nftables v0.3.0
	github.com/grafana/sobek v0.0.0-20260708062710-267a0e055bb4
	github.com/hashicorp/yamux v0.1.2
	github.com/hugelgupf/p9 v0.4.1
	github.com/kevinburke/ssh_config v1.6.0
	github.com/mattn/go-shellwords v1.0.12
	github.com/miekg/dns v1.1.72
	github.com/moby/buildkit v0.18.2
	github.com/moby/patternmatcher v0.6.0
	github.com/modelcontextprotocol/go-sdk v1.6.1
	github.com/muesli/termenv v0.16.0
	github.com/opencontainers/go-digest v1.0.0
	github.com/opencontainers/image-spec v1.1.1
	github.com/opencontainers/runtime-spec v1.2.1
	github.com/pelletier/go-toml v1.9.5
	github.com/prometheus/client_golang v1.23.2
	github.com/redis/go-redis/v9 v9.21.0
	github.com/tonistiigi/fsutil v0.0.0-20241121093142-31cf1f437184
	github.com/tonistiigi/vt100 v0.0.0-20240514184818-90bafcd6abab
	github.com/vishvananda/netlink v1.3.0
	github.com/zeebo/xxh3 v1.1.0
	go.opentelemetry.io/collector/component v1.62.0
	go.opentelemetry.io/collector/confmap v1.62.0
	go.opentelemetry.io/collector/connector v0.156.0
	go.opentelemetry.io/collector/exporter v1.62.0
	go.opentelemetry.io/collector/exporter/debugexporter v0.156.0
	go.opentelemetry.io/collector/exporter/otlpexporter v0.156.0
	go.opentelemetry.io/collector/exporter/otlphttpexporter v0.156.0
	go.opentelemetry.io/collector/extension v1.62.0
	go.opentelemetry.io/collector/otelcol v0.156.0
	go.opentelemetry.io/collector/processor v1.62.0
	go.opentelemetry.io/collector/processor/batchprocessor v0.156.0
	go.opentelemetry.io/collector/processor/memorylimiterprocessor v0.156.0
	go.opentelemetry.io/collector/receiver v1.62.0
	go.opentelemetry.io/collector/receiver/otlpreceiver v0.156.0
	go.opentelemetry.io/collector/service v0.156.0
	go.opentelemetry.io/contrib/bridges/otelslog v0.19.0
	go.opentelemetry.io/contrib/exporters/autoexport v0.69.0
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.69.0
	go.opentelemetry.io/contrib/instrumentation/runtime v0.69.0
	go.opentelemetry.io/otel v1.44.0
	go.opentelemetry.io/otel/exporters/prometheus v0.66.0
	go.opentelemetry.io/otel/log v0.20.0
	go.opentelemetry.io/otel/metric v1.44.0
	go.opentelemetry.io/otel/sdk v1.44.0
	go.opentelemetry.io/otel/sdk/log v0.20.0
	go.opentelemetry.io/otel/sdk/metric v1.44.0
	go.opentelemetry.io/otel/trace v1.44.0
	go.opentelemetry.io/proto/otlp v1.10.0
	go.starlark.net v0.0.0-20260613233743-8ba36ccb83fb
	gocloud.dev v0.46.0
	golang.ngrok.com/ngrok/v2 v2.1.4
	golang.org/x/crypto v0.53.0
	golang.org/x/net v0.56.0
	golang.org/x/sync v0.21.0
	golang.org/x/sys v0.46.0
	golang.org/x/term v0.44.0
	google.golang.org/grpc v1.82.0
	google.golang.org/protobuf v1.36.11
	k8s.io/api v0.32.1
	k8s.io/apimachinery v0.32.1
	k8s.io/client-go v0.32.1
	k8s.io/utils v0.0.0-20241104100929-3ea5e8cea738
	sigs.k8s.io/yaml v1.4.0
)

// yamux is vendored in-repo (third_party/yamux) as a fork: it adds per-stream
// send priority (strict for the control channel + weighted round-robin among the
// data/mount streams) and a per-stream window ceiling, so a bulk block-mount
// stream cannot starve the latency-sensitive control channel nor force a global
// 16 MiB window on every stream. See .agents/docs/JOURNAL.md.
replace github.com/hashicorp/yamux => ./third_party/yamux

require (
	cel.dev/expr v0.25.1 // indirect
	cloud.google.com/go v0.123.0 // indirect
	cloud.google.com/go/auth v0.18.2 // indirect
	cloud.google.com/go/auth/oauth2adapt v0.2.8 // indirect
	cloud.google.com/go/compute/metadata v0.9.0 // indirect
	cloud.google.com/go/iam v1.5.3 // indirect
	cloud.google.com/go/monitoring v1.24.3 // indirect
	cloud.google.com/go/storage v1.61.3 // indirect
	github.com/AdaLogics/go-fuzz-headers v0.0.0-20240806141605-e8a1dd7889d6 // indirect
	github.com/AdamKorcz/go-118-fuzz-build v0.0.0-20230306123547-8075edf89bb0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.21.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.13.1 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.11.2 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/storage/azblob v1.6.4 // indirect
	github.com/AzureAD/microsoft-authentication-library-for-go v1.7.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/detectors/gcp v1.32.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/exporter/metric v0.55.0 // indirect
	github.com/GoogleCloudPlatform/opentelemetry-operations-go/internal/resourcemapping v0.55.0 // indirect
	github.com/Microsoft/go-winio v0.6.2 // indirect
	github.com/Microsoft/hcsshim v0.12.8 // indirect
	github.com/agext/levenshtein v1.2.3 // indirect
	github.com/anchore/go-struct-converter v0.0.0-20221118182256-c68fdcfa2092 // indirect
	github.com/armon/circbuf v0.0.0-20190214190532-5111143e8da2 // indirect
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.13 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.29 // indirect
	github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager v0.2.3 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.29 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.29 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.4.30 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.9.22 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.29 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.19.30 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.2.1 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.31.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.36.7 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cenkalti/backoff/v7 v7.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/charmbracelet/colorprofile v0.4.1 // indirect
	github.com/charmbracelet/harmonica v0.2.0 // indirect
	github.com/charmbracelet/x/ansi v0.11.6 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.9.0 // indirect
	github.com/clipperhouse/stringish v0.1.1 // indirect
	github.com/clipperhouse/uax29/v2 v2.5.0 // indirect
	github.com/cncf/xds/go v0.0.0-20260202195803-dba9d589def2 // indirect
	github.com/containerd/containerd/api v1.7.19 // indirect
	github.com/containerd/fifo v1.1.0 // indirect
	github.com/containerd/log v0.1.0 // indirect
	github.com/containerd/nydus-snapshotter v0.14.0 // indirect
	github.com/containerd/platforms v0.2.1 // indirect
	github.com/containerd/stargz-snapshotter/estargz v0.15.1 // indirect
	github.com/containerd/ttrpc v1.2.5 // indirect
	github.com/containernetworking/cni v1.2.2 // indirect
	github.com/containernetworking/plugins v1.4.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/dlclark/regexp2/v2 v2.2.2 // indirect
	github.com/docker/docker-credential-helpers v0.9.3 // indirect
	github.com/docker/go-connections v0.7.0 // indirect
	github.com/docker/go-events v0.0.0-20190806004212-e31b211e4f1c // indirect
	github.com/ebitengine/purego v0.10.0 // indirect
	github.com/emicklei/go-restful/v3 v3.11.0 // indirect
	github.com/envoyproxy/go-control-plane/envoy v1.37.0 // indirect
	github.com/envoyproxy/protoc-gen-validate v1.3.3 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/felixge/httpsnoop v1.0.4 // indirect
	github.com/foxboron/go-tpm-keyfiles v0.0.0-20251226215517-609e4778396f // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/fxamacker/cbor/v2 v2.7.0 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-ole/go-ole v1.2.6 // indirect
	github.com/go-openapi/jsonpointer v0.21.0 // indirect
	github.com/go-openapi/jsonreference v0.20.2 // indirect
	github.com/go-openapi/swag v0.23.0 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/gobwas/glob v0.2.3 // indirect
	github.com/gofrs/flock v0.12.1 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/google/gnostic-models v0.6.8 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/google/go-tpm v0.9.8 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/pprof v0.0.0-20241029153458-d1b30febd7db // indirect
	github.com/google/s2a-go v0.1.9 // indirect
	github.com/google/shlex v0.0.0-20191202100458-e7afc7fbc510 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/google/wire v0.7.0 // indirect
	github.com/googleapis/enterprise-certificate-proxy v0.3.14 // indirect
	github.com/googleapis/gax-go/v2 v2.19.0 // indirect
	github.com/gorilla/websocket v1.5.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-cleanhttp v0.5.2 // indirect
	github.com/hashicorp/go-immutable-radix/v2 v2.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/in-toto/in-toto-golang v0.5.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/jpillora/backoff v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/compress v1.18.7 // indirect
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	github.com/knadh/koanf/maps v0.1.2 // indirect
	github.com/knadh/koanf/providers/confmap v1.0.0 // indirect
	github.com/knadh/koanf/v2 v2.3.5 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.3.0 // indirect
	github.com/lufia/plan9stats v0.0.0-20251013123823-9fd1530e3ec3 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.19 // indirect
	github.com/mdlayher/netlink v1.7.3-0.20250113171957-fbb4dce95f42 // indirect
	github.com/mdlayher/socket v0.5.0 // indirect
	github.com/mitchellh/copystructure v1.2.0 // indirect
	github.com/mitchellh/hashstructure/v2 v2.0.2 // indirect
	github.com/mitchellh/reflectwalk v1.0.2 // indirect
	github.com/moby/docker-image-spec v1.3.1 // indirect
	github.com/moby/locker v1.0.1 // indirect
	github.com/moby/spdystream v0.5.0 // indirect
	github.com/moby/sys/mount v0.3.4 // indirect
	github.com/moby/sys/mountinfo v0.7.2 // indirect
	github.com/moby/sys/sequential v0.6.0 // indirect
	github.com/moby/sys/signal v0.7.1 // indirect
	github.com/moby/sys/symlink v0.2.0 // indirect
	github.com/moby/sys/user v0.4.0 // indirect
	github.com/moby/sys/userns v0.1.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/morikuni/aec v1.0.0 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/mxk/go-flowrate v0.0.0-20140419014527-cca7078d478f // indirect
	github.com/opencontainers/selinux v1.11.0 // indirect
	github.com/package-url/packageurl-go v0.1.1-0.20220428063043-89078438f170 // indirect
	github.com/pierrec/lz4/v4 v4.1.27 // indirect
	github.com/pkg/browser v0.0.0-20240102092130-5ac0b6a4141c // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/planetscale/vtprotobuf v0.6.1-0.20240319094008-0393e58bdf10 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/power-devops/perfstat v0.0.0-20240221224432-82ca36839d55 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.69.0 // indirect
	github.com/prometheus/otlptranslator v1.0.0 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/rs/cors v1.11.1 // indirect
	github.com/secure-systems-lab/go-securesystemslib v0.4.0 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/shibumi/go-pathspec v1.3.0 // indirect
	github.com/shirou/gopsutil/v4 v4.26.5 // indirect
	github.com/sirupsen/logrus v1.9.4 // indirect
	github.com/spdx/tools-golang v0.5.3 // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/spiffe/go-spiffe/v2 v2.6.0 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/tklauser/go-sysconf v0.3.16 // indirect
	github.com/tklauser/numcpus v0.11.0 // indirect
	github.com/tonistiigi/dchapes-mode v0.0.0-20241001053921-ca0759fec205 // indirect
	github.com/tonistiigi/go-archvariant v1.0.0 // indirect
	github.com/tonistiigi/go-csvvalue v0.0.0-20240710180619-ddb21b71c0b4 // indirect
	github.com/tonistiigi/units v0.0.0-20180711220420-6950e57a87ea // indirect
	github.com/u-root/uio v0.0.0-20230305220412-3e8cd9d6bf63 // indirect
	github.com/vbatts/tar-split v0.11.5 // indirect
	github.com/vishvananda/netns v0.0.4 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	github.com/yusufpapurcu/wmi v1.2.4 // indirect
	go.etcd.io/bbolt v1.3.11 // indirect
	go.opencensus.io v0.24.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/collector v0.156.0 // indirect
	go.opentelemetry.io/collector/client v1.62.0 // indirect
	go.opentelemetry.io/collector/component/componentstatus v0.156.0 // indirect
	go.opentelemetry.io/collector/component/componenttest v0.156.0 // indirect
	go.opentelemetry.io/collector/config/configauth v1.62.0 // indirect
	go.opentelemetry.io/collector/config/configcompression v1.62.0 // indirect
	go.opentelemetry.io/collector/config/configgrpc v0.156.0 // indirect
	go.opentelemetry.io/collector/config/confighttp v0.156.0 // indirect
	go.opentelemetry.io/collector/config/configmiddleware v1.62.0 // indirect
	go.opentelemetry.io/collector/config/confignet v1.62.0 // indirect
	go.opentelemetry.io/collector/config/configopaque v1.62.0 // indirect
	go.opentelemetry.io/collector/config/configoptional v1.62.0 // indirect
	go.opentelemetry.io/collector/config/configretry v1.62.0 // indirect
	go.opentelemetry.io/collector/config/configtelemetry v0.156.0 // indirect
	go.opentelemetry.io/collector/config/configtls v1.62.0 // indirect
	go.opentelemetry.io/collector/confmap/xconfmap v0.156.0 // indirect
	go.opentelemetry.io/collector/connector/connectortest v0.156.0 // indirect
	go.opentelemetry.io/collector/connector/xconnector v0.156.0 // indirect
	go.opentelemetry.io/collector/consumer v1.62.0 // indirect
	go.opentelemetry.io/collector/consumer/consumererror v0.156.0 // indirect
	go.opentelemetry.io/collector/consumer/consumererror/xconsumererror v0.156.0 // indirect
	go.opentelemetry.io/collector/consumer/consumertest v0.156.0 // indirect
	go.opentelemetry.io/collector/consumer/xconsumer v0.156.0 // indirect
	go.opentelemetry.io/collector/exporter/exporterhelper v0.156.0 // indirect
	go.opentelemetry.io/collector/exporter/exporterhelper/xexporterhelper v0.156.0 // indirect
	go.opentelemetry.io/collector/exporter/exportertest v0.156.0 // indirect
	go.opentelemetry.io/collector/exporter/xexporter v0.156.0 // indirect
	go.opentelemetry.io/collector/extension/extensionauth v1.62.0 // indirect
	go.opentelemetry.io/collector/extension/extensioncapabilities v0.156.0 // indirect
	go.opentelemetry.io/collector/extension/extensionmiddleware v0.156.0 // indirect
	go.opentelemetry.io/collector/extension/extensiontest v0.156.0 // indirect
	go.opentelemetry.io/collector/extension/xextension v0.156.0 // indirect
	go.opentelemetry.io/collector/featuregate v1.62.0 // indirect
	go.opentelemetry.io/collector/internal/componentalias v0.156.0 // indirect
	go.opentelemetry.io/collector/internal/fanoutconsumer v0.156.0 // indirect
	go.opentelemetry.io/collector/internal/memorylimiter v0.156.0 // indirect
	go.opentelemetry.io/collector/internal/sharedcomponent v0.156.0 // indirect
	go.opentelemetry.io/collector/internal/telemetry v0.156.0 // indirect
	go.opentelemetry.io/collector/pdata v1.62.0 // indirect
	go.opentelemetry.io/collector/pdata/pprofile v0.156.0 // indirect
	go.opentelemetry.io/collector/pdata/testdata v0.156.0 // indirect
	go.opentelemetry.io/collector/pdata/xpdata v0.156.0 // indirect
	go.opentelemetry.io/collector/pipeline v1.62.0 // indirect
	go.opentelemetry.io/collector/pipeline/xpipeline v0.156.0 // indirect
	go.opentelemetry.io/collector/processor/processorhelper v0.156.0 // indirect
	go.opentelemetry.io/collector/processor/processorhelper/xprocessorhelper v0.156.0 // indirect
	go.opentelemetry.io/collector/processor/processortest v0.156.0 // indirect
	go.opentelemetry.io/collector/processor/xprocessor v0.156.0 // indirect
	go.opentelemetry.io/collector/receiver/receiverhelper v0.156.0 // indirect
	go.opentelemetry.io/collector/receiver/receivertest v0.156.0 // indirect
	go.opentelemetry.io/collector/receiver/xreceiver v0.156.0 // indirect
	go.opentelemetry.io/collector/service/hostcapabilities v0.156.0 // indirect
	go.opentelemetry.io/contrib/bridges/otelzap v0.19.0 // indirect
	go.opentelemetry.io/contrib/bridges/prometheus v0.69.0 // indirect
	go.opentelemetry.io/contrib/detectors/gcp v1.43.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.69.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace v0.46.1 // indirect
	go.opentelemetry.io/contrib/otelconf v0.24.0 // indirect
	go.opentelemetry.io/contrib/propagators/b3 v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.20.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.20.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutlog v0.20.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.44.0 // indirect
	go.uber.org/atomic v1.11.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.28.0 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.ngrok.com/muxado/v2 v2.0.1 // indirect
	golang.org/x/exp v0.0.0-20260527015227-08cc5374adb3 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/text v0.38.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.org/x/tools v0.46.0 // indirect
	golang.org/x/xerrors v0.0.0-20240903120638-7835f813f4da // indirect
	gonum.org/v1/gonum v0.17.0 // indirect
	google.golang.org/api v0.272.0 // indirect
	google.golang.org/genproto v0.0.0-20260316180232-0b37fe3546d5 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	gopkg.in/evanphx/json-patch.v4 v4.12.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	gotest.tools/v3 v3.5.2 // indirect
	k8s.io/klog/v2 v2.130.1 // indirect
	k8s.io/kube-openapi v0.0.0-20241105132330-32ad38e42d3f // indirect
	sigs.k8s.io/json v0.0.0-20241010143419-9aa6b5e7a4b3 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.4.2 // indirect
)
