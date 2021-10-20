/*
Copyright 2021 Gravitational, Inc.
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

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	goMainFile = `
	package main

	import (
		"github.com/gravitational/teleport/api"
	)

	func main() {
		api.func()
	}`
)

func TestUpdateGoModulePath(t *testing.T) {
	modDir := t.TempDir()
	modFilePath := filepath.Join(modDir, "go.mod")

	modPath := "github.com/gravitational/teleport/api"
	goModFile := `module github.com/gravitational/teleport/api

go 1.15

require github.com/gravitational/teleport/api v0.0.0

require (
	github.com/gravitational/teleport/api v0.0.0
	github.com/gravitational/teleport/api v0.0.0 // indirect
)

replace github.com/gravitational/teleport/api => ./api

replace github.com/gravitational/teleport/api v0.0.0 => ./api

replace (
	github.com/gravitational/teleport/api v0.0.0 => ./api
)
`

	newVersion := "2.1.3"
	newModPath := modPath + "/v2"
	newGoModFile := `module github.com/gravitational/teleport/api/v2

go 1.15

require github.com/gravitational/teleport/api/v2 v2.1.3

require (
	github.com/gravitational/teleport/api/v2 v2.1.3
	github.com/gravitational/teleport/api/v2 v2.1.3 // indirect
)

replace github.com/gravitational/teleport/api/v2 => ./api

replace github.com/gravitational/teleport/api/v2 v2.1.3 => ./api

replace (
	github.com/gravitational/teleport/api/v2 v2.1.3 => ./api
)
`

	err := os.WriteFile(modFilePath, []byte(goModFile), 0660)
	require.NoError(t, err)

	err = updateGoModFile(modDir, modPath, newModPath, newVersion)
	require.NoError(t, err)

	bytes, err := os.ReadFile(modFilePath)
	require.NoError(t, err)

	fmt.Println("\n\nBREAK\n\ns")
	fmt.Println(newGoModFile)

	fmt.Println("\n\nBREAK\n\ns")
	fmt.Println(string(bytes))

	require.Equal(t, newGoModFile, string(bytes))
}
