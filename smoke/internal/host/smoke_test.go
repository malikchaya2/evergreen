package host

import (
	"context"
	"fmt"
	"syscall"
	"testing"

	"github.com/evergreen-ci/evergreen/agent/globals"
	"github.com/evergreen-ci/evergreen/smoke/internal"
	"github.com/mongodb/grip"
	"github.com/mongodb/grip/message"
	"github.com/pkg/errors"
)

// TestSmokeHostTask runs the smoke test for a host task.
func TestSmokeHostTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	params := GetSmokeTestParamsFromEnv(t)
	grip.Info(message.Fields{
		"message": "got smoke test parameters",
		"params":  fmt.Sprintf("%#v", params),
	})

	// // Manually update admin settings in DB for GitHub App credentials.
	// //chayatodo: do this for the smoke test
	// testSettings.AuthConfig = integrationSettings.AuthConfig
	// err := testSettings.AuthConfig.Set(context.Background())
	// require.NoError(t, err, "Error updating auth config settings in DB")

	// if val, ok := integrationSettings.Expansions[evergreen.GithubAppPrivateKey]; ok {
	// 	testSettings.Expansions[evergreen.GithubAppPrivateKey] = val
	// }
	// err = testSettings.Set(context.Background())

	//manually update admin settings in DB for GitHub App credentials.

	settings, err := GetConfig(ctx)
	s.NoError(err)
	s.NotNil(settings)

	appServerCmd := internal.StartAppServer(ctx, t, params.APIParams)
	defer func() {
		if appServerCmd != nil {
			grip.Error(errors.Wrap(appServerCmd.Signal(ctx, syscall.SIGTERM), "stopping app server after test completion"))
		}
	}()

	agentCmd := internal.StartAgent(ctx, t, params.APIParams, globals.HostMode, params.ExecModeID, params.ExecModeSecret)
	defer func() {
		if agentCmd != nil {
			grip.Error(errors.Wrap(agentCmd.Signal(ctx, syscall.SIGTERM), "stopping agent after test completion"))
		}
	}()

	RunHostTaskPatchTest(ctx, t, params)
}
