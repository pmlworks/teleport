/*
Copyright 2015 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package daemon

import (
	"context"
	"fmt"
	"net"

	"github.com/google/uuid"
	"github.com/gravitational/teleport/lib/srv/alpnproxy"
	alpncommon "github.com/gravitational/teleport/lib/srv/alpnproxy/common"
	"github.com/gravitational/teleport/lib/teleterm/api/uri"
	"github.com/gravitational/teleport/lib/utils"

	"github.com/gravitational/trace"

	log "github.com/sirupsen/logrus"
)

// CreateGatewayParams describes gateway parameters
type GatewayParams struct {
	// ResourceName is the resource name
	ResourceName string
	// HostID is the hostID of the resource agent
	HostID string
	// Port is the gateway port
	Port string
	// Protocol is the gateway protocol
	Protocol string
}

// Gateway describes local proxy that creates a gateway to the remote Teleport resource.
type Gateway struct {
	// URI is gateway URI
	URI string
	// ResourceName is the Teleport resource name
	ResourceName string
	// ClusterID is the cluster ID of the gateway
	ClusterID string
	// HostID is the gateway remote host ID
	HostID string
	// LocalPort the gateway local port
	LocalPort string
	// LocalAddress is the local address
	LocalAddress string
	// Protocol is the gateway protocol
	Protocol string
	// CACertPath
	CACertPath string
	// DBCertPath
	DBCertPath string
	// KeyPath
	KeyPath string

	localProxy *alpnproxy.LocalProxy
	// closeContext and closeCancel are used to signal to any waiting goroutines
	// that the local proxy is now closed and to release any resources.
	closeContext context.Context
	closeCancel  context.CancelFunc
}

func (g *Gateway) Close() {
	g.closeCancel()
}

// Open opens a gateway to Teleport proxy
func (g *Gateway) Open() {
	go func() {
		if err := g.localProxy.Start(g.closeContext); err != nil {
			log.WithError(err).Warn("Failed to open a connection.")
		}
	}()
}

// CreateGateway creates a gateway to the Teleport proxy
func (c *Cluster) CreateGateway(ctx context.Context, dbURI, port, user string) (*Gateway, error) {
	db, err := c.GetDatabase(ctx, dbURI)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	if err := c.ReissueDBCerts(ctx, user, db); err != nil {
		return nil, trace.Wrap(err)
	}

	gateway, err := c.createGateway(GatewayParams{
		ResourceName: db.GetName(),
		HostID:       uri.Parse(dbURI).DB(),
		Port:         port,
		Protocol:     db.GetProtocol(),
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	c.gateways = append(c.gateways, gateway)

	gateway.Open()

	return gateway, nil
}

// RemoveGateway removes Cluster gateway
func (c *Cluster) RemoveGateway(ctx context.Context, gatewayURI string) error {
	gateway, err := c.FindGateway(gatewayURI)
	if err != nil {
		return trace.Wrap(err)
	}

	gateway.Close()

	// remove closed gateway from list
	for index := range c.gateways {
		if c.gateways[index] == gateway {
			c.gateways = append(c.gateways[:index], c.gateways[index+1:]...)
			return nil
		}
	}

	return nil
}

// FindGateway finds gateway by URI
func (c *Cluster) FindGateway(gatewayURI string) (*Gateway, error) {
	for _, gateway := range c.GetGateways() {
		if gateway.URI == gatewayURI {
			return gateway, nil
		}
	}

	return nil, trace.NotFound("gateway is not found: %v", gatewayURI)
}

// GetGateways returns a list of cluster gateways
func (c *Cluster) GetGateways() []*Gateway {
	return c.gateways
}

func (c *Cluster) createGateway(params GatewayParams) (*Gateway, error) {
	addr := fmt.Sprintf("127.0.0.1:%s", params.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// make sure the listener is closed if gateway creation failed
	ok := false
	defer func() {
		if ok {
			return
		}

		if err := listener.Close(); err != nil {
			log.WithError(err).Warn("Failed to close listener.")
		}
	}()

	closeContext, closeCancel := context.WithCancel(context.Background())
	defer closeCancel()

	localProxy, err := c.newLocalProxy(closeContext, params.Protocol, listener)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	ok = true
	return &Gateway{
		URI:          uri.Cluster(c.status.Name).Gateway(uuid.NewString()).String(),
		LocalAddress: listener.Addr().String(),
		LocalPort:    params.Port,
		Protocol:     params.Protocol,
		HostID:       params.HostID,
		ResourceName: params.ResourceName,
		ClusterID:    c.status.Name,
		KeyPath:      c.status.KeyPath(),
		CACertPath:   c.status.CACertPath(),
		DBCertPath:   c.status.DatabaseCertPath(params.HostID),
		closeContext: closeContext,
		closeCancel:  closeCancel,
		localProxy:   localProxy,
	}, nil

}

func (c *Cluster) newLocalProxy(ctx context.Context, protocol string, listener net.Listener) (*alpnproxy.LocalProxy, error) {
	alpnProtocol, err := alpncommon.ToALPNProtocol(protocol)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	address, err := utils.ParseAddr(c.clusterClient.WebProxyAddr)
	if err != nil {
		return nil, trace.Wrap(err)
	}

	lp, err := alpnproxy.NewLocalProxy(alpnproxy.LocalProxyConfig{
		InsecureSkipVerify: c.clusterClient.InsecureSkipVerify,
		RemoteProxyAddr:    c.clusterClient.WebProxyAddr,
		Protocol:           alpnProtocol,
		Listener:           listener,
		ParentContext:      ctx,
		SNI:                address.Host(),
	})
	if err != nil {
		return nil, trace.Wrap(err)
	}

	return lp, nil
}
