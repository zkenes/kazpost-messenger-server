// Copyright (c) 2017-present TinkerTech, Inc. All Rights Reserved.
// See License.txt for license information.

package rpcplugintest

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/mattermost/mattermost-server/model"
	"github.com/mattermost/mattermost-server/plugin"
	"github.com/mattermost/mattermost-server/plugin/plugintest"
)

type SupervisorProviderFunc = func(*model.BundleInfo) (plugin.Supervisor, error)

func TestSupervisorProvider(t *testing.T, sp SupervisorProviderFunc) {
	for name, f := range map[string]func(*testing.T, SupervisorProviderFunc){
		"Supervisor":                           testSupervisor,
		"Supervisor_InvalidExecutablePath":     testSupervisor_InvalidExecutablePath,
		"Supervisor_NonExistentExecutablePath": testSupervisor_NonExistentExecutablePath,
		"Supervisor_StartTimeout":              testSupervisor_StartTimeout,
		"Supervisor_PluginCrash":               testSupervisor_PluginCrash,
	} {
		t.Run(name, func(t *testing.T) { f(t, sp) })
	}
}

func testSupervisor(t *testing.T, sp SupervisorProviderFunc) {
	dir, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	backend := filepath.Join(dir, "backend.exe")
	CompileGo(t, `
		package main

		import (
			"github.com/mattermost/mattermost-server/plugin/rpcplugin"
		)

		type MyPlugin struct {}

		func main() {
			rpcplugin.Main(&MyPlugin{})
		}
	`, backend)

	ioutil.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id": "foo", "backend": {"executable": "backend.exe"}}`), 0600)

	bundle := model.BundleInfoForPath(dir)
	supervisor, err := sp(bundle)
	require.NoError(t, err)
	require.NoError(t, supervisor.Start(nil))
	require.NoError(t, supervisor.Stop())
}

func testSupervisor_InvalidExecutablePath(t *testing.T, sp SupervisorProviderFunc) {
	dir, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	ioutil.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id": "foo", "backend": {"executable": "/foo/../../backend.exe"}}`), 0600)

	bundle := model.BundleInfoForPath(dir)
	supervisor, err := sp(bundle)
	assert.Nil(t, supervisor)
	assert.Error(t, err)
}

func testSupervisor_NonExistentExecutablePath(t *testing.T, sp SupervisorProviderFunc) {
	dir, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	ioutil.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id": "foo", "backend": {"executable": "thisfileshouldnotexist"}}`), 0600)

	bundle := model.BundleInfoForPath(dir)
	supervisor, err := sp(bundle)
	require.NotNil(t, supervisor)
	require.NoError(t, err)

	require.Error(t, supervisor.Start(nil))
}

// If plugin development goes really wrong, let's make sure plugin activation won't block forever.
func testSupervisor_StartTimeout(t *testing.T, sp SupervisorProviderFunc) {
	dir, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	backend := filepath.Join(dir, "backend.exe")
	CompileGo(t, `
		package main

		func main() {
			for {
			}
		}
	`, backend)

	ioutil.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id": "foo", "backend": {"executable": "backend.exe"}}`), 0600)

	bundle := model.BundleInfoForPath(dir)
	supervisor, err := sp(bundle)
	require.NoError(t, err)
	require.Error(t, supervisor.Start(nil))
}

// Crashed plugins should be relaunched.
func testSupervisor_PluginCrash(t *testing.T, sp SupervisorProviderFunc) {
	dir, err := ioutil.TempDir("", "")
	require.NoError(t, err)
	defer os.RemoveAll(dir)

	backend := filepath.Join(dir, "backend.exe")
	CompileGo(t, `
		package main

		import (
			"os"

			"github.com/mattermost/mattermost-server/plugin"
			"github.com/mattermost/mattermost-server/plugin/rpcplugin"
		)

		type Configuration struct {
			ShouldExit bool
		}

		type MyPlugin struct {
			config Configuration
		}

		func (p *MyPlugin) OnActivate(api plugin.API) error {
			api.LoadPluginConfiguration(&p.config)
			return nil
		}

		func (p *MyPlugin) OnDeactivate() error {
			if p.config.ShouldExit {
				os.Exit(1)
			}
			return nil
		}

		func main() {
			rpcplugin.Main(&MyPlugin{})
		}
	`, backend)

	ioutil.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"id": "foo", "backend": {"executable": "backend.exe"}}`), 0600)

	var api plugintest.API
	shouldExit := true
	api.On("LoadPluginConfiguration", mock.MatchedBy(func(x interface{}) bool { return true })).Return(func(dest interface{}) error {
		err := json.Unmarshal([]byte(fmt.Sprintf(`{"ShouldExit": %v}`, shouldExit)), dest)
		shouldExit = false
		return err
	})

	bundle := model.BundleInfoForPath(dir)
	supervisor, err := sp(bundle)
	require.NoError(t, err)
	require.NoError(t, supervisor.Start(&api))

	failed := false
	recovered := false
	for i := 0; i < 30; i++ {
		if supervisor.Hooks().OnDeactivate() == nil {
			require.True(t, failed)
			recovered = true
			break
		} else {
			failed = true
		}
		time.Sleep(time.Millisecond * 100)
	}
	assert.True(t, recovered)
	require.NoError(t, supervisor.Stop())
}
