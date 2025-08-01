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

package auth_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/digitorus/pkcs7"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/google/uuid"
	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/testing/protocmp"

	"github.com/gravitational/teleport"
	apiclient "github.com/gravitational/teleport/api/client"
	"github.com/gravitational/teleport/api/client/proto"
	"github.com/gravitational/teleport/api/client/webclient"
	headerv1 "github.com/gravitational/teleport/api/gen/proto/go/teleport/header/v1"
	machineidv1pb "github.com/gravitational/teleport/api/gen/proto/go/teleport/machineid/v1"
	workloadidentityv1pb "github.com/gravitational/teleport/api/gen/proto/go/teleport/workloadidentity/v1"
	"github.com/gravitational/teleport/api/metadata"
	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/api/types/events"
	"github.com/gravitational/teleport/api/utils/keys"
	"github.com/gravitational/teleport/integrations/lib/testing/fakejoin"
	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/auth/authclient"
	"github.com/gravitational/teleport/lib/auth/authtest"
	"github.com/gravitational/teleport/lib/auth/join"
	"github.com/gravitational/teleport/lib/auth/machineid/machineidv1"
	"github.com/gravitational/teleport/lib/auth/state"
	"github.com/gravitational/teleport/lib/auth/testauthority"
	"github.com/gravitational/teleport/lib/cloud/azure"
	libevents "github.com/gravitational/teleport/lib/events"
	"github.com/gravitational/teleport/lib/events/eventstest"
	"github.com/gravitational/teleport/lib/fixtures"
	"github.com/gravitational/teleport/lib/kube/token"
	"github.com/gravitational/teleport/lib/reversetunnelclient"
	"github.com/gravitational/teleport/lib/tbot/identity"
	"github.com/gravitational/teleport/lib/tlsca"
	"github.com/gravitational/teleport/lib/utils"
	"github.com/gravitational/teleport/lib/utils/log/logtest"
)

func renewBotCerts(
	ctx context.Context,
	srv *authtest.TLSServer,
	tlsCertPEM []byte,
	botUser string,
	key crypto.Signer,
) (*authclient.Client, *proto.Certs, error) {
	fakeClock := srv.Clock().(*clockwork.FakeClock)

	privateKeyPEM, err := keys.MarshalPrivateKey(key)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	tlsCert, err := tls.X509KeyPair(tlsCertPEM, privateKeyPEM)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	client, err := srv.NewClientWithCert(tlsCert)
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}

	sshPub, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	tlsPub, err := keys.MarshalPublicKey(key.Public())
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	certs, err := client.GenerateUserCerts(ctx, proto.UserCertsRequest{
		SSHPublicKey: ssh.MarshalAuthorizedKey(sshPub),
		TLSPublicKey: tlsPub,
		Username:     botUser,
		Expires:      fakeClock.Now().Add(time.Hour),
	})
	if err != nil {
		return nil, nil, trace.Wrap(err)
	}
	return client, certs, nil
}

// TestRegisterBotCertificateGenerationCheck ensures bot cert generation checks
// work in ordinary conditions, with several rapid renewals.
func TestRegisterBotCertificateGenerationCheck(t *testing.T) {
	t.Parallel()

	srv := newTestTLSServer(t)
	ctx := context.Background()
	fakeClock := srv.Clock().(*clockwork.FakeClock)

	_, err := authtest.CreateRole(ctx, srv.Auth(), "example", types.RoleSpecV6{})
	require.NoError(t, err)

	// Create a new bot.
	client, err := srv.NewClient(authtest.TestAdmin())
	require.NoError(t, err)
	bot, err := client.BotServiceClient().CreateBot(ctx, &machineidv1pb.CreateBotRequest{
		Bot: &machineidv1pb.Bot{
			Kind:    types.KindBot,
			Version: types.V1,
			Metadata: &headerv1.Metadata{
				Name: "test",
			},
			Spec: &machineidv1pb.BotSpec{
				Roles: []string{"example"},
			},
		},
	})
	require.NoError(t, err)

	token, err := types.NewProvisionTokenFromSpec("testxyzzy", time.Time{}, types.ProvisionTokenSpecV2{
		Roles:   types.SystemRoles{types.RoleBot},
		BotName: bot.Metadata.Name,
	})
	require.NoError(t, err)
	require.NoError(t, client.CreateToken(ctx, token))

	result, err := join.Register(ctx, join.RegisterParams{
		Token: token.GetName(),
		ID: state.IdentityID{
			Role: types.RoleBot,
		},
		AuthServers: []utils.NetAddr{*utils.MustParseAddr(srv.Addr().String())},
	})
	require.NoError(t, err)
	checkCertLoginIP(t, result.Certs.TLS, "127.0.0.1")

	initialCert, err := tlsca.ParseCertificatePEM(result.Certs.TLS)
	require.NoError(t, err)
	initialIdent, err := tlsca.FromSubject(initialCert.Subject, initialCert.NotAfter)
	require.NoError(t, err)

	require.Equal(t, uint64(1), initialIdent.Generation)
	require.Equal(t, "test", initialIdent.BotName)
	require.NotEmpty(t, initialIdent.BotInstanceID)

	certs := result.Certs

	// Renew the cert a bunch of times.
	for i := range 10 {
		// Ensure the state of the bot instance before renewal is sane.
		bi, err := srv.Auth().BotInstance.GetBotInstance(ctx, initialIdent.BotName, initialIdent.BotInstanceID)
		require.NoError(t, err)

		// There should always be at least 1 entry as the initial join is
		// duplicated in the list.
		require.Len(t, bi.Status.LatestAuthentications, min(i+1, machineidv1.AuthenticationHistoryLimit))

		// Generation starts at 1 for initial certs.
		latest := bi.Status.LatestAuthentications[len(bi.Status.LatestAuthentications)-1]
		require.Equal(t, int32(i+1), latest.Generation)

		lastExpires := bi.Metadata.Expires.AsTime()

		// Advance the clock a bit.
		fakeClock.Advance(time.Minute)

		_, certs, err = renewBotCerts(ctx, srv, certs.TLS, bot.Status.UserName, result.PrivateKey)
		require.NoError(t, err)

		// Parse the Identity
		renewedCert, err := tlsca.ParseCertificatePEM(certs.TLS)
		require.NoError(t, err)
		renewedIdent, err := tlsca.FromSubject(renewedCert.Subject, renewedCert.NotAfter)
		require.NoError(t, err)

		// Validate that we receive 2 TLS CAs (Host and User)
		require.Len(t, certs.TLSCACerts, 2)

		// Cert must be renewable.
		require.True(t, renewedIdent.Renewable)
		require.False(t, renewedIdent.DisallowReissue)

		// Initial certs have generation=1 and we start the loop with a renewal, so add 2
		require.Equal(t, uint64(i+2), renewedIdent.Generation)

		// Ensure the bot instance after renewal is sane.
		bi, err = srv.Auth().BotInstance.GetBotInstance(ctx, initialIdent.BotName, initialIdent.BotInstanceID)
		require.NoError(t, err)

		require.Len(t, bi.Status.LatestAuthentications, min(i+2, machineidv1.AuthenticationHistoryLimit))

		latest = bi.Status.LatestAuthentications[len(bi.Status.LatestAuthentications)-1]
		require.Equal(t, int32(i+2), latest.Generation)

		require.True(t, bi.Metadata.Expires.AsTime().After(lastExpires), "Metadata.Expires must be extended")
	}
}

// TestBotJoinAttrs_Kubernetes validates that a bot can join using the
// Kubernetes join method and that the correct join attributes are encoded in
// the resulting bot cert, and, that when this cert is used to produce role
// certificates, the correct attributes are encoded in the role cert.
//
// Whilst this specifically tests the Kubernetes join method, it tests by proxy
// the implementation for most of the join methods.
func TestBotJoinAttrs_Kubernetes(t *testing.T) {
	t.Parallel()

	srv := newTestTLSServer(t)
	ctx := context.Background()

	role, err := authtest.CreateRole(ctx, srv.Auth(), "example", types.RoleSpecV6{})
	require.NoError(t, err)

	// Create a new bot.
	client, err := srv.NewClient(authtest.TestAdmin())
	require.NoError(t, err)
	bot, err := client.BotServiceClient().CreateBot(ctx, &machineidv1pb.CreateBotRequest{
		Bot: &machineidv1pb.Bot{
			Kind:    types.KindBot,
			Version: types.V1,
			Metadata: &headerv1.Metadata{
				Name: "test",
			},
			Spec: &machineidv1pb.BotSpec{
				Roles: []string{"example"},
			},
		},
	})
	require.NoError(t, err)

	k8s, err := fakejoin.NewKubernetesSigner(srv.Clock())
	require.NoError(t, err)
	jwks, err := k8s.GetMarshaledJWKS()
	require.NoError(t, err)
	fakePSAT, err := k8s.SignServiceAccountJWT(
		"my-pod",
		"my-namespace",
		"my-service-account",
		srv.ClusterName(),
	)
	require.NoError(t, err)

	tok, err := types.NewProvisionTokenFromSpec(
		"my-k8s-token",
		time.Time{},
		types.ProvisionTokenSpecV2{
			Roles:      types.SystemRoles{types.RoleBot},
			JoinMethod: types.JoinMethodKubernetes,
			BotName:    bot.Metadata.Name,
			Kubernetes: &types.ProvisionTokenSpecV2Kubernetes{
				Type: types.KubernetesJoinTypeStaticJWKS,
				StaticJWKS: &types.ProvisionTokenSpecV2Kubernetes_StaticJWKSConfig{
					JWKS: jwks,
				},
				Allow: []*types.ProvisionTokenSpecV2Kubernetes_Rule{
					{
						ServiceAccount: "my-namespace:my-service-account",
					},
				},
			},
		},
	)
	require.NoError(t, err)
	require.NoError(t, client.CreateToken(ctx, tok))

	result, err := join.Register(ctx, join.RegisterParams{
		Token:      tok.GetName(),
		JoinMethod: types.JoinMethodKubernetes,
		ID: state.IdentityID{
			Role: types.RoleBot,
		},
		AuthServers: []utils.NetAddr{*utils.MustParseAddr(srv.Addr().String())},
		KubernetesReadFileFunc: func(name string) ([]byte, error) {
			return []byte(fakePSAT), nil
		},
	})
	require.NoError(t, err)

	// Validate correct join attributes are encoded.
	cert, err := tlsca.ParseCertificatePEM(result.Certs.TLS)
	require.NoError(t, err)
	ident, err := tlsca.FromSubject(cert.Subject, cert.NotAfter)
	require.NoError(t, err)
	wantAttrs := &workloadidentityv1pb.JoinAttrs{
		Meta: &workloadidentityv1pb.JoinAttrsMeta{
			JoinTokenName: tok.GetName(),
			JoinMethod:    string(types.JoinMethodKubernetes),
		},
		Kubernetes: &workloadidentityv1pb.JoinAttrsKubernetes{
			ServiceAccount: &workloadidentityv1pb.JoinAttrsKubernetesServiceAccount{
				Namespace: "my-namespace",
				Name:      "my-service-account",
			},
			Pod: &workloadidentityv1pb.JoinAttrsKubernetesPod{
				Name: "my-pod",
			},
			Subject: "system:serviceaccount:my-namespace:my-service-account",
		},
	}
	require.Empty(t, cmp.Diff(
		ident.JoinAttributes,
		wantAttrs,
		protocmp.Transform(),
	))

	// Now, try to produce a role certificate using the bot cert, to ensure
	// that the join attributes are correctly propagated.
	privateKeyPEM, err := keys.MarshalPrivateKey(result.PrivateKey)
	require.NoError(t, err)
	tlsCert, err := tls.X509KeyPair(result.Certs.TLS, privateKeyPEM)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(result.PrivateKey.Public())
	require.NoError(t, err)
	tlsPub, err := keys.MarshalPublicKey(result.PrivateKey.Public())
	require.NoError(t, err)
	botClient, err := srv.NewClientWithCert(tlsCert)
	require.NoError(t, err)
	roleCerts, err := botClient.GenerateUserCerts(ctx, proto.UserCertsRequest{
		SSHPublicKey: ssh.MarshalAuthorizedKey(sshPub),
		TLSPublicKey: tlsPub,
		Username:     bot.Status.UserName,
		RoleRequests: []string{
			role.GetName(),
		},
		UseRoleRequests: true,
		Expires:         srv.Clock().Now().Add(time.Hour),
	})
	require.NoError(t, err)

	roleCert, err := tlsca.ParseCertificatePEM(roleCerts.TLS)
	require.NoError(t, err)
	roleIdent, err := tlsca.FromSubject(roleCert.Subject, roleCert.NotAfter)
	require.NoError(t, err)
	require.Empty(t, cmp.Diff(
		roleIdent.JoinAttributes,
		wantAttrs,
		protocmp.Transform(),
	))
}

// TestRegisterBotInstance tests that bot instances are created properly on join
func TestRegisterBotInstance(t *testing.T) {
	t.Parallel()

	srv := newTestTLSServer(t)
	// Inject mockEmitter to capture audit events
	mockEmitter := &eventstest.MockRecorderEmitter{}
	srv.Auth().SetEmitter(mockEmitter)
	ctx := context.Background()

	_, err := authtest.CreateRole(ctx, srv.Auth(), "example", types.RoleSpecV6{})
	require.NoError(t, err)

	// Create a new bot.
	client, err := srv.NewClient(authtest.TestAdmin())
	require.NoError(t, err)
	bot, err := client.BotServiceClient().CreateBot(ctx, &machineidv1pb.CreateBotRequest{
		Bot: &machineidv1pb.Bot{
			Kind:    types.KindBot,
			Version: types.V1,
			Metadata: &headerv1.Metadata{
				Name: "test",
			},
			Spec: &machineidv1pb.BotSpec{
				Roles: []string{"example"},
			},
		},
	})
	require.NoError(t, err)

	token, err := types.NewProvisionTokenFromSpec("testxyzzy", time.Time{}, types.ProvisionTokenSpecV2{
		Roles:   types.SystemRoles{types.RoleBot},
		BotName: bot.Metadata.Name,
	})
	require.NoError(t, err)
	require.NoError(t, client.CreateToken(ctx, token))

	result, err := join.Register(ctx, join.RegisterParams{
		Token: token.GetName(),
		ID: state.IdentityID{
			Role: types.RoleBot,
		},
		AuthServers: []utils.NetAddr{*utils.MustParseAddr(srv.Addr().String())},
	})
	require.NoError(t, err)

	// The returned certs should have a bot instance ID.
	cert, err := tlsca.ParseCertificatePEM(result.Certs.TLS)
	require.NoError(t, err)

	ident, err := tlsca.FromSubject(cert.Subject, cert.NotAfter)
	require.NoError(t, err)

	require.NotEmpty(t, ident.BotInstanceID)

	// The instance ID should match a bot instance record.
	botInstance, err := srv.Auth().BotInstance.GetBotInstance(ctx, ident.BotName, ident.BotInstanceID)
	require.NoError(t, err)

	require.Equal(t, ident.BotName, botInstance.GetSpec().BotName)
	require.Equal(t, ident.BotInstanceID, botInstance.GetSpec().InstanceId)

	// The initial authentication record should be sane
	ia := botInstance.GetStatus().InitialAuthentication
	require.NotNil(t, ia)
	require.Equal(t, int32(1), ia.Generation)
	require.Equal(t, string(types.JoinMethodToken), ia.JoinMethod)
	require.Equal(t, token.GetSafeName(), ia.JoinToken)
	// The latest authentications field should contain the same record (and
	// only that record.)
	require.Len(t, botInstance.GetStatus().LatestAuthentications, 1)
	require.EqualExportedValues(t, ia, botInstance.GetStatus().LatestAuthentications[0])

	// Validate that expected audit events were emitted...
	auditEvents := mockEmitter.Events()
	var joinEvent *events.BotJoin
	for _, event := range auditEvents {
		evt, ok := event.(*events.BotJoin)
		if ok {
			joinEvent = evt
			break
		}
	}
	require.NotNil(t, joinEvent)
	require.Empty(t,
		cmp.Diff(joinEvent, &events.BotJoin{
			Metadata: events.Metadata{
				Type: libevents.BotJoinEvent,
				Code: libevents.BotJoinCode,
			},
			Status: events.Status{
				Success: true,
			},
			UserName:  "bot-test",
			BotName:   "test",
			Method:    string(types.JoinMethodToken),
			TokenName: token.GetSafeName(),
			ConnectionMetadata: events.ConnectionMetadata{
				RemoteAddr: "127.0.0.1",
			},
			BotInstanceID: ident.BotInstanceID,
		},
			// There appears to be a bug with cmp.Diff and nil event.Struct that
			// causes a panic so let's just ignore it.
			cmpopts.IgnoreFields(events.BotJoin{}, "Attributes"),
			cmpopts.IgnoreFields(events.Metadata{}, "Time"),
			cmpopts.EquateEmpty(),
		),
	)

	var certIssueEvent *events.CertificateCreate
	for _, event := range auditEvents {
		evt, ok := event.(*events.CertificateCreate)
		if ok {
			certIssueEvent = evt
			break
		}
	}
	require.NotNil(t, certIssueEvent)
	require.Empty(t,
		cmp.Diff(certIssueEvent, &events.CertificateCreate{
			Metadata: events.Metadata{
				Type: libevents.CertificateCreateEvent,
				Code: libevents.CertificateCreateCode,
			},
			CertificateType: "user",
			Identity: &events.Identity{
				User:             "bot-test",
				Roles:            []string{"bot-test"},
				RouteToCluster:   "localhost",
				ClientIP:         "127.0.0.1",
				TeleportCluster:  "localhost",
				PrivateKeyPolicy: "none",
				BotName:          "test",
				BotInstanceID:    ident.BotInstanceID,
			},
			CertificateAuthority: &events.CertificateAuthority{
				Type:   "user",
				Domain: "localhost",
			},
		},
			cmpopts.IgnoreFields(events.Metadata{}, "Time"),
			cmpopts.IgnoreFields(events.Identity{}, "Logins", "Expires"),
			cmpopts.IgnoreFields(events.ClientMetadata{}, "UserAgent"),
			cmpopts.IgnoreFields(events.CertificateAuthority{}, "SubjectKeyID"),
			cmpopts.EquateEmpty(),
		),
	)
}

// TestRegisterBotCertificateGenerationStolen simulates a stolen renewable
// certificate where a generation check is expected to fail.
func TestRegisterBotCertificateGenerationStolen(t *testing.T) {
	t.Parallel()
	srv := newTestTLSServer(t)
	ctx := context.Background()
	_, err := authtest.CreateRole(ctx, srv.Auth(), "example", types.RoleSpecV6{})
	require.NoError(t, err)

	// Create a new bot.
	client, err := srv.NewClient(authtest.TestAdmin())
	require.NoError(t, err)
	bot, err := client.BotServiceClient().CreateBot(ctx, &machineidv1pb.CreateBotRequest{
		Bot: &machineidv1pb.Bot{
			Kind:    types.KindBot,
			Version: types.V1,
			Metadata: &headerv1.Metadata{
				Name: "test",
			},
			Spec: &machineidv1pb.BotSpec{
				Roles: []string{"example"},
			},
		},
	})
	require.NoError(t, err)

	token, err := types.NewProvisionTokenFromSpec("testxyzzy", time.Time{}, types.ProvisionTokenSpecV2{
		Roles:   types.SystemRoles{types.RoleBot},
		BotName: bot.Metadata.Name,
	})
	require.NoError(t, err)
	require.NoError(t, client.CreateToken(ctx, token))

	result, err := join.Register(ctx, join.RegisterParams{
		Token: token.GetName(),
		ID: state.IdentityID{
			Role: types.RoleBot,
		},
		AuthServers: []utils.NetAddr{*utils.MustParseAddr(srv.Addr().String())},
	})
	require.NoError(t, err)

	// Renew the certs once (e.g. this is the actual bot process)
	renewedClient, certsReal, err := renewBotCerts(ctx, srv, result.Certs.TLS, bot.Status.UserName, result.PrivateKey)
	require.NoError(t, err)

	// This client should be able to ping.
	_, err = renewedClient.Ping(ctx)
	require.NoError(t, err)

	// Check the generation, it should be 2.
	impersonatedTLSCert, err := tlsca.ParseCertificatePEM(certsReal.TLS)
	require.NoError(t, err)
	impersonatedIdent, err := tlsca.FromSubject(impersonatedTLSCert.Subject, impersonatedTLSCert.NotAfter)
	require.NoError(t, err)
	require.Equal(t, uint64(2), impersonatedIdent.Generation)

	// Meanwhile, the initial set of certs was stolen. Let's try to renew those.
	_, _, err = renewBotCerts(ctx, srv, result.Certs.TLS, bot.Status.UserName, result.PrivateKey)
	require.Error(t, err)
	require.True(t, trace.IsAccessDenied(err))

	// The bot instance should now be locked.
	locks, err := srv.Auth().GetLocks(ctx, true, types.LockTarget{
		BotInstanceID: impersonatedIdent.BotInstanceID,
	})
	require.NoError(t, err)
	require.NotEmpty(t, locks)

	// The original client should be locked out.
	require.Eventually(t, func() bool {
		_, err = renewedClient.Ping(ctx)
		return err != nil && strings.Contains(err.Error(), "access denied")
	}, 5*time.Second, 100*time.Millisecond)
}

// TestRegisterBotCertificateExtensions ensures bot cert extensions are present.
func TestRegisterBotCertificateExtensions(t *testing.T) {
	t.Parallel()
	srv := newTestTLSServer(t)
	ctx := context.Background()

	_, err := authtest.CreateRole(ctx, srv.Auth(), "example", types.RoleSpecV6{})
	require.NoError(t, err)

	// Create a new bot.
	client, err := srv.NewClient(authtest.TestAdmin())
	require.NoError(t, err)
	bot, err := client.BotServiceClient().CreateBot(ctx, &machineidv1pb.CreateBotRequest{
		Bot: &machineidv1pb.Bot{
			Kind:    types.KindBot,
			Version: types.V1,
			Metadata: &headerv1.Metadata{
				Name: "test",
			},
			Spec: &machineidv1pb.BotSpec{
				Roles: []string{"example"},
			},
		},
	})
	require.NoError(t, err)

	token, err := types.NewProvisionTokenFromSpec("testxyzzy", time.Time{}, types.ProvisionTokenSpecV2{
		Roles:   types.SystemRoles{types.RoleBot},
		BotName: bot.Metadata.Name,
	})
	require.NoError(t, err)
	require.NoError(t, client.CreateToken(ctx, token))

	result, err := join.Register(ctx, join.RegisterParams{
		Token: token.GetName(),
		ID: state.IdentityID{
			Role: types.RoleBot,
		},
		AuthServers: []utils.NetAddr{*utils.MustParseAddr(srv.Addr().String())},
	})
	require.NoError(t, err)
	checkCertLoginIP(t, result.Certs.TLS, "127.0.0.1")

	_, certs, err := renewBotCerts(ctx, srv, result.Certs.TLS, bot.Status.UserName, result.PrivateKey)
	require.NoError(t, err)

	// Parse the Identity
	impersonatedTLSCert, err := tlsca.ParseCertificatePEM(certs.TLS)
	require.NoError(t, err)
	impersonatedIdent, err := tlsca.FromSubject(impersonatedTLSCert.Subject, impersonatedTLSCert.NotAfter)
	require.NoError(t, err)

	// Check for proper cert extensions
	require.True(t, impersonatedIdent.Renewable)
	require.False(t, impersonatedIdent.DisallowReissue)
	require.Equal(t, "test", impersonatedIdent.BotName)

	// Initial certs have generation=1 and we start with a renewal, so add 2
	require.Equal(t, uint64(2), impersonatedIdent.Generation)
}

// TestRegisterBot_RemoteAddr checks that certs returned for bot registration contain specified in the request remote addr.
func TestRegisterBot_RemoteAddr(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	p, err := newTestPack(ctx, t.TempDir())
	require.NoError(t, err)
	a := p.a

	_, sshPubKey, _, tlsPubKey := newSSHAndTLSKeyPairs(t)

	roleName := "test-role"
	_, err = authtest.CreateRole(ctx, a, roleName, types.RoleSpecV6{})
	require.NoError(t, err)

	botName := "botty"
	_, err = machineidv1.UpsertBot(ctx, a, &machineidv1pb.Bot{
		Kind:    types.KindBot,
		Version: types.V1,
		Metadata: &headerv1.Metadata{
			Name: botName,
		},
		Spec: &machineidv1pb.BotSpec{
			Roles: []string{roleName},
		},
	}, a.GetClock().Now(), "")
	require.NoError(t, err)

	remoteAddr := "42.42.42.42:42"

	t.Run("IAM method", func(t *testing.T) {
		a.SetHTTPClientForAWSSTS(&mockClient{
			respStatusCode: http.StatusOK,
			respBody: responseFromAWSIdentity(auth.AWSIdentity{
				Account: "1234",
				Arn:     "arn:aws::1111",
			}),
		})

		// add token to auth server
		awsTokenName := "aws-test-token"
		awsToken, err := types.NewProvisionTokenFromSpec(
			awsTokenName,
			time.Now().Add(time.Minute),
			types.ProvisionTokenSpecV2{
				Roles: []types.SystemRole{types.RoleBot},
				Allow: []*types.TokenRule{
					{
						AWSAccount: "1234",
						AWSARN:     "arn:aws::1111",
					},
				},
				BotName:    botName,
				JoinMethod: types.JoinMethodIAM,
			})
		require.NoError(t, err)
		require.NoError(t, a.UpsertToken(ctx, awsToken))

		certs, err := a.RegisterUsingIAMMethod(context.Background(), func(challenge string) (*proto.RegisterUsingIAMMethodRequest, error) {
			templateInput := defaultIdentityRequestTemplateInput(challenge)
			var identityRequest bytes.Buffer
			require.NoError(t, identityRequestTemplate.Execute(&identityRequest, templateInput))

			req := &proto.RegisterUsingIAMMethodRequest{
				RegisterUsingTokenRequest: &types.RegisterUsingTokenRequest{
					Token:        awsTokenName,
					HostID:       "test-bot",
					Role:         types.RoleBot,
					PublicSSHKey: sshPubKey,
					PublicTLSKey: tlsPubKey,
					RemoteAddr:   "42.42.42.42:42",
				},
				StsIdentityRequest: identityRequest.Bytes(),
			}
			return req, nil
		})
		require.NoError(t, err)
		checkCertLoginIP(t, certs.TLS, remoteAddr)
	})

	t.Run("Azure method", func(t *testing.T) {
		subID := uuid.NewString()
		resourceGroup := "rg"
		rsID := vmResourceID(subID, resourceGroup, "test-vm")
		vmID := "vmID"

		accessToken, err := makeToken(rsID, "", a.GetClock().Now())
		require.NoError(t, err)

		// add token to auth server
		azureTokenName := "azure-test-token"
		azureToken, err := types.NewProvisionTokenFromSpec(
			azureTokenName,
			time.Now().Add(time.Minute),
			types.ProvisionTokenSpecV2{
				Roles:      []types.SystemRole{types.RoleBot},
				Azure:      &types.ProvisionTokenSpecV2Azure{Allow: []*types.ProvisionTokenSpecV2Azure_Rule{{Subscription: subID}}},
				BotName:    botName,
				JoinMethod: types.JoinMethodAzure,
			})
		require.NoError(t, err)
		require.NoError(t, a.UpsertToken(ctx, azureToken))

		vmClient := &mockAzureVMClient{
			vms: map[string]*azure.VirtualMachine{
				rsID: {
					ID:            rsID,
					Name:          "test-vm",
					Subscription:  subID,
					ResourceGroup: resourceGroup,
					VMID:          vmID,
				},
			},
		}
		getVMClient := makeVMClientGetter(map[string]*mockAzureVMClient{
			subID: vmClient,
		})

		tlsConfig, err := fixtures.LocalTLSConfig()
		require.NoError(t, err)

		block, _ := pem.Decode(fixtures.LocalhostKey)
		pkey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		require.NoError(t, err)

		certs, err := a.RegisterUsingAzureMethodWithOpts(context.Background(), func(challenge string) (*proto.RegisterUsingAzureMethodRequest, error) {
			ad := auth.AttestedData{
				Nonce:          challenge,
				SubscriptionID: subID,
				ID:             vmID,
			}
			adBytes, err := json.Marshal(&ad)
			require.NoError(t, err)
			s, err := pkcs7.NewSignedData(adBytes)
			require.NoError(t, err)
			require.NoError(t, s.AddSigner(tlsConfig.Certificate, pkey, pkcs7.SignerInfoConfig{}))
			signature, err := s.Finish()
			require.NoError(t, err)
			signedAD := auth.SignedAttestedData{
				Encoding:  "pkcs7",
				Signature: base64.StdEncoding.EncodeToString(signature),
			}
			signedADBytes, err := json.Marshal(&signedAD)
			require.NoError(t, err)

			req := &proto.RegisterUsingAzureMethodRequest{
				RegisterUsingTokenRequest: &types.RegisterUsingTokenRequest{
					Token:        azureTokenName,
					HostID:       "test-node",
					Role:         types.RoleBot,
					PublicSSHKey: sshPubKey,
					PublicTLSKey: tlsPubKey,
					RemoteAddr:   remoteAddr,
				},
				AttestedData: signedADBytes,
				AccessToken:  accessToken,
			}
			return req, nil
		}, auth.WithAzureCerts([]*x509.Certificate{tlsConfig.Certificate}), auth.WithAzureVerifyFunc(mockVerifyToken(nil)), auth.WithAzureVMClientGetter(getVMClient))
		require.NoError(t, err)
		checkCertLoginIP(t, certs.TLS, remoteAddr)
	})
}

// authClientForRegisterResult is a test helper that creats an auth client for
// the given [*join.RegisterResult].
func authClientForRegisterResult(t *testing.T, ctx context.Context, addr *utils.NetAddr, result *join.RegisterResult) *authclient.Client {
	privateKeyPEM, err := keys.MarshalPrivateKey(result.PrivateKey)
	require.NoError(t, err)
	sshPub, err := ssh.NewPublicKey(result.PrivateKey.Public())
	require.NoError(t, err)
	ident, err := identity.ReadIdentityFromStore(&identity.LoadIdentityParams{
		PrivateKeyBytes: privateKeyPEM,
		PublicKeyBytes:  ssh.MarshalAuthorizedKey(sshPub),
		TokenHashBytes:  []byte{},
	}, result.Certs)
	require.NoError(t, err)

	facade := identity.NewFacade(false, true, ident)

	tlsConfig, err := facade.TLSConfig()
	require.NoError(t, err)
	sshConfig, err := facade.SSHClientConfig()
	require.NoError(t, err)

	resolver, err := reversetunnelclient.CachingResolver(
		ctx,
		reversetunnelclient.WebClientResolver(&webclient.Config{
			Context:   ctx,
			ProxyAddr: addr.String(),
			Insecure:  true,
		}),
		nil /* clock */)
	require.NoError(t, err)

	log := logtest.NewLogger()
	dialer, err := reversetunnelclient.NewTunnelAuthDialer(reversetunnelclient.TunnelAuthDialerConfig{
		Resolver:              resolver,
		ClientConfig:          sshConfig,
		Log:                   log,
		InsecureSkipTLSVerify: true,
		GetClusterCAs:         apiclient.ClusterCAsFromCertPool(tlsConfig.RootCAs),
	})
	require.NoError(t, err)

	authClientConfig := &authclient.Config{
		TLS:         tlsConfig,
		SSH:         sshConfig,
		AuthServers: []utils.NetAddr{*addr},
		Log:         log,
		Insecure:    true,
		ProxyDialer: dialer,
		DialOpts: []grpc.DialOption{
			metadata.WithUserAgentFromTeleportComponent(teleport.ComponentTBot),
		},
	}

	c, err := authclient.Connect(ctx, authClientConfig)
	require.NoError(t, err)

	return c
}

// instanceIDFromCerts parses a TLS identity from the certificates and returns
// the embedded BotInstanceID and generation, if any.
func instanceIDFromCerts(t *testing.T, certs *proto.Certs) (string, uint64) {
	t.Helper()

	cert, err := tlsca.ParseCertificatePEM(certs.TLS)
	require.NoError(t, err)

	ident, err := tlsca.FromSubject(cert.Subject, cert.NotAfter)
	require.NoError(t, err)

	return ident.BotInstanceID, ident.Generation
}

// registerHelper calls `join.Register` with the given token, prefilling params
// where possible. Overrides may be applied with `fns`.
func registerHelper(
	ctx context.Context, token types.ProvisionToken,
	addr *utils.NetAddr,
	fns ...func(*join.RegisterParams),
) (*join.RegisterResult, error) {
	params := join.RegisterParams{
		JoinMethod: token.GetJoinMethod(),
		Token:      token.GetName(),
		ID: state.IdentityID{
			Role: types.RoleBot,
		},
		AuthServers: []utils.NetAddr{*addr},
		KubernetesReadFileFunc: func(name string) ([]byte, error) {
			return []byte("jwks-matching-service-account"), nil
		},
	}

	for _, fn := range fns {
		fn(&params)
	}

	result, err := join.Register(ctx, params)
	return result, trace.Wrap(err)
}

// TestRegisterBot_BotInstanceRejoin validates that bot instance IDs are
// preserved when rejoining with an authenticated auth client.
func TestRegisterBot_BotInstanceRejoin(t *testing.T) {
	// Note: we can't enable parallel testing for this due to use of t.Setenv()
	// for AWS client configuration.

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv := newTestTLSServer(t)
	a := srv.Auth()
	addr := utils.MustParseAddr(srv.Addr().String())

	// Configure mock join methods
	k8sTokenName := "jwks-matching-service-account"
	k8sReadFileFunc := func(name string) ([]byte, error) {
		return []byte(k8sTokenName), nil
	}
	a.SetJWKSValidator(func(_ time.Time, _ []byte, _ string, tkn string) (*token.ValidationResult, error) {
		if tkn == k8sTokenName {
			return &token.ValidationResult{Username: "system:serviceaccount:static-jwks:matching"}, nil
		}

		return nil, errMockInvalidToken
	})

	a.SetHTTPClientForAWSSTS(&mockClient{
		respStatusCode: http.StatusOK,
		respBody: responseFromAWSIdentity(auth.AWSIdentity{
			Account: "1234",
			Arn:     "arn:aws::1111",
		}),
	})

	t.Setenv("AWS_ACCESS_KEY_ID", "FAKE_ID")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "FAKE_KEY")
	t.Setenv("AWS_SESSION_TOKEN", "FAKE_TOKEN")
	t.Setenv("AWS_REGION", "us-west-2")

	// Create a bot
	roleName := "test-role"
	_, err := authtest.CreateRole(ctx, a, roleName, types.RoleSpecV6{})
	require.NoError(t, err)

	botName := "bot"
	_, err = machineidv1.UpsertBot(ctx, a, &machineidv1pb.Bot{
		Kind:    types.KindBot,
		Version: types.V1,
		Metadata: &headerv1.Metadata{
			Name: botName,
		},
		Spec: &machineidv1pb.BotSpec{
			Roles: []string{roleName},
		},
	}, a.GetClock().Now(), "")
	require.NoError(t, err)

	// Create k8s and IAM join tokens
	k8sToken, err := types.NewProvisionTokenFromSpec("static-jwks", time.Now().Add(10*time.Minute), types.ProvisionTokenSpecV2{
		JoinMethod: types.JoinMethodKubernetes,
		Roles:      []types.SystemRole{types.RoleBot},
		BotName:    botName,
		Kubernetes: &types.ProvisionTokenSpecV2Kubernetes{
			Type: types.KubernetesJoinTypeStaticJWKS,
			Allow: []*types.ProvisionTokenSpecV2Kubernetes_Rule{
				{ServiceAccount: "static-jwks:matching"},
			},
			StaticJWKS: &types.ProvisionTokenSpecV2Kubernetes_StaticJWKSConfig{
				JWKS: "fake-jwks",
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, a.CreateToken(ctx, k8sToken))

	awsToken, err := types.NewProvisionTokenFromSpec(
		"aws-test-token",
		time.Now().Add(10*time.Minute),
		types.ProvisionTokenSpecV2{
			Roles: []types.SystemRole{types.RoleBot},
			Allow: []*types.TokenRule{
				{
					AWSAccount: "1234",
					AWSARN:     "arn:aws::1111",
				},
			},
			BotName:    botName,
			JoinMethod: types.JoinMethodIAM,
		})
	require.NoError(t, err)
	require.NoError(t, a.UpsertToken(ctx, awsToken))

	// Join as a "bot" with both token types.
	k8sResult, err := registerHelper(ctx, k8sToken, addr, func(p *join.RegisterParams) {
		p.KubernetesReadFileFunc = k8sReadFileFunc
	})
	require.NoError(t, err)
	initialK8sInstanceID, initialK8sGeneration := instanceIDFromCerts(t, k8sResult.Certs)
	require.NotEmpty(t, initialK8sInstanceID)
	require.Equal(t, uint64(1), initialK8sGeneration)

	awsResult, err := registerHelper(ctx, awsToken, addr)
	require.NoError(t, err)
	initialAWSInstanceID, initialAWSGeneration := instanceIDFromCerts(t, awsResult.Certs)
	require.NotEmpty(t, initialAWSInstanceID)
	require.Equal(t, uint64(1), initialAWSGeneration)

	// They should be issued unique IDs despite being the same bot.
	require.NotEqual(t, initialK8sInstanceID, initialAWSInstanceID, "instance IDs must not be the same when no client certs are provided")

	// Rejoin using the k8s client and make sure we're issued certs with the
	// same instance ID.
	k8sClient := authClientForRegisterResult(t, ctx, addr, k8sResult)
	rejoinedK8sResult, err := registerHelper(ctx, k8sToken, addr, func(p *join.RegisterParams) {
		p.KubernetesReadFileFunc = k8sReadFileFunc
		p.AuthClient = k8sClient
	})
	require.NoError(t, err)

	rejoinedK8sID, rejoinedK8sGeneration := instanceIDFromCerts(t, rejoinedK8sResult.Certs)
	require.Equal(t, initialK8sInstanceID, rejoinedK8sID)
	require.Equal(t, uint64(2), rejoinedK8sGeneration)

	// Repeat for the AWS client. Note that the AWS client is routed through the
	// join service, the instance ID must be provided to auth by the proxy as
	// part of the `RegisterUsingTokenRequest`.
	iamClient := authClientForRegisterResult(t, ctx, addr, awsResult)
	rejoinedAWSResult, err := registerHelper(ctx, awsToken, addr, func(p *join.RegisterParams) {
		p.AuthClient = iamClient
	})
	require.NoError(t, err)

	rejoinedAWSID, rejoinedAWSGeneration := instanceIDFromCerts(t, rejoinedAWSResult.Certs)
	require.Equal(t, initialAWSInstanceID, rejoinedAWSID)
	require.Equal(t, uint64(2), rejoinedAWSGeneration)

	// Last, try to lie to auth. The k8s value should be overwritten with the
	// correct instance ID since auth can directly inspect the client identity.
	// For good measure, we'll include a "legitimate" instance ID from the AWS
	// bot.
	certs, err := k8sClient.RegisterUsingToken(ctx, &types.RegisterUsingTokenRequest{
		Token:         k8sToken.GetName(),
		HostID:        "test-bot",
		IDToken:       k8sTokenName,
		Role:          types.RoleBot,
		PublicSSHKey:  []byte(sshPubKey),
		PublicTLSKey:  []byte(tlsPubKey),
		BotInstanceID: initialAWSInstanceID,
	})
	require.NoError(t, err)

	rejoinedK8sID, rejoinedK8sGeneration = instanceIDFromCerts(t, certs)
	require.Equal(t, initialK8sInstanceID, rejoinedK8sID)
	require.Equal(t, uint64(3), rejoinedK8sGeneration)

	// Note: Lying via IAM join not tested as that must be routed through the
	// join service (along with Azure and TPM).
}

// TestRegisterBotWithInvalidInstanceID ensures that client-specified instance
// IDs from untrusted sources are ignored and will be issued a new bot instance
// ID.
func TestRegisterBotWithInvalidInstanceID(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv := newTestTLSServer(t)
	a := srv.Auth()

	botName := "bot"
	k8sTokenName := "jwks-matching-service-account"
	a.SetJWKSValidator(func(_ time.Time, _ []byte, _ string, tkn string) (*token.ValidationResult, error) {
		if tkn == k8sTokenName {
			return &token.ValidationResult{Username: "system:serviceaccount:static-jwks:matching"}, nil
		}

		return nil, errMockInvalidToken
	})
	token, err := types.NewProvisionTokenFromSpec("static-jwks", time.Now().Add(10*time.Minute), types.ProvisionTokenSpecV2{
		JoinMethod: types.JoinMethodKubernetes,
		Roles:      []types.SystemRole{types.RoleBot},
		BotName:    botName,
		Kubernetes: &types.ProvisionTokenSpecV2Kubernetes{
			Type: types.KubernetesJoinTypeStaticJWKS,
			Allow: []*types.ProvisionTokenSpecV2Kubernetes_Rule{
				{ServiceAccount: "static-jwks:matching"},
			},
			StaticJWKS: &types.ProvisionTokenSpecV2Kubernetes_StaticJWKSConfig{
				JWKS: "fake-jwks",
			},
		},
	})
	require.NoError(t, err)
	require.NoError(t, a.CreateToken(ctx, token))

	roleName := "test-role"
	_, err = authtest.CreateRole(ctx, a, roleName, types.RoleSpecV6{})
	require.NoError(t, err)

	_, err = machineidv1.UpsertBot(ctx, a, &machineidv1pb.Bot{
		Kind:    types.KindBot,
		Version: types.V1,
		Metadata: &headerv1.Metadata{
			Name: botName,
		},
		Spec: &machineidv1pb.BotSpec{
			Roles: []string{roleName},
		},
	}, a.GetClock().Now(), "")
	require.NoError(t, err)

	client, err := srv.NewClient(authtest.TestAdmin())
	require.NoError(t, err)

	privateKey, sshPublicKey, err := testauthority.New().GenerateKeyPair()
	require.NoError(t, err)
	sshPrivateKey, err := ssh.ParseRawPrivateKey(privateKey)
	require.NoError(t, err)
	tlsPublicKey, err := tlsca.MarshalPublicKeyFromPrivateKeyPEM(sshPrivateKey)
	require.NoError(t, err)

	// Try registering with a proxy client; this is trusted but the invalid
	// instance ID should be overwritten and a new instance generated.
	certs, err := srv.Auth().RegisterUsingToken(ctx, &types.RegisterUsingTokenRequest{
		Token:         token.GetName(),
		HostID:        "test-bot",
		Role:          types.RoleBot,
		PublicSSHKey:  sshPublicKey,
		PublicTLSKey:  tlsPublicKey,
		IDToken:       k8sTokenName,
		BotInstanceID: "foo",
	})

	// Should not generate any errors, especially some variety of "instance not
	// found" which might indicate improper behavior when encountering a
	// nonexistent token.
	require.NoError(t, err)

	// Should not issue certs with an obviously invalid instance ID, or no ID.
	id, generation := instanceIDFromCerts(t, certs)
	require.NotEmpty(t, id)
	require.NotEqual(t, "foo", id)
	require.Equal(t, uint64(1), generation)

	// Try registering with a non-proxy client; this is untrusted and the
	// client-provided ID should be discarded.
	certs, err = client.RegisterUsingToken(ctx, &types.RegisterUsingTokenRequest{
		Token:         token.GetName(),
		HostID:        "test-bot",
		Role:          types.RoleBot,
		PublicSSHKey:  sshPublicKey,
		PublicTLSKey:  tlsPublicKey,
		IDToken:       k8sTokenName,
		BotInstanceID: "foo",
	})

	// As above, should not generate any errors, and a new ID should be
	// generated.
	require.NoError(t, err)

	id, generation = instanceIDFromCerts(t, certs)
	require.NotEmpty(t, id)
	require.NotEqual(t, "foo", id)
	require.Equal(t, uint64(1), generation)
}

func TestRegisterBotMultipleTokens(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	srv := newTestTLSServer(t)

	// Initial setup, create a bot and join token.
	client, err := srv.NewClient(authtest.TestAdmin())
	require.NoError(t, err)
	bot, err := client.BotServiceClient().CreateBot(ctx, &machineidv1pb.CreateBotRequest{
		Bot: &machineidv1pb.Bot{
			Kind:    types.KindBot,
			Version: types.V1,
			Metadata: &headerv1.Metadata{
				Name: "test",
			},
			Spec: &machineidv1pb.BotSpec{
				Roles: []string{"example"},
			},
		},
	})
	require.NoError(t, err)

	tokenA, err := types.NewProvisionTokenFromSpec("a", time.Time{}, types.ProvisionTokenSpecV2{
		Roles:   types.SystemRoles{types.RoleBot},
		BotName: bot.Metadata.Name,
	})
	require.NoError(t, err)
	require.NoError(t, client.CreateToken(ctx, tokenA))

	tokenB, err := types.NewProvisionTokenFromSpec("b", time.Time{}, types.ProvisionTokenSpecV2{
		Roles:   types.SystemRoles{types.RoleBot},
		BotName: bot.Metadata.Name,
	})
	require.NoError(t, err)
	require.NoError(t, client.CreateToken(ctx, tokenB))

	resultA, err := join.Register(ctx, join.RegisterParams{
		Token: tokenA.GetName(),
		ID: state.IdentityID{
			Role: types.RoleBot,
		},
		AuthServers: []utils.NetAddr{*utils.MustParseAddr(srv.Addr().String())},
	})
	require.NoError(t, err)
	certsA := resultA.Certs

	initialInstanceA, _ := instanceIDFromCerts(t, certsA)
	require.NotEmpty(t, initialInstanceA)

	resultB, err := join.Register(ctx, join.RegisterParams{
		Token: tokenB.GetName(),
		ID: state.IdentityID{
			Role: types.RoleBot,
		},
		AuthServers: []utils.NetAddr{*utils.MustParseAddr(srv.Addr().String())},
	})
	require.NoError(t, err)
	certsB := resultB.Certs

	initialInstanceB, _ := instanceIDFromCerts(t, certsB)
	require.NotEmpty(t, initialInstanceB)

	require.NotEqual(t, initialInstanceA, initialInstanceB)

	for i := range 6 {
		_, certsA, err = renewBotCerts(ctx, srv, certsA.TLS, bot.Status.UserName, resultA.PrivateKey)
		require.NoError(t, err)

		instanceA, generationA := instanceIDFromCerts(t, certsA)
		require.Equal(t, initialInstanceA, instanceA)
		require.Equal(t, uint64(i+2), generationA)

		// Only renew bot B 3x.
		if i < 3 {
			_, certsB, err = renewBotCerts(ctx, srv, certsB.TLS, bot.Status.UserName, resultB.PrivateKey)
			require.NoError(t, err)

			instanceB, generationB := instanceIDFromCerts(t, certsB)
			require.Equal(t, initialInstanceB, instanceB)
			require.Equal(t, uint64(i+2), generationB)
		}
	}

	// Renew B again. This will be the final renewal, but the legacy generation
	// counter on the user will be greater as it should have been incremented by
	// bot A.
	_, certsB, err = renewBotCerts(ctx, srv, certsB.TLS, bot.Status.UserName, resultB.PrivateKey)
	require.NoError(t, err)

	instanceB, generationB := instanceIDFromCerts(t, certsB)
	require.Equal(t, initialInstanceB, instanceB)
	require.Equal(t, uint64(5), generationB)

	botUser, err := client.GetUser(ctx, bot.Status.UserName, false)
	require.NoError(t, err)
	genStr := botUser.BotGenerationLabel()
	require.Equal(t, "7", genStr)
}

func checkCertLoginIP(t *testing.T, certBytes []byte, loginIP string) {
	t.Helper()

	cert, err := tlsca.ParseCertificatePEM(certBytes)
	require.NoError(t, err)
	identity, err := tlsca.FromSubject(cert.Subject, cert.NotAfter)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(identity.LoginIP, loginIP))
}
