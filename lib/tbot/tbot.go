/*
 * Teleport
 * Copyright (C) 2023  Gravitational, Inc.
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package tbot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/gravitational/trace"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/gravitational/teleport"
	apiclient "github.com/gravitational/teleport/api/client"
	"github.com/gravitational/teleport/api/client/proto"
	"github.com/gravitational/teleport/api/client/webclient"
	machineidv1pb "github.com/gravitational/teleport/api/gen/proto/go/teleport/machineid/v1"
	apitracing "github.com/gravitational/teleport/api/observability/tracing"
	"github.com/gravitational/teleport/api/types"
	autoupdate "github.com/gravitational/teleport/lib/autoupdate/agent"
	"github.com/gravitational/teleport/lib/modules"
	"github.com/gravitational/teleport/lib/observability/metrics"
	"github.com/gravitational/teleport/lib/reversetunnelclient"
	"github.com/gravitational/teleport/lib/tbot/client"
	"github.com/gravitational/teleport/lib/tbot/config"
	"github.com/gravitational/teleport/lib/tbot/identity"
	"github.com/gravitational/teleport/lib/tbot/readyz"
	"github.com/gravitational/teleport/lib/tbot/workloadidentity"
	"github.com/gravitational/teleport/lib/utils"
)

var tracer = otel.Tracer("github.com/gravitational/teleport/lib/tbot")

var clientMetrics = metrics.CreateGRPCClientMetrics(
	false,
	prometheus.Labels{},
)

const componentTBot = "tbot"

// Service is a long-running sub-component of tbot.
type Service interface {
	// String returns a human-readable name for the service that can be used
	// in logging. It should identify the type of the service and any top
	// level configuration that could distinguish it from a same-type service.
	String() string
	// Run starts the service and blocks until the service exits. It should
	// return a nil error if the service exits successfully and an error
	// if it is unable to proceed. It should exit gracefully if the context
	// is canceled.
	Run(ctx context.Context) error
}

// OneShotService is a [Service] that offers a mode in which it runs a single
// time and then exits. This aligns with the `--oneshot` mode of tbot.
type OneShotService interface {
	Service
	// OneShot runs the service once and then exits. It should return a nil
	// error if the service exits successfully and an error if it is unable
	// to proceed. It should exit gracefully if the context is canceled.
	OneShot(ctx context.Context) error
}

type Bot struct {
	cfg     *config.BotConfig
	log     *slog.Logger
	modules modules.Modules

	mu             sync.Mutex
	started        bool
	botIdentitySvc *identityService
}

func New(cfg *config.BotConfig, log *slog.Logger) *Bot {
	if log == nil {
		log = slog.Default()
	}

	return &Bot{
		cfg:     cfg,
		log:     log,
		modules: modules.GetModules(),
	}
}

func (b *Bot) markStarted() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.started {
		return trace.BadParameter("bot has already been started")
	}
	b.started = true

	return nil
}

type getBotIdentityFn func() *identity.Identity

// BotIdentity returns the bot's own identity. This will return nil if the bot
// has not been started.
func (b *Bot) BotIdentity() *identity.Identity {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.botIdentitySvc.GetIdentity()
}

// Client returns the bot's API client. This will return nil if the bot has not
// been started.
func (b *Bot) Client() *apiclient.Client {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.botIdentitySvc.GetClient()
}

func (b *Bot) Run(ctx context.Context) (err error) {
	ctx, span := tracer.Start(ctx, "Bot/Run")
	defer func() { apitracing.EndSpan(span, err) }()
	startedAt := time.Now()

	if err := metrics.RegisterPrometheusCollectors(
		metrics.BuildCollector(),
		clientMetrics,
		loopIterationsCounter,
		loopIterationsSuccessCounter,
		loopIterationsFailureCounter,
		loopIterationTime,
	); err != nil {
		return trace.Wrap(err)
	}

	if err := b.markStarted(); err != nil {
		return trace.Wrap(err)
	}
	unlock, err := b.preRunChecks(ctx)
	defer func() {
		b.log.DebugContext(ctx, "Unlocking bot storage.")
		if unlock != nil {
			if err := unlock(); err != nil {
				b.log.WarnContext(
					ctx, "Failed to release lock. Future starts of tbot may fail.", "error", err,
				)
			}
		}
	}()
	if err != nil {
		return trace.Wrap(err)
	}

	addr, addrKind := b.cfg.Address()
	var resolver reversetunnelclient.Resolver
	if shouldUseProxyAddr() {
		if addrKind != config.AddressKindProxy {
			return trace.BadParameter("TBOT_USE_PROXY_ADDR requires that a proxy address is set using --proxy-server or proxy_server")
		}
		// If the user has indicated they want tbot to prefer using the proxy
		// address they have configured, we use a static resolver set to this
		// address. We also assume that they have TLS routing/multiplexing
		// enabled, since otherwise we'd need them to manually configure an
		// an entry for each kind of address.
		resolver = reversetunnelclient.StaticResolver(
			addr, types.ProxyListenerMode_Multiplex,
		)
	} else {
		resolver, err = reversetunnelclient.CachingResolver(
			ctx,
			reversetunnelclient.WebClientResolver(&webclient.Config{
				Context:   ctx,
				ProxyAddr: addr,
				Insecure:  b.cfg.Insecure,
			}),
			nil /* clock */)
		if err != nil {
			return trace.Wrap(err)
		}
	}

	addr, kind := b.cfg.Address()
	clientBuilder, err := client.NewBuilder(client.BuilderConfig{
		Address: client.Address{
			Addr: addr,
			Kind: kind,
		},
		AuthServerAddressMode: b.cfg.AuthServerAddressMode,
		Resolver:              resolver,
		Logger: b.log.With(
			teleport.ComponentKey,
			teleport.Component(componentTBot, "client"),
		),
		Insecure: b.cfg.Insecure,
		Metrics:  clientMetrics,
	})
	if err != nil {
		return trace.Wrap(err)
	}

	// Create an error group to manage all the services lifetimes.
	eg, egCtx := errgroup.WithContext(ctx)
	var services []Service

	// ReloadBroadcaster allows multiple entities to trigger a reload of
	// all services. This allows os signals and other events such as CA
	// rotations to trigger appropriate renewals.
	reloadBroadcaster := &channelBroadcaster{
		chanSet: map[chan struct{}]struct{}{},
	}
	// Trigger reloads from an configured reload channel.
	if b.cfg.ReloadCh != nil {
		// We specifically do not use the error group here as we do not want
		// this goroutine to block the bot from exiting.
		go func() {
			for {
				select {
				case <-egCtx.Done():
					return
				case <-b.cfg.ReloadCh:
					reloadBroadcaster.broadcast()
				}
			}
		}()
	}

	statusRegistry := readyz.NewRegistry()

	b.mu.Lock()
	b.botIdentitySvc = &identityService{
		cfg:               b.cfg,
		reloadBroadcaster: reloadBroadcaster,
		clientBuilder:     clientBuilder,
		log: b.log.With(
			teleport.ComponentKey, teleport.Component(componentTBot, "identity"),
		),
		statusReporter: statusRegistry.AddService("identity"),
	}
	b.mu.Unlock()

	// Initialize bot's own identity. This will load from disk, or fetch a new
	// identity, and perform an initial renewal if necessary.
	if err := b.botIdentitySvc.Initialize(ctx); err != nil {
		return trace.Wrap(err)
	}
	defer func() {
		if err := b.botIdentitySvc.Close(); err != nil {
			b.log.ErrorContext(
				ctx,
				"Failed to close bot identity service",
				"error", err,
			)
		}
	}()
	services = append(services, b.botIdentitySvc)

	identityGenerator, err := b.botIdentitySvc.GetGenerator()
	if err != nil {
		return trace.Wrap(err)
	}

	authPingCache := &authPingCache{
		client: b.botIdentitySvc.GetClient(),
		log:    b.log,
	}
	proxyPingCache := &proxyPingCache{
		authPingCache: authPingCache,
		botCfg:        b.cfg,
		log:           b.log,
	}
	alpnUpgradeCache := &alpnProxyConnUpgradeRequiredCache{
		botCfg: b.cfg,
		log:    b.log,
	}

	// Setup all other services
	if b.cfg.DiagAddr != "" {
		services = append(services, &diagnosticsService{
			diagAddr:       b.cfg.DiagAddr,
			pprofEnabled:   b.cfg.Debug,
			statusRegistry: statusRegistry,
			log: b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "diagnostics"),
			),
		})
	}

	services = append(services, &heartbeatService{
		now:       time.Now,
		botCfg:    b.cfg,
		startedAt: startedAt,
		log: b.log.With(
			teleport.ComponentKey, teleport.Component(componentTBot, "heartbeat"),
		),
		heartbeatSubmitter: machineidv1pb.NewBotInstanceServiceClient(
			b.botIdentitySvc.GetClient().GetConnection(),
		),
		botIdentityReadyCh: b.botIdentitySvc.Ready(),
		interval:           time.Minute * 30,
		retryLimit:         5,
		statusReporter:     statusRegistry.AddService("heartbeat"),
	})

	services = append(services, &caRotationService{
		getBotIdentity:     b.botIdentitySvc.GetIdentity,
		botClient:          b.botIdentitySvc.GetClient(),
		botIdentityReadyCh: b.botIdentitySvc.Ready(),
		log: b.log.With(
			teleport.ComponentKey, teleport.Component(componentTBot, "ca-rotation"),
		),
		reloadBroadcaster: reloadBroadcaster,
		statusReporter:    statusRegistry.AddService("ca-rotation"),
	})

	// We only want to create this service if it's needed by a dependent
	// service.
	var trustBundleCache *workloadidentity.TrustBundleCache
	setupTrustBundleCache := func() (*workloadidentity.TrustBundleCache, error) {
		if trustBundleCache != nil {
			return trustBundleCache, nil
		}

		var err error
		trustBundleCache, err = workloadidentity.NewTrustBundleCache(workloadidentity.TrustBundleCacheConfig{
			FederationClient:   b.botIdentitySvc.GetClient().SPIFFEFederationServiceClient(),
			TrustClient:        b.botIdentitySvc.GetClient().TrustClient(),
			EventsClient:       b.botIdentitySvc.GetClient(),
			ClusterName:        b.botIdentitySvc.GetIdentity().ClusterName,
			BotIdentityReadyCh: b.botIdentitySvc.Ready(),
			Logger: b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "spiffe-trust-bundle-cache"),
			),
			StatusReporter: statusRegistry.AddService("spiffe-trust-bundle-cache"),
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		services = append(services, trustBundleCache)
		return trustBundleCache, nil
	}
	var crlCache *workloadidentity.CRLCache
	setupCRLCache := func() (*workloadidentity.CRLCache, error) {
		if crlCache != nil {
			return crlCache, nil
		}

		var err error
		crlCache, err = workloadidentity.NewCRLCache(workloadidentity.CRLCacheConfig{
			RevocationsClient: b.botIdentitySvc.GetClient().WorkloadIdentityRevocationServiceClient(),
			Logger: b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "crl-cache"),
			),
			StatusReporter: statusRegistry.AddService("crl-cache"),
		})
		if err != nil {
			return nil, trace.Wrap(err)
		}
		services = append(services, crlCache)
		return crlCache, nil
	}

	// Append any services configured by the user
	for _, svcCfg := range b.cfg.Services {
		// Convert the service config into the actual service type.
		switch svcCfg := svcCfg.(type) {
		case *config.SPIFFEWorkloadAPIService:
			b.log.WarnContext(
				ctx,
				"The 'spiffe-workload-api' service is deprecated and will be removed in Teleport V19.0.0. See https://goteleport.com/docs/reference/workload-identity/configuration-resource-migration/ for further information.",
			)
			clientCredential := &config.UnstableClientCredentialOutput{}
			svcIdentity := &ClientCredentialOutputService{
				botAuthClient:      b.botIdentitySvc.GetClient(),
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				botCfg:             b.cfg,
				cfg:                clientCredential,
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				reloadBroadcaster:  reloadBroadcaster,
				identityGenerator:  identityGenerator,
			}
			svcIdentity.log = b.log.With(
				teleport.ComponentKey, teleport.Component(
					componentTBot, "svc", svcIdentity.String(),
				),
			)
			services = append(services, svcIdentity)

			tbCache, err := setupTrustBundleCache()
			if err != nil {
				return trace.Wrap(err)
			}

			svc := &SPIFFEWorkloadAPIService{
				svcIdentity:      clientCredential,
				botCfg:           b.cfg,
				cfg:              svcCfg,
				trustBundleCache: tbCache,
				clientBuilder:    clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.DatabaseTunnelService:
			svc := &DatabaseTunnelService{
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				proxyPingCache:     proxyPingCache,
				botClient:          b.botIdentitySvc.GetClient(),
				botCfg:             b.cfg,
				cfg:                svcCfg,
				identityGenerator:  identityGenerator,
				clientBuilder:      clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.ExampleService:
			services = append(services, &ExampleService{
				cfg: svcCfg,
			})
		case *config.SSHMultiplexerService:
			svc := &SSHMultiplexerService{
				alpnUpgradeCache:   alpnUpgradeCache,
				botAuthClient:      b.botIdentitySvc.GetClient(),
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				botCfg:             b.cfg,
				cfg:                svcCfg,
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				proxyPingCache:     proxyPingCache,
				reloadBroadcaster:  reloadBroadcaster,
				identityGenerator:  identityGenerator,
				clientBuilder:      clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.KubernetesOutput:
			svc := &KubernetesOutputService{
				botAuthClient:      b.botIdentitySvc.GetClient(),
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				botCfg:             b.cfg,
				cfg:                svcCfg,
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				proxyPingCache:     proxyPingCache,
				reloadBroadcaster:  reloadBroadcaster,
				executablePath:     autoupdate.StableExecutable,
				identityGenerator:  identityGenerator,
				clientBuilder:      clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.KubernetesV2Output:
			svc := &KubernetesV2OutputService{
				botAuthClient:      b.botIdentitySvc.GetClient(),
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				botCfg:             b.cfg,
				cfg:                svcCfg,
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				proxyPingCache:     proxyPingCache,
				reloadBroadcaster:  reloadBroadcaster,
				executablePath:     autoupdate.StableExecutable,
				identityGenerator:  identityGenerator,
				clientBuilder:      clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.SPIFFESVIDOutput:
			b.log.WarnContext(
				ctx,
				"The 'spiffe-svid' service is deprecated and will be removed in Teleport V19.0.0. See https://goteleport.com/docs/reference/workload-identity/configuration-resource-migration/ for further information.",
			)
			svc := &SPIFFESVIDOutputService{
				botAuthClient:     b.botIdentitySvc.GetClient(),
				botCfg:            b.cfg,
				cfg:               svcCfg,
				getBotIdentity:    b.botIdentitySvc.GetIdentity,
				identityGenerator: identityGenerator,
				clientBuilder:     clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			if !b.cfg.Oneshot {
				tbCache, err := setupTrustBundleCache()
				if err != nil {
					return trace.Wrap(err)
				}
				svc.trustBundleCache = tbCache
			}
			services = append(services, svc)
		case *config.SSHHostOutput:
			svc := &SSHHostOutputService{
				botAuthClient:      b.botIdentitySvc.GetClient(),
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				botCfg:             b.cfg,
				cfg:                svcCfg,
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				reloadBroadcaster:  reloadBroadcaster,
				identityGenerator:  identityGenerator,
				clientBuilder:      clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.ApplicationOutput:
			svc := &ApplicationOutputService{
				botAuthClient:      b.botIdentitySvc.GetClient(),
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				botCfg:             b.cfg,
				cfg:                svcCfg,
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				reloadBroadcaster:  reloadBroadcaster,
				identityGenerator:  identityGenerator,
				clientBuilder:      clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.DatabaseOutput:
			svc := &DatabaseOutputService{
				botAuthClient:      b.botIdentitySvc.GetClient(),
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				botCfg:             b.cfg,
				cfg:                svcCfg,
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				reloadBroadcaster:  reloadBroadcaster,
				identityGenerator:  identityGenerator,
				clientBuilder:      clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.IdentityOutput:
			svc := &IdentityOutputService{
				botAuthClient:      b.botIdentitySvc.GetClient(),
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				botCfg:             b.cfg,
				cfg:                svcCfg,
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				reloadBroadcaster:  reloadBroadcaster,
				executablePath:     autoupdate.StableExecutable,
				alpnUpgradeCache:   alpnUpgradeCache,
				proxyPingCache:     proxyPingCache,
				identityGenerator:  identityGenerator,
				clientBuilder:      clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.UnstableClientCredentialOutput:
			svc := &ClientCredentialOutputService{
				botAuthClient:      b.botIdentitySvc.GetClient(),
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				botCfg:             b.cfg,
				cfg:                svcCfg,
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				reloadBroadcaster:  reloadBroadcaster,
				identityGenerator:  identityGenerator,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.ApplicationTunnelService:
			svc := &ApplicationTunnelService{
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				proxyPingCache:     proxyPingCache,
				botClient:          b.botIdentitySvc.GetClient(),
				botCfg:             b.cfg,
				cfg:                svcCfg,
				identityGenerator:  identityGenerator,
				clientBuilder:      clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.WorkloadIdentityX509Service:
			svc := &WorkloadIdentityX509Service{
				botAuthClient:     b.botIdentitySvc.GetClient(),
				botCfg:            b.cfg,
				cfg:               svcCfg,
				getBotIdentity:    b.botIdentitySvc.GetIdentity,
				identityGenerator: identityGenerator,
				clientBuilder:     clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			if !b.cfg.Oneshot {
				tbCache, err := setupTrustBundleCache()
				if err != nil {
					return trace.Wrap(err)
				}
				svc.trustBundleCache = tbCache
				crlCache, err := setupCRLCache()
				if err != nil {
					return trace.Wrap(err)
				}
				svc.crlCache = crlCache
			}
			services = append(services, svc)
		case *config.WorkloadIdentityJWTService:
			svc := &WorkloadIdentityJWTService{
				botAuthClient:     b.botIdentitySvc.GetClient(),
				botCfg:            b.cfg,
				cfg:               svcCfg,
				getBotIdentity:    b.botIdentitySvc.GetIdentity,
				identityGenerator: identityGenerator,
				clientBuilder:     clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			if !b.cfg.Oneshot {
				tbCache, err := setupTrustBundleCache()
				if err != nil {
					return trace.Wrap(err)
				}
				svc.trustBundleCache = tbCache
			}
			services = append(services, svc)
		case *config.WorkloadIdentityAPIService:
			clientCredential := &config.UnstableClientCredentialOutput{}
			svcIdentity := &ClientCredentialOutputService{
				botAuthClient:      b.botIdentitySvc.GetClient(),
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				botCfg:             b.cfg,
				cfg:                clientCredential,
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				reloadBroadcaster:  reloadBroadcaster,
				identityGenerator:  identityGenerator,
			}
			svcIdentity.log = b.log.With(
				teleport.ComponentKey, teleport.Component(
					componentTBot, "svc", svcIdentity.String(),
				),
			)
			services = append(services, svcIdentity)

			tbCache, err := setupTrustBundleCache()
			if err != nil {
				return trace.Wrap(err)
			}
			crlCache, err := setupCRLCache()
			if err != nil {
				return trace.Wrap(err)
			}

			svc := &WorkloadIdentityAPIService{
				svcIdentity:      clientCredential,
				botCfg:           b.cfg,
				cfg:              svcCfg,
				trustBundleCache: tbCache,
				crlCache:         crlCache,
				clientBuilder:    clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		case *config.WorkloadIdentityAWSRAService:
			svc := &WorkloadIdentityAWSRAService{
				botCfg:             b.cfg,
				cfg:                svcCfg,
				botAuthClient:      b.botIdentitySvc.GetClient(),
				botIdentityReadyCh: b.botIdentitySvc.Ready(),
				getBotIdentity:     b.botIdentitySvc.GetIdentity,
				reloadBroadcaster:  reloadBroadcaster,
				identityGenerator:  identityGenerator,
				clientBuilder:      clientBuilder,
			}
			svc.log = b.log.With(
				teleport.ComponentKey, teleport.Component(componentTBot, "svc", svc.String()),
			)
			svc.statusReporter = statusRegistry.AddService(svc.String())
			services = append(services, svc)
		default:
			return trace.BadParameter("unknown service type: %T", svcCfg)
		}
	}

	b.log.InfoContext(ctx, "Initialization complete. Starting services")
	// Start services
	for _, svc := range services {
		log := b.log.With("service", svc.String())

		if b.cfg.Oneshot {
			svc, ok := svc.(OneShotService)
			// We ignore services with no one-shot implementation
			if !ok {
				log.InfoContext(
					ctx,
					"Service does not support oneshot mode, it will not run",
				)
				continue
			}
			eg.Go(func() error {
				log.InfoContext(ctx, "Running service in oneshot mode")
				err := svc.OneShot(egCtx)
				if err != nil {
					log.ErrorContext(
						egCtx, "Service exited with error", "error", err,
					)
					return trace.Wrap(err, "service(%s)", svc.String())
				}
				log.InfoContext(ctx, "Service finished")
				return nil
			})
		} else {
			eg.Go(func() error {
				log.InfoContext(ctx, "Starting service")
				err := svc.Run(egCtx)
				if err != nil {
					log.ErrorContext(
						egCtx, "Service exited with error", "error", err,
					)
					return trace.Wrap(err, "service(%s)", svc.String())
				}
				log.InfoContext(ctx, "Service exited")
				return nil
			})
		}
	}

	return eg.Wait()
}

// preRunChecks returns an unlock function which must be deferred.
// It performs any initial validation and locks the bot's storage before any
// more expensive initialization is performed.
func (b *Bot) preRunChecks(ctx context.Context) (_ func() error, err error) {
	ctx, span := tracer.Start(ctx, "Bot/preRunChecks")
	defer func() { apitracing.EndSpan(span, err) }()

	if b.cfg.JoinURI != "" {
		parsed, err := config.ParseJoinURI(b.cfg.JoinURI)
		if err != nil {
			return nil, trace.Wrap(err, "parsing joining URI")
		}

		if err := parsed.ApplyToConfig(b.cfg); err != nil {
			return nil, trace.Wrap(err, "applying joining URI to bot config")
		}
	}

	_, addrKind := b.cfg.Address()
	switch addrKind {
	case config.AddressKindUnspecified:
		return nil, trace.BadParameter(
			"either a proxy or auth address must be set using --proxy-server, --auth-server or configuration",
		)
	}

	// Ensure they have provided a join method.
	if b.cfg.Onboarding.JoinMethod == types.JoinMethodUnspecified {
		return nil, trace.BadParameter("join method must be provided")
	}

	if b.cfg.FIPS {
		if !b.modules.IsBoringBinary() {
			b.log.ErrorContext(ctx, "FIPS mode enabled but FIPS compatible binary not in use. Ensure you are using the Enterprise FIPS binary to use this flag.")
			return nil, trace.BadParameter("fips mode enabled but binary was not compiled with boringcrypto")
		}
		b.log.InfoContext(ctx, "Bot is running in FIPS compliant mode.")
	}

	// First, try to make sure all destinations are usable.
	if err := checkDestinations(ctx, b.cfg); err != nil {
		return nil, trace.Wrap(err)
	}

	// Start by loading the bot's primary storage.
	store := b.cfg.Storage.Destination
	if err := identity.VerifyWrite(ctx, store); err != nil {
		return nil, trace.Wrap(
			err, "Could not write to destination %s, aborting", store,
		)
	}

	// Now attempt to lock the destination so we have sole use of it
	unlock, err := store.TryLock()
	if err != nil {
		if errors.Is(err, utils.ErrUnsuccessfulLockTry) {
			return unlock, trace.Wrap(
				err,
				"Failed to acquire exclusive lock for tbot destination directory - is tbot already running?",
			)
		}
		return unlock, trace.Wrap(err)
	}

	if !store.IsPersistent() {
		b.log.WarnContext(
			ctx,
			"Bot is configured with a non-persistent storage destination. If the bot is running in a non-ephemeral environment, this will impact the ability to provide a long-lived bot instance identity",
		)
	}

	return unlock, nil
}

// checkDestinations checks all destinations and tries to create any that
// don't already exist.
func checkDestinations(ctx context.Context, cfg *config.BotConfig) error {
	// Note: This is vaguely problematic as we don't recommend that users
	// store renewable certs under the same user as end-user certs. That said,
	//  - if the destination was properly created via tbot init this is a no-op
	//  - if users intend to follow that advice but miss a step, it should fail
	//    due to lack of permissions
	storageDest := cfg.Storage.Destination

	// Note: no subdirs to init for bot's internal storage.
	if err := storageDest.Init(ctx, []string{}); err != nil {
		return trace.Wrap(err)
	}

	// TODO: consider warning if ownership of all destinations is not expected.
	for _, initable := range cfg.GetInitables() {
		if err := initable.Init(ctx); err != nil {
			return trace.Wrap(err)
		}
	}

	return nil
}

type authPingCache struct {
	client *apiclient.Client
	log    *slog.Logger

	mu          sync.RWMutex
	cachedValue *proto.PingResponse
}

func (a *authPingCache) ping(ctx context.Context) (proto.PingResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cachedValue != nil {
		return *a.cachedValue, nil
	}

	a.log.DebugContext(ctx, "Pinging auth server.")
	res, err := a.client.Ping(ctx)
	if err != nil {
		a.log.ErrorContext(ctx, "Failed to ping auth server.", "error", err)
		return proto.PingResponse{}, trace.Wrap(err)
	}
	a.cachedValue = &res
	a.log.DebugContext(ctx, "Successfully pinged auth server.", "pong", res)

	return *a.cachedValue, nil
}

type proxyPingCache struct {
	authPingCache *authPingCache
	botCfg        *config.BotConfig
	log           *slog.Logger

	mu          sync.RWMutex
	cachedValue *proxyPingResponse
}

func (p *proxyPingCache) ping(ctx context.Context) (*proxyPingResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cachedValue != nil {
		return p.cachedValue, nil
	}

	// Determine the Proxy address to use.
	addr, addrKind := p.botCfg.Address()
	switch addrKind {
	case config.AddressKindAuth:
		// If the address is an auth address, ping auth to determine proxy addr.
		authPong, err := p.authPingCache.ping(ctx)
		if err != nil {
			return nil, trace.Wrap(err)
		}
		addr = authPong.ProxyPublicAddr
	case config.AddressKindProxy:
		// If the address is a proxy address, use it directly.
	default:
		return nil, trace.BadParameter("unsupported address kind: %v", addrKind)
	}

	p.log.DebugContext(ctx, "Pinging proxy.", "addr", addr)
	res, err := webclient.Find(&webclient.Config{
		Context:   ctx,
		ProxyAddr: addr,
		Insecure:  p.botCfg.Insecure,
	})
	if err != nil {
		p.log.ErrorContext(ctx, "Failed to ping proxy.", "error", err)
		return nil, trace.Wrap(err)
	}
	p.log.DebugContext(ctx, "Successfully pinged proxy.", "pong", res)
	p.cachedValue = &proxyPingResponse{
		PingResponse:        res,
		configuredProxyAddr: p.botCfg.ProxyServer,
	}

	return p.cachedValue, nil
}

type proxyPingResponse struct {
	*webclient.PingResponse
	configuredProxyAddr string
}

// useProxyAddrEnv is an environment variable which can be set to
// force `tbot` to prefer using the proxy address explicitly provided by the
// user over the one fetched from the proxy ping. This is only intended to work
// in cases where TLS routing is enabled, and is intended to support cases where
// the Proxy is accessible from multiple addresses, and the one included in the
// ProxyPing is incorrect.
const useProxyAddrEnv = "TBOT_USE_PROXY_ADDR"

// shouldUseProxyAddr returns true if the TBOT_USE_PROXY_ADDR environment
// variable is set to "yes". More generally, this indicates that the user wishes
// for tbot to prefer using the proxy address that has been explicitly provided
// by the user rather than the one fetched via a discovery process (e.g ping).
func shouldUseProxyAddr() bool {
	return os.Getenv(useProxyAddrEnv) == "yes"
}

// proxyWebAddr returns the address to use to connect to the proxy web port.
// In TLS routing mode, this address should be used for most/all connections.
// This function takes into account the TBOT_USE_PROXY_ADDR environment
// variable, which can be used to force the use of the proxy address explicitly
// provided by the user rather than use the one fetched from the proxy ping.
func (p *proxyPingResponse) proxyWebAddr() (string, error) {
	if shouldUseProxyAddr() {
		if p.configuredProxyAddr == "" {
			return "", trace.BadParameter("TBOT_USE_PROXY_ADDR set but no explicit proxy address configured")
		}
		return p.configuredProxyAddr, nil
	}
	return p.Proxy.SSH.PublicAddr, nil
}

// proxySSHAddr returns the address to use to connect to the proxy SSH service.
// Includes potential override via TBOT_USE_PROXY_ADDR.
func (p *proxyPingResponse) proxySSHAddr() (string, error) {
	if p.Proxy.TLSRoutingEnabled && shouldUseProxyAddr() {
		// If using TLS routing, we should use the manually overridden address
		// for the proxy web port.
		if p.configuredProxyAddr == "" {
			return "", trace.BadParameter("TBOT_USE_PROXY_ADDR set but no explicit proxy address configured")
		}
		return p.configuredProxyAddr, nil
	}
	// SSHProxyHostPort returns the host and port to use to connect to the
	// proxy's SSH service. If TLS routing is enabled, this will return the
	// proxy's web address, if not, the proxy SSH listener.
	host, port, err := p.Proxy.SSHProxyHostPort()
	if err != nil {
		return "", trace.Wrap(err)
	}
	return net.JoinHostPort(host, port), nil
}

type alpnProxyConnUpgradeRequiredCache struct {
	botCfg *config.BotConfig
	log    *slog.Logger

	mu    sync.Mutex
	cache map[string]bool
	group singleflight.Group
}

func (a *alpnProxyConnUpgradeRequiredCache) isUpgradeRequired(ctx context.Context, addr string, insecure bool) (bool, error) {
	key := fmt.Sprintf("%s-%t", addr, insecure)

	a.mu.Lock()
	if a.cache == nil {
		a.cache = make(map[string]bool)
	}
	v, ok := a.cache[key]
	if ok {
		a.mu.Unlock()
		return v, nil
	}
	a.mu.Unlock()

	val, err, _ := a.group.Do(key, func() (any, error) {
		// Recheck the cache in case we've just missed a previous group
		// completing
		a.mu.Lock()
		v, ok := a.cache[key]
		if ok {
			a.mu.Unlock()
			return v, nil
		}
		a.mu.Unlock()

		// Ok, now we know for sure that the work hasn't already been done or
		// isn't in flight, we can complete it.
		a.log.DebugContext(ctx, "Testing ALPN upgrade necessary", "addr", addr, "insecure", insecure)
		v = apiclient.IsALPNConnUpgradeRequired(ctx, addr, insecure)
		a.log.DebugContext(ctx, "Tested ALPN upgrade necessary", "addr", addr, "insecure", insecure, "result", v)
		if err := ctx.Err(); err != nil {
			// Check for case where false is returned because client canceled ctx.
			// We don't want to cache this result.
			return v, trace.Wrap(err)
		}

		a.mu.Lock()
		a.cache[key] = v
		a.mu.Unlock()
		return v, nil
	})
	return val.(bool), err
}
