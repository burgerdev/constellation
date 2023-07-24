/*
Copyright (c) Edgeless Systems GmbH

SPDX-License-Identifier: AGPL-3.0-only
*/

package kubernetes

import (
	"context"
	"net"

	"github.com/edgelesssys/constellation/v2/internal/deploy/helm"
	"github.com/edgelesssys/constellation/v2/internal/logger"
	"github.com/edgelesssys/constellation/v2/internal/role"
	"github.com/edgelesssys/constellation/v2/internal/versions/components"
)

type clusterUtil interface {
	InstallComponents(ctx context.Context, kubernetesComponents components.Components) error
	InitCluster(ctx context.Context, initConfig []byte, nodeName, clusterName string, ips []net.IP, controlPlaneHost, controlPlanePort string, conformanceMode bool, log *logger.Logger) ([]byte, error)
	JoinCluster(ctx context.Context, joinConfig []byte, peerRole role.Role, controlPlaneHost, controlPlanePort string, log *logger.Logger) error
	WaitForCilium(ctx context.Context, log *logger.Logger) error
	FixCilium(ctx context.Context) error
	StartKubelet() error
}

// helmClient bundles functions related to microservice deployment.
// Only microservices that can be deployed purely via Helm are deployed with this interface.
type helmClient interface {
	InstallChart(context.Context, helm.Release) error
	InstallChartWithValues(ctx context.Context, release helm.Release, extraValues map[string]any) error
}
