/*
Copyright 2022 Gravitational, Inc.

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

package types

import (
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/gravitational/trace"
	"github.com/stretchr/testify/require"

	"github.com/gravitational/teleport/api/constants"
)

// TestAppPublicAddrValidation tests PublicAddr field validation to make sure that
// an app with internal "kube-teleport-proxy-alpn." ServerName prefix won't be created.
func TestAppPublicAddrValidation(t *testing.T) {
	tests := []struct {
		name       string
		publicAddr string
		check      require.ErrorAssertionFunc
	}{
		{
			name:       "kubernetes app",
			publicAddr: "kubernetes.example.com:3080",
			check:      hasNoErr,
		},
		{
			name:       "kubernetes app public addr without port",
			publicAddr: "kubernetes.example.com",
			check:      hasNoErr,
		},
		{
			name:       "kubernetes app http",
			publicAddr: "http://kubernetes.example.com:3080",
			check:      hasNoErr,
		},
		{
			name:       "kubernetes app https",
			publicAddr: "https://kubernetes.example.com:3080",
			check:      hasNoErr,
		},
		{
			name:       "public address with internal kube ServerName prefix",
			publicAddr: constants.KubeTeleportProxyALPNPrefix + "example.com:3080",
			check:      hasErrTypeBadParameter,
		},
		{
			name:       "https public address with internal kube ServerName prefix",
			publicAddr: "https://" + constants.KubeTeleportProxyALPNPrefix + "example.com:3080",
			check:      hasErrTypeBadParameter,
		},
		{
			name:       "addr with numbers in the host",
			publicAddr: "123456789012.teleport.example.com:3080",
			check:      hasNoErr,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewAppV3(Metadata{
				Name: "TestApp",
			}, AppSpecV3{
				PublicAddr: tc.publicAddr,
				URI:        "localhost:3080",
			})
			tc.check(t, err)
		})
	}
}

func TestAppPortsValidation(t *testing.T) {
	tests := []struct {
		name     string
		tcpPorts []*PortRange
		uri      string
		check    require.ErrorAssertionFunc
	}{
		{
			name: "valid ranges and single ports",
			tcpPorts: []*PortRange{
				&PortRange{Port: 22, EndPort: 25},
				&PortRange{Port: 26},
				&PortRange{Port: 65535},
			},
			check: hasNoErr,
		},
		{
			name: "valid overlapping ranges",
			tcpPorts: []*PortRange{
				&PortRange{Port: 100, EndPort: 200},
				&PortRange{Port: 150, EndPort: 175},
				&PortRange{Port: 111},
				&PortRange{Port: 150, EndPort: 210},
				&PortRange{Port: 1, EndPort: 65535},
			},
			check: hasNoErr,
		},
		{
			name: "valid non-TCP app with ports ignored",
			uri:  "http://localhost:8000",
			tcpPorts: []*PortRange{
				&PortRange{Port: 123456789},
				&PortRange{Port: 10, EndPort: 2},
			},
			check: hasNoErr,
		},
		// Test cases for invalid ports.
		{
			name: "port smaller than 1",
			tcpPorts: []*PortRange{
				&PortRange{Port: 0},
			},
			check: hasErrTypeBadParameter,
		},
		{
			name: "port bigger than 65535",
			tcpPorts: []*PortRange{
				&PortRange{Port: 78787},
			},
			check: hasErrTypeBadParameter,
		},
		{
			name: "end port smaller than 2",
			tcpPorts: []*PortRange{
				&PortRange{Port: 5, EndPort: 1},
			},
			check: hasErrTypeBadParameterAndContains("end port must be between 6 and 65535"),
		},
		{
			name: "end port bigger than 65535",
			tcpPorts: []*PortRange{
				&PortRange{Port: 1, EndPort: 78787},
			},
			check: hasErrTypeBadParameter,
		},
		{
			name: "end port smaller than port",
			tcpPorts: []*PortRange{
				&PortRange{Port: 10, EndPort: 5},
			},
			check: hasErrTypeBadParameterAndContains("end port must be between 11 and 65535"),
		},
		{
			name: "uri specifies port",
			uri:  "tcp://localhost:1234",
			tcpPorts: []*PortRange{
				&PortRange{Port: 1000, EndPort: 1500},
			},
			check: hasErrTypeBadParameterAndContains("must not include a port number"),
		},
		{
			name: "invalid uri",
			uri:  "%",
			tcpPorts: []*PortRange{
				&PortRange{Port: 1000, EndPort: 1500},
			},
			check: hasErrAndContains("invalid URL escape"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := AppSpecV3{
				URI:      "tcp://localhost",
				TCPPorts: tc.tcpPorts,
			}
			if tc.uri != "" {
				spec.URI = tc.uri
			}

			_, err := NewAppV3(Metadata{Name: "TestApp"}, spec)
			tc.check(t, err)
		})
	}
}

func TestAppServerSorter(t *testing.T) {
	t.Parallel()

	testValsUnordered := []string{"d", "b", "a", "c"}

	makeServers := func(testVals []string, testField string) []AppServer {
		servers := make([]AppServer, len(testVals))
		for i := 0; i < len(testVals); i++ {
			testVal := testVals[i]
			var err error
			servers[i], err = NewAppServerV3(Metadata{
				Name: "_",
			}, AppServerSpecV3{
				HostID: "_",
				App: &AppV3{
					Metadata: Metadata{
						Name:        getTestVal(testField == ResourceMetadataName, testVal),
						Description: getTestVal(testField == ResourceSpecDescription, testVal),
					},
					Spec: AppSpecV3{
						URI:        "_",
						PublicAddr: getTestVal(testField == ResourceSpecPublicAddr, testVal),
					},
				},
			})
			require.NoError(t, err)
		}
		return servers
	}

	cases := []struct {
		name      string
		fieldName string
	}{
		{
			name:      "by name",
			fieldName: ResourceMetadataName,
		},
		{
			name:      "by description",
			fieldName: ResourceSpecDescription,
		},
		{
			name:      "by publicAddr",
			fieldName: ResourceSpecPublicAddr,
		},
	}

	for _, c := range cases {
		c := c
		t.Run(fmt.Sprintf("%s desc", c.name), func(t *testing.T) {
			sortBy := SortBy{Field: c.fieldName, IsDesc: true}
			servers := AppServers(makeServers(testValsUnordered, c.fieldName))
			require.NoError(t, servers.SortByCustom(sortBy))
			targetVals, err := servers.GetFieldVals(c.fieldName)
			require.NoError(t, err)
			require.IsDecreasing(t, targetVals)
		})

		t.Run(fmt.Sprintf("%s asc", c.name), func(t *testing.T) {
			sortBy := SortBy{Field: c.fieldName}
			servers := AppServers(makeServers(testValsUnordered, c.fieldName))
			require.NoError(t, servers.SortByCustom(sortBy))
			targetVals, err := servers.GetFieldVals(c.fieldName)
			require.NoError(t, err)
			require.IsIncreasing(t, targetVals)
		})
	}

	// Test error.
	sortBy := SortBy{Field: "unsupported"}
	servers := makeServers(testValsUnordered, "does-not-matter")
	require.True(t, trace.IsNotImplemented(AppServers(servers).SortByCustom(sortBy)))
}

func TestAppIsAWSConsole(t *testing.T) {
	tests := []struct {
		name               string
		uri                string
		cloud              string
		assertIsAWSConsole require.BoolAssertionFunc
	}{
		{
			name:               "AWS Standard",
			uri:                "https://console.aws.amazon.com/ec2/v2/home",
			assertIsAWSConsole: require.True,
		},
		{
			name:               "AWS China",
			uri:                "https://console.amazonaws.cn/console/home",
			assertIsAWSConsole: require.True,
		},
		{
			name:               "AWS GovCloud (US)",
			uri:                "https://console.amazonaws-us-gov.com/console/home",
			assertIsAWSConsole: require.True,
		},
		{
			name:               "Region based not supported yet",
			uri:                "https://us-west-1.console.aws.amazon.com",
			assertIsAWSConsole: require.False,
		},
		{
			name:               "Not an AWS Console URL",
			uri:                "https://hello.world",
			assertIsAWSConsole: require.False,
		},
		{
			name:               "CLI-only AWS App",
			cloud:              CloudAWS,
			assertIsAWSConsole: require.True,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, err := NewAppV3(Metadata{
				Name: "aws",
			}, AppSpecV3{
				URI:   test.uri,
				Cloud: test.cloud,
			})
			require.NoError(t, err)

			test.assertIsAWSConsole(t, app.IsAWSConsole())
		})
	}
}

func TestApplicationGetAWSExternalID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		appAWS             *AppAWS
		expectedExternalID string
	}{
		{
			name: "not configured",
		},
		{
			name: "configured",
			appAWS: &AppAWS{
				ExternalID: "default-external-id",
			},
			expectedExternalID: "default-external-id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, err := NewAppV3(Metadata{
				Name: "aws",
			}, AppSpecV3{
				URI: constants.AWSConsoleURL,
				AWS: test.appAWS,
			})
			require.NoError(t, err)

			require.Equal(t, test.expectedExternalID, app.GetAWSExternalID())
		})
	}
}

func TestApplicationGetAWSRolesAnywhereProfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                          string
		appAWS                        *AppAWS
		expectedProfileARN            string
		expectedAcceptRoleSessionName bool
	}{
		{
			name: "app aws not configured",
		},
		{
			name: "roles anywhere profile not configured",
			appAWS: &AppAWS{
				RolesAnywhereProfile: &AppAWSRolesAnywhereProfile{},
			},
			expectedProfileARN:            "",
			expectedAcceptRoleSessionName: false,
		},
		{
			name: "configured",
			appAWS: &AppAWS{
				RolesAnywhereProfile: &AppAWSRolesAnywhereProfile{
					ProfileARN:            "profile1",
					AcceptRoleSessionName: true,
				},
			},
			expectedProfileARN:            "profile1",
			expectedAcceptRoleSessionName: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, err := NewAppV3(Metadata{
				Name: "aws",
			}, AppSpecV3{
				URI: constants.AWSConsoleURL,
				AWS: test.appAWS,
			})
			require.NoError(t, err)

			require.Equal(t, test.expectedProfileARN, app.GetAWSRolesAnywhereProfileARN())
			require.Equal(t, test.expectedAcceptRoleSessionName, app.GetAWSRolesAnywhereAcceptRoleSessionName())
		})
	}
}

func TestAppIsAzureCloud(t *testing.T) {
	tests := []struct {
		name     string
		cloud    string
		expected bool
	}{
		{
			name:     "Azure Cloud",
			cloud:    CloudAzure,
			expected: true,
		},
		{
			name:     "not Azure Cloud",
			cloud:    CloudAWS,
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, err := NewAppV3(Metadata{Name: "myapp"}, AppSpecV3{Cloud: test.cloud})
			require.NoError(t, err)
			require.Equal(t, test.expected, app.IsAzureCloud())
		})
	}
}

func TestNewAppV3(t *testing.T) {
	tests := []struct {
		name    string
		meta    Metadata
		spec    AppSpecV3
		want    *AppV3
		wantErr require.ErrorAssertionFunc
	}{
		{
			name:    "empty app",
			meta:    Metadata{},
			spec:    AppSpecV3{},
			want:    nil,
			wantErr: require.Error,
		},
		{
			name: "non-cloud app",
			meta: Metadata{
				Name:        "myapp",
				Description: "my fancy app",
			},
			spec: AppSpecV3{URI: "example.com"},
			want: &AppV3{
				Kind:    "app",
				Version: "v3",
				Metadata: Metadata{
					Name:        "myapp",
					Namespace:   "default",
					Description: "my fancy app",
				}, Spec: AppSpecV3{URI: "example.com"},
			},
			wantErr: require.NoError,
		},
		{
			name: "non-cloud app #2",
			meta: Metadata{
				Name:        "myapp",
				Description: "my fancy app",
			},
			spec: AppSpecV3{URI: "example.com"},
			want: &AppV3{
				Kind:    "app",
				Version: "v3",
				Metadata: Metadata{
					Name:        "myapp",
					Namespace:   "default",
					Description: "my fancy app",
				},
				Spec: AppSpecV3{URI: "example.com"},
			},
			wantErr: require.NoError,
		},
		{
			name: "azure app",
			meta: Metadata{Name: "myazure"},
			spec: AppSpecV3{Cloud: CloudAzure},
			want: &AppV3{
				Kind:     "app",
				Version:  "v3",
				Metadata: Metadata{Name: "myazure", Namespace: "default"},
				Spec:     AppSpecV3{URI: "cloud://Azure", Cloud: CloudAzure},
			},
			wantErr: require.NoError,
		},
		{
			name: "aws app CLI only",
			meta: Metadata{Name: "myaws"},
			spec: AppSpecV3{Cloud: CloudAWS},
			want: &AppV3{
				Kind:     "app",
				Version:  "v3",
				Metadata: Metadata{Name: "myaws", Namespace: "default"},
				Spec:     AppSpecV3{URI: "cloud://AWS", Cloud: CloudAWS},
			},
			wantErr: require.NoError,
		},
		{
			name: "aws app console",
			meta: Metadata{Name: "myaws"},
			spec: AppSpecV3{Cloud: CloudAWS, URI: constants.AWSConsoleURL},
			want: &AppV3{
				Kind:     "app",
				Version:  "v3",
				Metadata: Metadata{Name: "myaws", Namespace: "default"},
				Spec:     AppSpecV3{URI: constants.AWSConsoleURL, Cloud: CloudAWS},
			},
			wantErr: require.NoError,
		},
		{
			name: "aws app using integration",
			meta: Metadata{Name: "myaws"},
			spec: AppSpecV3{Cloud: CloudAWS, URI: constants.AWSConsoleURL, Integration: "my-integration"},
			want: &AppV3{
				Kind:     "app",
				Version:  "v3",
				Metadata: Metadata{Name: "myaws", Namespace: "default"},
				Spec:     AppSpecV3{URI: constants.AWSConsoleURL, Cloud: CloudAWS, Integration: "my-integration"},
			},
			wantErr: require.NoError,
		},
		{
			name: "app with required apps list",
			meta: Metadata{Name: "clientapp"},
			spec: AppSpecV3{RequiredAppNames: []string{"api22"}, URI: "example.com"},
			want: &AppV3{
				Kind:     "app",
				Version:  "v3",
				Metadata: Metadata{Name: "clientapp", Namespace: "default"},
				Spec:     AppSpecV3{RequiredAppNames: []string{"api22"}, URI: "example.com"},
			},
			wantErr: require.NoError,
		},
		{
			name: "app with basic CORS policy",
			meta: Metadata{Name: "api22"},
			spec: AppSpecV3{
				URI: "example.com",
				CORS: &CORSPolicy{
					AllowedOrigins:   []string{"https://client.example.com"},
					AllowedMethods:   []string{"GET", "POST"},
					AllowedHeaders:   []string{"Content-Type", "Authorization"},
					AllowCredentials: true,
					MaxAge:           86400,
				},
			},
			want: &AppV3{
				Kind:    "app",
				Version: "v3",
				Metadata: Metadata{
					Name:      "api22",
					Namespace: "default",
				},
				Spec: AppSpecV3{
					URI: "example.com",
					CORS: &CORSPolicy{
						AllowedOrigins:   []string{"https://client.example.com"},
						AllowedMethods:   []string{"GET", "POST"},
						AllowedHeaders:   []string{"Content-Type", "Authorization"},
						AllowCredentials: true,
						MaxAge:           86400,
					},
				},
			},
			wantErr: require.NoError,
		},
		{
			name: "app with no CORS policy",
			meta: Metadata{Name: "api22"},
			spec: AppSpecV3{
				URI: "example.com",
			},
			want: &AppV3{
				Kind:    "app",
				Version: "v3",
				Metadata: Metadata{
					Name:      "api22",
					Namespace: "default",
				},
				Spec: AppSpecV3{
					URI: "example.com",
					// CORS is nil, indicating no CORS policy
				},
			},
			wantErr: require.NoError,
		},
		{
			name:    "invalid cloud identifier",
			meta:    Metadata{Name: "dummy"},
			spec:    AppSpecV3{Cloud: "dummy"},
			want:    nil,
			wantErr: require.Error,
		},
		{
			name: "mcp with command",
			meta: Metadata{
				Name: "mcp-everything",
			},
			spec: AppSpecV3{
				MCP: &MCP{
					Command:       "docker",
					Args:          []string{"run", "-i", "--rm", "mcp/everything"},
					RunAsHostUser: "docker",
				},
			},
			want: &AppV3{
				Kind:    "app",
				SubKind: "mcp",
				Version: "v3",
				Metadata: Metadata{
					Name:      "mcp-everything",
					Namespace: "default",
				},
				Spec: AppSpecV3{
					URI: "mcp+stdio://",
					MCP: &MCP{
						Command:       "docker",
						Args:          []string{"run", "-i", "--rm", "mcp/everything"},
						RunAsHostUser: "docker",
					},
				},
			},
			wantErr: require.NoError,
		},
		{
			name: "mcp missing spec",
			meta: Metadata{
				Name: "mcp-missing-run-as",
			},
			spec: AppSpecV3{
				URI: "mcp+stdio://",
			},
			wantErr: require.Error,
		},
		{
			name: "mcp missing run_as_host_user",
			meta: Metadata{
				Name: "mcp-missing-spec",
			},
			spec: AppSpecV3{
				MCP: &MCP{
					Command: "docker",
					Args:    []string{"run", "-i", "--rm", "mcp/everything"},
				},
			},
			wantErr: require.Error,
		},
		{
			name: "mcp demo",
			meta: Metadata{
				Name: "teleport-mcp-demo",
				Labels: map[string]string{
					TeleportInternalResourceType: DemoResource,
				},
			},
			spec: AppSpecV3{
				URI: "mcp+stdio://teleport-mcp-demo",
			},
			want: &AppV3{
				Kind:    "app",
				SubKind: "mcp",
				Version: "v3",
				Metadata: Metadata{
					Name:      "teleport-mcp-demo",
					Namespace: "default",
					Labels: map[string]string{
						TeleportInternalResourceType: DemoResource,
					},
				},
				Spec: AppSpecV3{
					URI: "mcp+stdio://teleport-mcp-demo",
				},
			},
			wantErr: require.NoError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual, err := NewAppV3(tt.meta, tt.spec)
			tt.wantErr(t, err)
			require.Equal(t, tt.want, actual)
		})
	}
}

func TestPortRangesContains(t *testing.T) {
	portRanges := PortRanges([]*PortRange{
		&PortRange{Port: 10, EndPort: 20},
		&PortRange{Port: 42},
	})

	tests := []struct {
		port int
		want require.BoolAssertionFunc
	}{
		{port: 10, want: require.True},
		{port: 20, want: require.True},
		{port: 15, want: require.True},
		{port: 42, want: require.True},
		{port: 30, want: require.False},
		{port: 0, want: require.False},
	}

	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.port), func(t *testing.T) {
			tt.want(t, portRanges.Contains(tt.port))
		})
	}
}

func hasNoErr(t require.TestingT, err error, msgAndArgs ...interface{}) {
	require.NoError(t, err, msgAndArgs...)
}

func hasErrTypeBadParameter(t require.TestingT, err error, msgAndArgs ...interface{}) {
	require.True(t, trace.IsBadParameter(err), "expected bad parameter error, got %+v", err)
}

func hasErrTypeBadParameterAndContains(msg string) require.ErrorAssertionFunc {
	return func(t require.TestingT, err error, msgAndArgs ...interface{}) {
		require.True(t, trace.IsBadParameter(err), "err should be trace.BadParameter")
		require.ErrorContains(t, err, msg, msgAndArgs...)
	}
}

func hasErrAndContains(msg string) require.ErrorAssertionFunc {
	return func(t require.TestingT, err error, msgAndArgs ...interface{}) {
		require.ErrorContains(t, err, msg, msgAndArgs...)
	}
}

func TestGetMCPServerTransportType(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{
			name: "stdio",
			uri:  "mcp+stdio://",
			want: MCPTransportStdio,
		},
		{
			name: "unknown",
			uri:  "http://localhost",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, GetMCPServerTransportType(tt.uri))
		})
	}
}

func TestDeduplicateApps(t *testing.T) {
	var apps []Application
	for _, name := range []string{"a", "b", "c", "b", "a", "d"} {
		app_, err := NewAppV3(Metadata{
			Name: name,
		}, AppSpecV3{
			URI: "localhost:3080",
		})
		require.NoError(t, err)
		apps = append(apps, app_)
	}

	deduped := DeduplicateApps(apps)
	require.Equal(t, []string{"a", "b", "c", "d"}, slices.Collect(ResourceNames(deduped)))
}
